package yamlsplice

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// text wraps the raw bytes and a line table.
type text struct {
	buf   []byte
	lines []int // byte offset of the start of each line
}

func newText(buf []byte) *text {
	t := &text{buf: buf}
	t.lines = append(t.lines, 0)
	if bytes.HasPrefix(buf, []byte("\xef\xbb\xbf")) {
		t.lines[0] = 3
	}
	for i, c := range buf {
		if c == '\n' {
			t.lines = append(t.lines, i+1)
		}
	}
	return t
}

// off converts a 1-based line and 1-based rune column into a byte offset.
func (t *text) off(line, col int) int {
	if line < 1 {
		line = 1
	}
	if line > len(t.lines) {
		return len(t.buf)
	}
	p := t.lines[line-1]
	for i := 1; i < col && p < len(t.buf); i++ {
		_, w := utf8.DecodeRune(t.buf[p:])
		p += w
	}
	return p
}

func (t *text) lineIdx(off int) int {
	return sort.Search(len(t.lines), func(i int) bool { return t.lines[i] > off }) - 1
}

// lineStart returns the offset of the first byte of the line containing off.
func (t *text) lineStart(off int) int {
	if off > len(t.buf) {
		off = len(t.buf)
	}
	return t.lines[t.lineIdx(off)]
}

// lineEnd returns the offset of the newline (or len) ending the line containing off.
func (t *text) lineEnd(off int) int {
	i := bytes.IndexByte(t.buf[min(off, len(t.buf)):], '\n')
	if i < 0 {
		return len(t.buf)
	}
	e := off + i
	if e > 0 && t.buf[e-1] == '\r' {
		e--
	}
	return e
}

// nextLine returns the start offset of the line after the one containing off,
// or len(buf) when off is on the last line.
func (t *text) nextLine(off int) int {
	i := bytes.IndexByte(t.buf[min(off, len(t.buf)):], '\n')
	if i < 0 {
		return len(t.buf)
	}
	return off + i + 1
}

// col returns the byte column of off within its line.
func (t *text) col(off int) int { return off - t.lineStart(off) }

func isSpace(c byte) bool { return c == ' ' || c == '\t' }
func isBreak(c byte) bool { return c == '\n' || c == '\r' }

// skipSpaces skips blanks and tabs.
func (t *text) skipSpaces(p int) int {
	for p < len(t.buf) && isSpace(t.buf[p]) {
		p++
	}
	return p
}

// skipSig skips whitespace, line breaks and comments.
func (t *text) skipSig(p int) int {
	for p < len(t.buf) {
		c := t.buf[p]
		switch {
		case isSpace(c) || isBreak(c):
			p++
		case c == '#' && (p == 0 || isSpace(t.buf[p-1]) || isBreak(t.buf[p-1])):
			p = t.lineEnd(p)
		default:
			return p
		}
	}
	return p
}

// skipProps skips anchors and tags starting at p, and the white space after them.
func (t *text) skipProps(p int) int {
	found := false
	for {
		q := t.skipSpaces(p)
		if q < len(t.buf) && (t.buf[q] == '&' || t.buf[q] == '!') {
			found = true
			for q < len(t.buf) && !isSpace(t.buf[q]) && !isBreak(t.buf[q]) {
				q++
			}
			p = q
			continue
		}
		break
	}
	if found {
		return t.skipSig(p)
	}
	return p
}

// node is a located YAML node.
type node struct {
	y      *yaml.Node
	parent *node
	start  int // start including anchors and tags
	tstart int // start of the node's own text
	end    int // end of the node's text (exclusive)
	null   bool
	flow   bool
	col    int // block collections: column of the first key or dash
	pairs  []*pair
	items  []*item
}

type pair struct {
	key      *node
	val      *node
	colon    int // offset of ':' or -1
	explicit bool
	owner    *node
}

type item struct {
	val   *node
	dash  int // offset of '-' or -1 in flow sequences
	owner *node
}

func (p *pair) end() int {
	if !p.val.null {
		return p.val.end
	}
	if p.colon >= 0 {
		return p.colon + 1
	}
	return p.key.end
}

func (i *item) start() int {
	if i.dash >= 0 {
		return i.dash
	}
	return i.val.start
}

func (i *item) end() int {
	if i.val.null && i.dash >= 0 {
		return i.dash + 1
	}
	return i.val.end
}

type docIndex struct {
	root *node // nil for an empty document
	y    *yaml.Node
}

type index struct {
	t    *text
	docs []*docIndex
}

// checkBreaks rejects line breaks other than LF and CRLF. The parser counts
// them as line breaks, which would put every later position out of step.
func checkBreaks(buf []byte) error {
	for i, c := range buf {
		if c == '\r' && (i+1 >= len(buf) || buf[i+1] != '\n') {
			return fmt.Errorf("%w: bare carriage return line break", ErrUnsupported)
		}
	}
	if bytes.Contains(buf, []byte("\u0085")) || bytes.Contains(buf, []byte("\u2028")) || bytes.Contains(buf, []byte("\u2029")) {
		return fmt.Errorf("%w: NEL, LS or PS line separator", ErrUnsupported)
	}
	return nil
}

func buildIndex(buf []byte) (*index, error) {
	if err := checkBreaks(buf); err != nil {
		return nil, err
	}
	t := newText(buf)
	ix := &index{t: t}
	dec := yaml.NewDecoder(bytes.NewReader(buf))
	for {
		var y yaml.Node
		err := dec.Decode(&y)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrSyntax, err)
		}
		di := &docIndex{y: &y}
		if len(y.Content) > 0 {
			r, err := t.build(y.Content[0], nil, -1)
			if err != nil {
				return nil, err
			}
			di.root = r
		}
		ix.docs = append(ix.docs, di)
	}
	return ix, nil
}

func (t *text) locErr(y *yaml.Node, what string) error {
	return fmt.Errorf("%w: cannot locate %s at line %d column %d", ErrUnsupported, what, y.Line, y.Column)
}

func (t *text) build(y *yaml.Node, parent *node, parentIndent int) (*node, error) {
	n := &node{y: y, parent: parent}
	n.start = t.off(y.Line, y.Column)
	if n.start > len(t.buf) {
		n.start = len(t.buf)
	}
	switch y.Kind {
	case yaml.AliasNode:
		n.tstart = n.start
		p := n.start
		if p >= len(t.buf) || t.buf[p] != '*' {
			return nil, t.locErr(y, "alias")
		}
		p++
		for p < len(t.buf) && !isSpace(t.buf[p]) && !isBreak(t.buf[p]) && t.buf[p] != ',' && t.buf[p] != ']' && t.buf[p] != '}' {
			p++
		}
		n.end = p
	case yaml.ScalarNode:
		if err := t.buildScalar(n, parentIndent); err != nil {
			return nil, err
		}
	case yaml.MappingNode, yaml.SequenceNode:
		if y.Style&yaml.FlowStyle != 0 {
			n.flow = true
			n.tstart = t.skipProps(n.start)
			open := byte('[')
			if y.Kind == yaml.MappingNode {
				open = '{'
			}
			if n.tstart >= len(t.buf) || t.buf[n.tstart] != open {
				return nil, t.locErr(y, "flow collection")
			}
			e, ok := t.flowEnd(n.tstart)
			if !ok {
				return nil, t.locErr(y, "flow collection end")
			}
			n.end = e
			if err := t.buildChildren(n, y); err != nil {
				return nil, err
			}
		} else {
			n.tstart = t.skipProps(n.start)
			if n.tstart == n.start {
				n.tstart = t.skipSig(n.start)
			}
			n.col = t.col(n.tstart)
			if err := t.buildChildren(n, y); err != nil {
				return nil, err
			}
		}
	default:
		return nil, t.locErr(y, "node")
	}
	return n, nil
}

func (t *text) buildChildren(n *node, y *yaml.Node) error {
	if y.Kind == yaml.MappingNode {
		prev := n.tstart
		if n.flow {
			prev = n.tstart + 1
		}
		for i := 0; i+1 < len(y.Content); i += 2 {
			k, err := t.build(y.Content[i], n, -1)
			if err != nil {
				return err
			}
			pr := &pair{key: k, owner: n, colon: -1}
			// explicit key indicator
			q := k.start - 1
			for q >= 0 && isSpace(t.buf[q]) {
				q--
			}
			if q >= 0 && t.buf[q] == '?' && (q+1 < len(t.buf) && isSpace(t.buf[q+1])) {
				pr.explicit = true
			}
			c := k.end
			if pr.explicit {
				c = t.skipSig(c)
			} else {
				c = t.skipSpaces(c)
			}
			if c < len(t.buf) && t.buf[c] == ':' {
				pr.colon = c
			}
			indent := n.col
			if n.flow {
				indent = -1
			}
			v, err := t.build(y.Content[i+1], n, indent)
			if err != nil {
				return err
			}
			if v.null && pr.colon >= 0 {
				v.start, v.tstart, v.end = pr.colon+1, pr.colon+1, pr.colon+1
			}
			pr.val = v
			n.pairs = append(n.pairs, pr)
			_ = prev
			prev = pr.end()
		}
		if !n.flow && len(n.pairs) > 0 {
			n.end = n.pairs[len(n.pairs)-1].end()
		}
		return nil
	}
	prev := n.tstart
	for i, c := range y.Content {
		it := &item{owner: n, dash: -1}
		indent := -1
		if !n.flow {
			p := prev
			if i > 0 {
				p = t.skipSig(prev)
			}
			if p >= len(t.buf) || t.buf[p] != '-' {
				return t.locErr(c, "sequence dash")
			}
			it.dash = p
			indent = n.col
		}
		v, err := t.build(c, n, indent)
		if err != nil {
			return err
		}
		if v.null && it.dash >= 0 {
			v.start, v.tstart, v.end = it.dash+1, it.dash+1, it.dash+1
		}
		it.val = v
		n.items = append(n.items, it)
		prev = it.end()
	}
	if !n.flow && len(n.items) > 0 {
		n.end = n.items[len(n.items)-1].end()
	}
	return nil
}

func (t *text) buildScalar(n *node, parentIndent int) error {
	y := n.y
	n.tstart = t.skipProps(n.start)
	p := n.tstart
	switch {
	case y.Style&yaml.DoubleQuotedStyle != 0:
		if p >= len(t.buf) || t.buf[p] != '"' {
			return t.locErr(y, "double-quoted scalar")
		}
		p++
		for p < len(t.buf) && t.buf[p] != '"' {
			if t.buf[p] == '\\' {
				p++
			}
			p++
		}
		if p >= len(t.buf) {
			return t.locErr(y, "double-quoted scalar end")
		}
		n.end = p + 1
	case y.Style&yaml.SingleQuotedStyle != 0:
		if p >= len(t.buf) || t.buf[p] != '\'' {
			return t.locErr(y, "single-quoted scalar")
		}
		p++
		for p < len(t.buf) {
			if t.buf[p] == '\'' {
				if p+1 < len(t.buf) && t.buf[p+1] == '\'' {
					p += 2
					continue
				}
				break
			}
			p++
		}
		if p >= len(t.buf) {
			return t.locErr(y, "single-quoted scalar end")
		}
		n.end = p + 1
	case y.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
		if p >= len(t.buf) || (t.buf[p] != '|' && t.buf[p] != '>') {
			return t.locErr(y, "block scalar")
		}
		end := t.lineEnd(p)
		q := t.nextLine(p)
		for q < len(t.buf) {
			le := t.lineEnd(q)
			line := t.buf[q:le]
			trimmed := bytes.TrimLeft(line, " ")
			if len(bytes.TrimSpace(line)) == 0 {
				q = t.nextLine(q)
				continue
			}
			ind := len(line) - len(trimmed)
			if ind <= parentIndent {
				break
			}
			if parentIndent < 0 && ind == 0 && (bytes.HasPrefix(line, []byte("---")) || bytes.HasPrefix(line, []byte("..."))) {
				break
			}
			end = le
			q = t.nextLine(q)
		}
		n.end = end
	default:
		if y.Value == "" && y.Tag != "!!str" || y.Value == "" && p >= len(t.buf) {
			n.null = true
			n.end = p
			return nil
		}
		if y.Value == "" {
			// tagged empty string: nothing to locate
			n.null = true
			n.end = p
			return nil
		}
		for _, r := range y.Value {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				continue
			}
			for p < len(t.buf) && (isSpace(t.buf[p]) || isBreak(t.buf[p])) {
				p++
			}
			if p >= len(t.buf) {
				return t.locErr(y, "plain scalar")
			}
			c, w := utf8.DecodeRune(t.buf[p:])
			if c != r {
				return t.locErr(y, "plain scalar")
			}
			p += w
		}
		n.end = p
	}
	return nil
}

// flowEnd returns the offset just past the bracket matching the one at p.
func (t *text) flowEnd(p int) (int, bool) {
	depth := 0
	var prev byte = '['
	for p < len(t.buf) {
		c := t.buf[p]
		switch {
		case c == '#' && (isSpace(t.buf[p-1]) || isBreak(t.buf[p-1])):
			p = t.lineEnd(p)
			continue
		case (c == '"' || c == '\'') && (prev == '[' || prev == '{' || prev == ',' || prev == ':' || prev == '?'):
			q := p + 1
			for q < len(t.buf) {
				if c == '"' && t.buf[q] == '\\' {
					q += 2
					continue
				}
				if t.buf[q] == c {
					if c == '\'' && q+1 < len(t.buf) && t.buf[q+1] == '\'' {
						q += 2
						continue
					}
					break
				}
				q++
			}
			if q >= len(t.buf) {
				return 0, false
			}
			p = q + 1
			prev = c
			continue
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
			if depth == 0 {
				return p + 1, true
			}
		}
		if !isSpace(c) && !isBreak(c) {
			prev = c
		}
		p++
	}
	return 0, false
}
