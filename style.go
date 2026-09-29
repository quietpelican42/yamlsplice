package yamlsplice

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// walk visits every located node under n.
func walk(n *node, f func(*node)) {
	f(n)
	for _, p := range n.pairs {
		walk(p.key, f)
		walk(p.val, f)
	}
	for _, it := range n.items {
		walk(it.val, f)
	}
}

func isBlockColl(n *node) bool {
	return !n.flow && (n.y.Kind == yaml.MappingNode || n.y.Kind == yaml.SequenceNode) && !n.null
}

// localStep measures the indentation step under container c from its children.
func localStep(c *node) int {
	for _, p := range c.pairs {
		v := p.val
		if isBlockColl(v) && len(v.pairs)+len(v.items) > 0 {
			if v.y.Kind == yaml.MappingNode && v.col > c.col {
				return v.col - c.col
			}
			if v.y.Kind == yaml.SequenceNode && v.col > c.col {
				return v.col - c.col
			}
		}
	}
	for _, it := range c.items {
		v := it.val
		if isBlockColl(v) && v.y.Kind == yaml.SequenceNode && v.col > c.col+2 {
			return v.col - c.col
		}
	}
	return 0
}

func (di *docIndex) docStep() int {
	if di.root == nil {
		return 2
	}
	hist := map[int]int{}
	walk(di.root, func(n *node) {
		if s := localStep(n); s > 0 && !n.flow {
			hist[s]++
		}
	})
	best, bc := 2, 0
	for k := 1; k <= 12; k++ {
		if hist[k] > bc {
			best, bc = k, hist[k]
		}
	}
	return best
}

// seqFlushOf reports whether block sequences under mapping keys start at the
// key's own column. known is false when no evidence exists.
func seqFlushOf(c *node) (flush, known bool) {
	for _, p := range c.pairs {
		v := p.val
		if isBlockColl(v) && v.y.Kind == yaml.SequenceNode && len(v.items) > 0 {
			return v.col == c.col, true
		}
	}
	return false, false
}

func (di *docIndex) docSeqFlush() bool {
	flush, found := false, false
	if di.root != nil {
		walk(di.root, func(n *node) {
			if found || n.y.Kind != yaml.MappingNode || n.flow {
				return
			}
			flush, found = seqFlushOf(n)
		})
	}
	return flush
}

func countStyles(vals []*node, hist map[quoteStyle]int) {
	for _, v := range vals {
		if v.y.Kind != yaml.ScalarNode || v.y.ShortTag() != "!!str" || v.null {
			continue
		}
		if v.y.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			continue
		}
		st := styleOf(v.y)
		if st != stylePlain || plainSafe(v.y.Value, false) {
			hist[st]++
		}
	}
}

func siblingValues(c *node) []*node {
	var vals []*node
	for _, p := range c.pairs {
		vals = append(vals, p.val)
	}
	for _, it := range c.items {
		vals = append(vals, it.val)
	}
	return vals
}

// layoutFor derives a layout for new content inside container c.
func (di *docIndex) layoutFor(c *node) layout {
	l := layout{want: stylePlain, quote: styleDouble}
	for n := c; n != nil; n = n.parent {
		if n.flow {
			l.flow = true
		}
	}
	l.step = localStep(c)
	if l.step == 0 {
		l.step = di.docStep()
	}
	if f, known := seqFlushOf(c); known {
		l.seqFlush = f
	} else {
		l.seqFlush = di.docSeqFlush()
	}
	hist := map[quoteStyle]int{}
	countStyles(siblingValues(c), hist)
	if hist[stylePlain]+hist[styleSingle]+hist[styleDouble] == 0 && di.root != nil {
		walk(di.root, func(n *node) { countStyles(siblingValues(n), hist) })
	}
	best, bc := stylePlain, hist[stylePlain]
	for _, st := range []quoteStyle{styleDouble, styleSingle} {
		if hist[st] > bc {
			best, bc = st, hist[st]
		}
	}
	l.want = best
	kh := map[quoteStyle]int{}
	for _, p := range c.pairs {
		if p.key.y.Kind == yaml.ScalarNode && p.key.y.Style&yaml.TaggedStyle == 0 {
			kh[styleOf(p.key.y)]++
		}
	}
	l.keyWant = stylePlain
	for _, st := range []quoteStyle{styleDouble, styleSingle} {
		if kh[st] > kh[l.keyWant] {
			l.keyWant = st
		}
	}
	if hist[styleSingle] > hist[styleDouble] {
		l.quote = styleSingle
	}
	return l
}

// preferFlow reports whether new collection values under c should be flow
// style, judged by the sibling collection values.
func preferFlow(c *node) bool {
	fl, bl := 0, 0
	for _, v := range siblingValues(c) {
		if (v.y.Kind == yaml.MappingNode || v.y.Kind == yaml.SequenceNode) && len(v.y.Content) > 0 {
			if v.flow {
				fl++
			} else {
				bl++
			}
		}
	}
	return fl > bl
}

// isCommentLine reports whether the line starting at ls holds only a comment.
func (t *text) isCommentLine(ls int) bool {
	le := t.lineEnd(ls)
	return strings.HasPrefix(strings.TrimLeft(string(t.buf[ls:le]), " \t"), "#")
}

func (t *text) isBlankLine(ls int) bool {
	le := t.lineEnd(ls)
	return strings.TrimSpace(string(t.buf[ls:le])) == ""
}

func (t *text) isDocMarker(ls int) bool {
	l := string(t.buf[ls:t.lineEnd(ls)])
	return l == "---" || l == "..." || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "... ")
}

// headStart returns the start of the comment block that sits directly above
// the line holding off, or that line's start when there is none. A block that
// reaches the top of the document is treated as a file header and not
// attached.
func (t *text) headStart(off int) int {
	ls := t.lineStart(off)
	cur := ls
	for {
		li := t.lineIdx(cur)
		if li == 0 {
			break
		}
		prev := t.lines[li-1]
		if !t.isCommentLine(prev) {
			break
		}
		cur = prev
	}
	if cur == ls {
		return ls
	}
	li := t.lineIdx(cur)
	if li == 0 || t.isDocMarker(t.lines[li-1]) {
		// keep header: attach only the comment lines after the first one that
		// is separated by nothing... simplest safe rule: attach nothing.
		return ls
	}
	return cur
}
