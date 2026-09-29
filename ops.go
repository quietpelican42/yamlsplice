package yamlsplice

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Position says where Insert places a new key.
type Position struct {
	kind string // "", "first", "after", "before"
	key  string
}

// First places the new key before all existing keys.
func First() Position { return Position{kind: "first"} }

// After places the new key directly after the sibling key.
func After(sibling string) Position { return Position{kind: "after", key: sibling} }

// Before places the new key directly before the sibling key.
func Before(sibling string) Position { return Position{kind: "before", key: sibling} }

func pad(n int) string { return strings.Repeat(" ", max(n, 0)) }

// baseCol returns the column that children of the value at r are measured from.
func (d *Doc) baseCol(r ref) int {
	t := d.s.ix.t
	switch {
	case r.pair != nil:
		return t.col(r.pair.key.start)
	case r.item != nil:
		if r.item.dash < 0 {
			return t.col(r.item.val.start)
		}
		return t.col(r.item.dash)
	}
	return 0
}

// container returns the collection whose layout applies to a value at r.
func containerOf(r ref) *node {
	if r.n.parent != nil {
		return r.n.parent
	}
	return r.n
}

// Set replaces the value at path. If the last segment names a key that does
// not exist in an existing mapping, the key is added at the end.
//
// value may be a string, bool, integer, float, nil, Raw, a slice or a map (maps
// are written in sorted key order). A string keeps the quoting of the value it
// replaces when it can, and is quoted when it would otherwise read back as
// another type or as invalid YAML.
func (d *Doc) Set(path string, value any) error {
	segs, err := parsePath(path)
	if err != nil {
		return err
	}
	y2, err := toNode(value)
	if err != nil {
		return err
	}
	di := d.idx()
	r, err := di.resolve(segs)
	if err != nil {
		if errors.Is(err, ErrNotFound) && len(segs) > 0 && !segs[len(segs)-1].isIndex {
			if pr, perr := di.resolve(segs[:len(segs)-1]); perr == nil && pr.n != nil &&
				(pr.n.y.Kind == yaml.MappingNode || pr.n.null) {
				return d.insert(pr, segs[len(segs)-1].key, y2, Position{})
			}
		}
		return err
	}
	if r.n == nil {
		return fmt.Errorf("%w: document is empty", ErrNotFound)
	}
	return d.replace(r, y2)
}

func (d *Doc) replace(r ref, y2 *yaml.Node) error {
	di := d.idx()
	n := r.n
	lay := di.layoutFor(containerOf(r))
	if c := containerOf(r); c != n && !lay.flow && preferFlow(c) {
		lay.flow = true
	}
	if n.flow {
		lay.flow = true
	}
	h := &hook{kind: "set", target: n.y, newNode: y2}
	want := lay.want
	if n.y.Kind == yaml.ScalarNode && n.y.ShortTag() == "!!str" && !n.null {
		want = styleOf(n.y)
		if n.y.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			want = lay.want
		}
	}
	base := d.baseCol(r)
	// Block scalar staying a block scalar.
	if n.y.Kind == yaml.ScalarNode && n.y.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 &&
		y2.Kind == yaml.ScalarNode && y2.ShortTag() == "!!str" && strings.Contains(y2.Value, "\n") {
		hdr := blockHeader(y2.Value, n.y.Style&yaml.FoldedStyle != 0, lay.step)
		body := blockBody(y2.Value, base+lay.step)
		return d.commit([]splice{{n.tstart, n.end, hdr + body}}, h)
	}
	rd := lay.renderValue(y2, base, want, r.item != nil)
	if y2.Kind == yaml.ScalarNode && y2.ShortTag() == "!!str" && strings.Contains(y2.Value, "\n") {
		rd = rendered{inline: doubleQuote(y2.Value)}
	}
	if rd.lines == nil {
		text := rd.inline
		switch {
		case n.null:
			return d.commit([]splice{{n.start, n.start, " " + text}}, h)
		case n.y.Kind == yaml.ScalarNode && n.y.Style&yaml.TaggedStyle == 0, n.flow:
			return d.commit([]splice{{n.tstart, n.end, text}}, h)
		default:
			prefix := ""
			if n.y.Anchor != "" {
				prefix = "&" + n.y.Anchor + " "
			}
			if isBlockColl(n) {
				return d.replaceBlockWithInline(r, prefix+text, h)
			}
			return d.commit([]splice{{n.start, n.end, prefix + text}}, h)
		}
	}
	return d.replaceWithBlock(r, y2, lay, h)
}

// replaceBlockWithInline turns a block collection value into inline text.
func (d *Doc) replaceBlockWithInline(r ref, text string, h *hook) error {
	t := d.s.ix.t
	n := r.n
	from := d.afterIndicator(r)
	q := t.skipSpaces(from)
	if q < len(t.buf) && t.buf[q] == '#' {
		// keep the comment on the key line: put the value before it
		le := t.lineEnd(q)
		return d.commit([]splice{{from, q, " " + text + " "}, {le, n.end, ""}}, h)
	}
	return d.commit([]splice{{from, n.end, " " + text}}, h)
}

// afterIndicator returns the offset just after the ':' or '-' introducing r.
func (d *Doc) afterIndicator(r ref) int {
	switch {
	case r.pair != nil && r.pair.colon >= 0:
		return r.pair.colon + 1
	case r.item != nil:
		return r.item.dash + 1
	}
	return r.n.start
}

func (d *Doc) replaceWithBlock(r ref, y2 *yaml.Node, lay layout, h *hook) error {
	t := d.s.ix.t
	n := r.n
	base := d.baseCol(r)
	switch {
	case r.pair == nil && r.item == nil:
		// root
		var lines []string
		if y2.Kind == yaml.MappingNode {
			lines = lay.blockMapLines(y2, 0)
		} else {
			lines = lay.blockSeqLines(y2, 0)
		}
		return d.commit([]splice{{n.start, n.end, strings.Join(lines, "\n")}}, h)
	case r.item != nil:
		var lines []string
		if y2.Kind == yaml.MappingNode {
			lines = lay.blockMapLines(y2, base+2)
		} else {
			lines = lay.blockSeqLines(y2, base+2)
		}
		txt := strings.TrimLeft(strings.Join(lines, "\n"), " ")
		if n.null {
			return d.commit([]splice{{n.start, n.start, " " + txt}}, h)
		}
		return d.commit([]splice{{n.start, n.end, txt}}, h)
	}
	from := d.afterIndicator(r)
	lines := d.linesFor(y2, base, lay)
	body := "\n" + strings.Join(lines, "\n")
	if n.null {
		at := t.lineEnd(from)
		return d.commit([]splice{{at, at, body}}, h)
	}
	// keep a comment that follows the colon on the key line
	q := t.skipSpaces(from)
	start := from
	if q < len(t.buf) && t.buf[q] == '#' {
		start = t.lineEnd(q)
	} else if q < len(t.buf) && isBreak(t.buf[q]) {
		start = q
	}
	if start > n.start && !isBlockColl(n) {
		start = from
	}
	end := n.end
	if isBlockColl(n) || n.y.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return d.commit([]splice{{start, t.lineEnd(end), body}}, h)
	}
	// inline old value: drop it, then add the block after the rest of the line
	at := t.lineEnd(end)
	return d.commit([]splice{{from, end, ""}, {at, at, body}}, h)
}

// linesFor renders a collection y2 as block lines for a value of a key at base.
func (d *Doc) linesFor(y2 *yaml.Node, base int, lay layout) []string {
	if y2.Kind == yaml.MappingNode {
		return lay.blockMapLines(y2, base+lay.step)
	}
	col := base + lay.step
	if lay.seqFlush {
		col = base
	}
	return lay.blockSeqLines(y2, col)
}

func blockHeader(v string, folded bool, step int) string {
	h := "|"
	if folded {
		h = ">"
	}
	first := v
	if i := strings.IndexByte(v, '\n'); i >= 0 {
		first = v[:i]
	}
	if strings.HasPrefix(first, " ") {
		h += fmt.Sprint(step)
	}
	switch {
	case !strings.HasSuffix(v, "\n"):
		h += "-"
	case strings.HasSuffix(v, "\n\n"):
		h += "+"
	}
	return h
}

func blockBody(v string, col int) string {
	trimmed := strings.TrimRight(v, "\n")
	var b strings.Builder
	for _, l := range strings.Split(trimmed, "\n") {
		b.WriteString("\n")
		if l != "" {
			b.WriteString(pad(col) + l)
		}
	}
	if strings.HasSuffix(v, "\n\n") {
		b.WriteString(strings.Repeat("\n", strings.Count(v[len(trimmed):], "\n")-1))
	}
	return b.String()
}

// Rename changes the key at path to newKey. Only the bytes of the key change.
func (d *Doc) Rename(path, newKey string) error {
	segs, err := parsePath(path)
	if err != nil {
		return err
	}
	di, t := d.idx(), d.s.ix.t
	r, err := di.resolve(segs)
	if err != nil {
		return err
	}
	if r.pair == nil {
		return fmt.Errorf("%w: %s is not a mapping key", ErrNotFound, path)
	}
	k := r.pair.key
	if k.y.Kind != yaml.ScalarNode {
		return fmt.Errorf("%w: key is not a scalar", ErrUnsupported)
	}
	for _, p := range r.pair.owner.pairs {
		if p != r.pair && keyMatches(p.key, newKey) {
			return fmt.Errorf("%w: %q", ErrExists, newKey)
		}
	}
	lay := di.layoutFor(r.pair.owner)
	want := styleOf(k.y)
	if k.y.Style&yaml.TaggedStyle != 0 || k.y.Anchor != "" {
		return fmt.Errorf("%w: key with tag or anchor", ErrUnsupported)
	}
	txt := renderKey(newKey, want, lay.quote, lay.flow)
	_ = t
	return d.commit([]splice{{k.tstart, k.end, txt}}, &hook{kind: "rename", target: k.y, newKey: newKey})
}

// Delete removes the mapping key or sequence item at path. The comment lines
// directly above the entry, and comment lines nested under it, are removed
// with it. A header comment at the top of the document is kept. Blank lines are
// never removed.
func (d *Doc) Delete(path string) error {
	segs, err := parsePath(path)
	if err != nil {
		return err
	}
	if len(segs) == 0 {
		return fmt.Errorf("%w: cannot delete the document root", ErrPath)
	}
	di, t := d.idx(), d.s.ix.t
	r, err := di.resolve(segs)
	if err != nil {
		return err
	}
	var owner *node
	var start, end, idx int
	var target *yaml.Node
	switch {
	case r.pair != nil:
		if r.pair.explicit {
			return fmt.Errorf("%w: explicit key", ErrUnsupported)
		}
		owner = r.pair.owner
		for i, p := range owner.pairs {
			if p == r.pair {
				idx = i
			}
		}
		start, end, target = r.pair.key.start, r.pair.end(), r.pair.key.y
	case r.item != nil:
		owner = r.item.owner
		for i, it := range owner.items {
			if it == r.item {
				idx = i
			}
		}
		start, end, target = r.item.start(), r.item.end(), r.item.val.y
	default:
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	h := &hook{kind: "delete", target: target}
	count := len(owner.pairs) + len(owner.items)
	if count == 1 {
		return d.deleteLast(owner, r, h)
	}
	if owner.flow {
		return d.deleteFlow(owner, idx, start, end, h)
	}
	if !t.startsLine(start) {
		// first entry on a "- " line: remove up to the start of the next entry
		var next int
		if idx+1 >= count {
			return fmt.Errorf("%w: entry does not start its line", ErrUnsupported)
		}
		if owner.y.Kind == yaml.MappingNode {
			next = owner.pairs[idx+1].key.start
		} else {
			next = owner.items[idx+1].start()
		}
		return d.commit([]splice{{start, next, ""}}, h)
	}
	from := t.headStart(start)
	to := t.nextLine(end)
	// nested comment lines that are indented deeper than the entry
	col := t.col(start)
	for to < len(t.buf) && t.isCommentLine(to) && indentOf(t, to) > col {
		to = t.nextLine(to)
	}
	return d.commit([]splice{{from, to, ""}}, h)
}

func indentOf(t *text, ls int) int {
	p := ls
	for p < len(t.buf) && isSpace(t.buf[p]) {
		p++
	}
	return p - ls
}

func (t *text) startsLine(off int) bool {
	ls := t.lineStart(off)
	return strings.TrimLeft(string(t.buf[ls:off]), " \t") == ""
}

// deleteLast removes the only entry of a collection, leaving an empty one.
func (d *Doc) deleteLast(owner *node, r ref, h *hook) error {
	t := d.s.ix.t
	empty := "{}"
	if owner.y.Kind == yaml.SequenceNode {
		empty = "[]"
	}
	if owner.flow {
		return d.commit([]splice{{owner.tstart, owner.end, empty}}, h)
	}
	// block: replace the collection's text with the empty flow form
	orn := ref{n: owner}
	if owner.parent != nil {
		for _, p := range owner.parent.pairs {
			if p.val == owner {
				orn.pair = p
			}
		}
		for _, it := range owner.parent.items {
			if it.val == owner {
				orn.item = it
			}
		}
	}
	switch {
	case orn.pair != nil:
		from := d.afterIndicator(orn)
		q := t.skipSpaces(from)
		if q < len(t.buf) && t.buf[q] == '#' {
			return d.commit([]splice{{from, q, " " + empty + " "}, {t.lineEnd(q), owner.end, ""}}, h)
		}
		return d.commit([]splice{{from, owner.end, " " + empty}}, h)
	default:
		return d.commit([]splice{{owner.start, owner.end, empty}}, h)
	}
}

func (d *Doc) deleteFlow(owner *node, idx, start, end int, h *hook) error {
	t := d.s.ix.t
	n := len(owner.pairs) + len(owner.items)
	starts := make([]int, n)
	ends := make([]int, n)
	for i := 0; i < n; i++ {
		if owner.y.Kind == yaml.MappingNode {
			starts[i], ends[i] = owner.pairs[i].key.start, owner.pairs[i].end()
		} else {
			starts[i], ends[i] = owner.items[i].start(), owner.items[i].end()
		}
	}
	if idx < n-1 {
		return d.commit([]splice{{starts[idx], starts[idx+1], ""}}, h)
	}
	// last: remove from the end of the previous entry (its comma stays out)
	e := ends[idx-1]
	_ = t
	return d.commit([]splice{{e, ends[idx], ""}}, h)
}

// Insert adds key with value to the mapping at path. By default the key goes
// after the last existing key, on the line after it, so blank-line groups stay
// intact. Use First, After or Before to choose another place. Insert also
// works on a key whose value is empty (key:), which becomes a mapping.
func (d *Doc) Insert(path, key string, value any, pos ...Position) error {
	segs, err := parsePath(path)
	if err != nil {
		return err
	}
	y2, err := toNode(value)
	if err != nil {
		return err
	}
	r, err := d.idx().resolve(segs)
	if err != nil {
		return err
	}
	var p Position
	if len(pos) > 0 {
		p = pos[0]
	}
	return d.insert(r, key, y2, p)
}

func (d *Doc) insert(r ref, key string, val *yaml.Node, pos Position) error {
	di, t := d.idx(), d.s.ix.t
	m := r.n
	if m == nil {
		return fmt.Errorf("%w: document is empty", ErrNotFound)
	}
	if m.y.Kind == yaml.AliasNode {
		return fmt.Errorf("%w: target is the alias *%s", ErrAlias, m.y.Value)
	}
	if m.null && r.pair != nil {
		return d.insertIntoNull(r, key, val)
	}
	if m.y.Kind != yaml.MappingNode {
		return fmt.Errorf("%w: target is not a mapping", ErrNotFound)
	}
	for _, p := range m.pairs {
		if keyMatches(p.key, key) {
			return fmt.Errorf("%w: %q", ErrExists, key)
		}
	}
	idx := len(m.pairs)
	switch pos.kind {
	case "first":
		idx = 0
	case "after", "before":
		found := -1
		for i, p := range m.pairs {
			if keyMatches(p.key, pos.key) {
				found = i
			}
		}
		if found < 0 {
			return fmt.Errorf("%w: sibling %q", ErrNotFound, pos.key)
		}
		idx = found
		if pos.kind == "after" {
			idx++
		}
	}
	h := &hook{kind: "insert", container: m.y, newNode: val, newKey: key, pos: idx}
	lay := di.layoutFor(m)
	if !lay.flow && preferFlow(m) {
		lay.flow = true
	}
	if m.flow {
		return d.insertFlowPair(m, key, val, idx, lay, h)
	}
	if len(m.pairs) == 0 {
		return fmt.Errorf("%w: empty block mapping", ErrUnsupported)
	}
	ktxt := renderKey(key, lay.keyWant, lay.quote, false)
	rd := lay.renderValue(val, m.col, lay.want, false)
	if val.Kind == yaml.ScalarNode && val.ShortTag() == "!!str" && strings.Contains(val.Value, "\n") {
		rd = rendered{inline: doubleQuote(val.Value)}
	}
	var lines []string
	if rd.lines == nil {
		lines = []string{pad(m.col) + ktxt + ": " + rd.inline}
	} else {
		rd.lines = d.linesFor(val, m.col, lay)
		lines = append([]string{pad(m.col) + ktxt + ":"}, rd.lines...)
	}
	block := strings.Join(lines, "\n")
	if idx == len(m.pairs) || (pos.kind == "after") {
		prev := m.pairs[idx-1]
		at := t.nextLine(prev.end())
		if at >= len(t.buf) && (len(t.buf) == 0 || t.buf[len(t.buf)-1] != '\n') {
			return d.commit([]splice{{len(t.buf), len(t.buf), "\n" + block}}, h)
		}
		return d.commit([]splice{{at, at, block + "\n"}}, h)
	}
	next := m.pairs[idx]
	if t.startsLine(next.key.start) {
		at := t.headStart(next.key.start)
		return d.commit([]splice{{at, at, block + "\n"}}, h)
	}
	// the next key shares a line with "- ": it moves to its own line
	txt := strings.TrimLeft(block, " ") + "\n" + pad(m.col)
	return d.commit([]splice{{next.key.start, next.key.start, txt}}, h)
}

func (d *Doc) insertFlowPair(m *node, key string, val *yaml.Node, idx int, lay layout, h *hook) error {
	lay.flow = true
	txt := renderKey(key, lay.keyWant, lay.quote, true) + ": " + lay.flowText(val)
	switch {
	case len(m.pairs) == 0:
		return d.commit([]splice{{m.tstart, m.end, "{" + txt + "}"}}, h)
	case idx == len(m.pairs):
		at := m.pairs[idx-1].end()
		return d.commit([]splice{{at, at, ", " + txt}}, h)
	default:
		at := m.pairs[idx].key.start
		return d.commit([]splice{{at, at, txt + ", "}}, h)
	}
}

func (d *Doc) insertIntoNull(r ref, key string, val *yaml.Node) error {
	di, t := d.idx(), d.s.ix.t
	m := r.n
	lay := di.layoutFor(m.parent)
	base := t.col(r.pair.key.start)
	wrap := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val}}
	h := &hook{kind: "set", target: m.y, newNode: wrap}
	if lay.flow {
		return d.commit([]splice{{m.start, m.start, " {" + renderKey(key, lay.keyWant, lay.quote, true) + ": " + lay.flowText(val) + "}"}}, h)
	}
	lines := lay.blockMapLines(wrap, base+lay.step)
	at := t.lineEnd(m.start)
	return d.commit([]splice{{at, at, "\n" + strings.Join(lines, "\n")}}, h)
}

// Append adds value at the end of the sequence at path. It works on a key
// whose value is empty (key:), which becomes a sequence.
func (d *Doc) Append(path string, value any) error {
	segs, err := parsePath(path)
	if err != nil {
		return err
	}
	y2, err := toNode(value)
	if err != nil {
		return err
	}
	di, t := d.idx(), d.s.ix.t
	r, err := di.resolve(segs)
	if err != nil {
		return err
	}
	s := r.n
	if s == nil {
		return fmt.Errorf("%w: document is empty", ErrNotFound)
	}
	if s.y.Kind == yaml.AliasNode {
		return fmt.Errorf("%w: target is the alias *%s", ErrAlias, s.y.Value)
	}
	if s.null && r.pair != nil {
		lay := di.layoutFor(s.parent)
		wrap := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{y2}}
		h := &hook{kind: "set", target: s.y, newNode: wrap}
		if lay.flow {
			return d.commit([]splice{{s.start, s.start, " [" + lay.flowText(y2) + "]"}}, h)
		}
		base := t.col(r.pair.key.start)
		lines := d.linesFor(wrap, base, lay)
		at := t.lineEnd(s.start)
		return d.commit([]splice{{at, at, "\n" + strings.Join(lines, "\n")}}, h)
	}
	if s.y.Kind != yaml.SequenceNode {
		return fmt.Errorf("%w: target is not a sequence", ErrNotFound)
	}
	lay := di.layoutFor(s)
	h := &hook{kind: "append", container: s.y, newNode: y2}
	if s.flow {
		lay.flow = true
		txt := lay.flowText(y2)
		if len(s.items) == 0 {
			return d.commit([]splice{{s.tstart, s.end, "[" + txt + "]"}}, h)
		}
		last := s.items[len(s.items)-1]
		e := last.end()
		// multi-line flow sequence: match the last item's line layout
		if t.lineIdx(last.start()) != t.lineIdx(s.tstart) && t.lineIdx(s.end-1) != t.lineIdx(last.end()) {
			q := t.skipSpaces(e)
			trailing := q < len(t.buf) && t.buf[q] == ','
			ind := pad(t.col(last.start()))
			if trailing {
				return d.commit([]splice{{q + 1, q + 1, "\n" + ind + txt + ","}}, h)
			}
			return d.commit([]splice{{e, e, ",\n" + ind + txt}}, h)
		}
		return d.commit([]splice{{e, e, ", " + txt}}, h)
	}
	if len(s.items) == 0 {
		return fmt.Errorf("%w: empty block sequence", ErrUnsupported)
	}
	col := s.col
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{y2}}
	if !lay.flow && preferFlow(s) {
		lay.flow = true
	}
	lines := lay.blockSeqLines(seq, col)
	last := s.items[len(s.items)-1]
	at := t.nextLine(last.end())
	block := strings.Join(lines, "\n")
	if at >= len(t.buf) && (len(t.buf) == 0 || t.buf[len(t.buf)-1] != '\n') {
		return d.commit([]splice{{len(t.buf), len(t.buf), "\n" + block}}, h)
	}
	return d.commit([]splice{{at, at, block + "\n"}}, h)
}
