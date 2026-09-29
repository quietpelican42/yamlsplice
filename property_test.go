package yamlsplice

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// genDoc writes a random YAML document in a random house style.
type gen struct {
	r        *rand.Rand
	indent   int
	seqFlush bool
	quote    int // 0 plain, 1 single, 2 double, 3 mixed
	comments bool
	blanks   bool
	sb       strings.Builder
	keyN     int
}

var words = []string{"alpha", "beta", "gamma", "delta", "name", "image", "port", "enabled", "yes", "no", "on", "off", "true", "null", "123", "x: y", "a #b", "héllo", "1.5", "~", "it's", "path/to/x", "0777", "", "hello world", "-dash", "@at", "k:v"}
var keys = []string{"name", "image", "ports", "env", "spec", "meta", "labels", "app", "tier", "version", "items", "data", "config", "type", "size", "count", "list", "opts", "id", "url"}

func (g *gen) scalar(inFlow bool) string {
	switch g.r.Intn(6) {
	case 0:
		return fmt.Sprint(g.r.Intn(10000))
	case 1:
		return []string{"true", "false", "null", "1.25"}[g.r.Intn(4)]
	}
	w := words[g.r.Intn(len(words))]
	q := g.quote
	if q == 3 {
		q = g.r.Intn(3)
	}
	if !plainSafe(w, inFlow) {
		if q == 0 || !canSingleQuote(w) {
			q = 2
		}
		if q == 0 {
			q = 1
		}
	}
	switch q {
	case 1:
		if canSingleQuote(w) {
			return singleQuote(w)
		}
		return doubleQuote(w)
	case 2:
		return doubleQuote(w)
	}
	return w
}

func (g *gen) maybeComment() string {
	if g.comments && g.r.Intn(4) == 0 {
		return strings.Repeat(" ", 1+g.r.Intn(3)) + "# c" + fmt.Sprint(g.r.Intn(100))
	}
	return ""
}

func (g *gen) headComment(ind int) {
	if g.comments && g.r.Intn(6) == 0 {
		fmt.Fprintf(&g.sb, "%s# note %d\n", strings.Repeat(" ", ind), g.r.Intn(100))
	}
}

func (g *gen) blank() {
	if g.blanks && g.r.Intn(5) == 0 {
		g.sb.WriteString("\n")
	}
}

func (g *gen) flowValue(depth int) string {
	if depth > 1 || g.r.Intn(2) == 0 {
		return g.scalar(true)
	}
	if g.r.Intn(2) == 0 {
		n := 1 + g.r.Intn(3)
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, g.flowValue(depth+1))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	n := 1 + g.r.Intn(3)
	var parts []string
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("%s%d: %s", keys[i], i, g.flowValue(depth+1)))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func (g *gen) mapping(ind int, depth int, n int) {
	pad := strings.Repeat(" ", ind)
	for i := 0; i < n; i++ {
		g.headComment(ind)
		k := fmt.Sprintf("%s%d", keys[g.r.Intn(len(keys))], i)
		switch c := g.r.Intn(10); {
		case c < 5 || depth >= 3:
			fmt.Fprintf(&g.sb, "%s%s: %s%s\n", pad, k, g.scalar(false), g.maybeComment())
		case c == 5:
			fmt.Fprintf(&g.sb, "%s%s: %s%s\n", pad, k, g.flowValue(0), g.maybeComment())
		case c == 6:
			fmt.Fprintf(&g.sb, "%s%s: |\n%s%sline one\n%s%sline two\n", pad, k, pad, strings.Repeat(" ", g.indent), pad, strings.Repeat(" ", g.indent))
		case c == 7:
			fmt.Fprintf(&g.sb, "%s%s:%s\n", pad, k, g.maybeComment())
			g.mapping(ind+g.indent, depth+1, 1+g.r.Intn(3))
		default:
			fmt.Fprintf(&g.sb, "%s%s:%s\n", pad, k, g.maybeComment())
			sp := ind + g.indent
			if g.seqFlush {
				sp = ind
			}
			g.sequence(sp, depth+1, 1+g.r.Intn(3))
		}
		g.blank()
	}
}

func (g *gen) sequence(ind int, depth int, n int) {
	pad := strings.Repeat(" ", ind)
	for i := 0; i < n; i++ {
		g.headComment(ind)
		switch c := g.r.Intn(8); {
		case c < 4 || depth >= 3:
			fmt.Fprintf(&g.sb, "%s- %s%s\n", pad, g.scalar(false), g.maybeComment())
		case c < 6:
			// mapping item: first key on the dash line
			var sub gen = *g
			sub.sb.Reset()
			sub.mapping(ind+2, depth+1, 1+g.r.Intn(3))
			text := sub.sb.String()
			g.r = sub.r
			body := strings.TrimLeft(text, " ")
			g.sb.WriteString(pad + "- " + body)
		case c == 6:
			fmt.Fprintf(&g.sb, "%s- %s\n", pad, g.flowValue(0))
		default:
			fmt.Fprintf(&g.sb, "%s-%s\n", pad, g.maybeComment())
			g.mapping(ind+g.indent, depth+1, 1+g.r.Intn(2))
		}
		g.blank()
	}
}

func genDocument(r *rand.Rand) string {
	g := &gen{r: r, indent: 2 + r.Intn(3), seqFlush: r.Intn(3) == 0, quote: r.Intn(4), comments: r.Intn(2) == 0, blanks: r.Intn(2) == 0}
	docs := 1
	if r.Intn(6) == 0 {
		docs = 2
	}
	for d := 0; d < docs; d++ {
		if d > 0 {
			g.sb.WriteString("---\n")
		} else if g.comments && r.Intn(3) == 0 {
			g.sb.WriteString("# file header\n")
		}
		if r.Intn(8) == 0 {
			g.sequence(0, 0, 2+r.Intn(3))
		} else {
			g.mapping(0, 0, 2+r.Intn(4))
		}
	}
	s := g.sb.String()
	if r.Intn(8) == 0 {
		s = strings.TrimSuffix(s, "\n")
	}
	return s
}

var newValues = []any{"fresh", "yes", "off", "12", "a: b", "multi word", "x #y", 42, true, nil, 2.5, "héllo", "quo\"te", ""}

func TestPropertyRandomEdits(t *testing.T) {
	iters := 12000
	if testing.Short() {
		iters = 400
	}
	r := rand.New(rand.NewSource(20260929))
	stats := map[string]int{}
	for it := 0; it < iters; it++ {
		src := genDocument(r)
		nodes, err := collectNodes([]byte(src))
		if err != nil {
			t.Fatalf("generator produced invalid YAML: %v\n%s", err, src)
		}
		if len(nodes) == 0 {
			continue
		}
		before, err := decodeAny([]byte(src))
		if err != nil {
			continue // duplicate keys etc.
		}
		tn := nodes[r.Intn(len(nodes))]
		val := newValues[r.Intn(len(newValues))]
		var kind, key string
		var path []string = tn.path
		var call func(d *Doc) error
		ptrs := ptr(tn.path)
		switch op := r.Intn(5); {
		case op == 0 && tn.n.Kind == yaml.ScalarNode && len(tn.path) > 0:
			kind = "set"
			call = func(d *Doc) error { return d.Set(ptrs, val) }
		case op == 1 && tn.key != nil && !tn.isIndex[len(tn.isIndex)-1]:
			kind, key = "rename", fmt.Sprintf("renamed%d", r.Intn(1000))
			call = func(d *Doc) error { return d.Rename(ptrs, key) }
		case op == 2 && len(tn.path) > 0 && tn.parent != nil && len(tn.parent.Content) > 2:
			kind = "delete"
			call = func(d *Doc) error { return d.Delete(ptrs) }
		case op == 3 && tn.n.Kind == yaml.MappingNode:
			kind, key = "insert", fmt.Sprintf("added%d", r.Intn(1000))
			var pos []Position
			lastInsertDefault = true
			if len(tn.n.Content) > 0 {
				k := tn.n.Content[2*r.Intn(len(tn.n.Content)/2)].Value
				switch r.Intn(4) {
				case 0:
					pos = []Position{After(k)}
					lastInsertDefault = k == tn.n.Content[len(tn.n.Content)-2].Value
				case 1:
					pos = []Position{Before(k)}
					lastInsertDefault = false
				case 2:
					pos = []Position{First()}
					lastInsertDefault = false
				}
			}
			call = func(d *Doc) error { return d.Insert(ptrs, key, val, pos...) }
		case op == 4 && tn.n.Kind == yaml.SequenceNode:
			kind = "append"
			call = func(d *Doc) error { return d.Append(ptrs, val) }
		default:
			continue
		}
		d, err := Edit([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		dd, _ := d.Document(tn.doc)
		callErr := call(dd)
		if callErr != nil {
			// Refusals are acceptable only for constructs the package documents.
			// A block scalar that ends a file with no final newline gains a
			// trailing newline when a line follows it; that is refused.
			eofBlock := errors.Is(callErr, ErrVerify) && !strings.HasSuffix(src, "\n")
			if !strings.Contains(callErr.Error(), "unsupported") && !strings.Contains(callErr.Error(), "empty") && !eofBlock {
				t.Fatalf("%s %s: %v\n--- src ---\n%s", kind, ptrs, callErr, src)
			}
			if string(d.Bytes()) != src {
				t.Fatalf("bytes changed on error")
			}
			stats["refused"]++
			continue
		}
		stats[kind]++
		out := string(d.Bytes())
		// 1. data equals the expected data, computed independently.
		want := before
		if e := expectedAny(want, tn.doc, kind, path, key, val); e != nil {
			continue
		}
		got, err := decodeAny([]byte(out))
		if err != nil {
			t.Fatalf("%s %s: result does not parse: %v\n--- src ---\n%s\n--- out ---\n%s", kind, ptrs, err, src, out)
		}
		if !sameAny(want, got) {
			t.Fatalf("%s %s: data mismatch\n--- src ---\n%s\n--- out ---\n%s\nwant %v\ngot  %v", kind, ptrs, src, out, want, got)
		}
		// 2. bytes outside the edited range are unchanged.
		checkUntouched(t, kind, ptrs, src, out, tn, key)
	}
	t.Logf("edits applied: %v", stats)
}

// removedRanges returns every a such that ol is sl with lines [a,a+k) removed.
func removedRanges(sl, ol []string) []int {
	k := len(sl) - len(ol)
	if k < 0 {
		return nil
	}
	var out []int
	for a := 0; a+k <= len(sl); a++ {
		if reflectEqualLines(sl[:a], ol[:a]) && reflectEqualLines(sl[a+k:], ol[a:]) {
			out = append(out, a)
		}
	}
	return out
}

type fataler interface {
	Helper()
	Fatalf(string, ...any)
}

func checkUntouched(t fataler, kind, ptrs, src, out string, tn tnode, key string) {
	t.Helper()
	fail := func(f string, a ...any) {
		t.Fatalf("%s %s: %s\n--- src ---\n%s\n--- out ---\n%s", kind, ptrs, fmt.Sprintf(f, a...), src, out)
	}
	sl, ol := splitLines(src), splitLines(out)
	switch kind {
	case "set", "rename":
		target := tn.n
		if kind == "rename" {
			target = tn.key
		}
		if target.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || target.Kind != yaml.ScalarNode {
			return // covered by the unit tests
		}
		li := target.Line - 1
		if len(ol) != len(sl) {
			fail("line count changed %d -> %d", len(sl), len(ol))
		}
		for i := range sl {
			if i != li && sl[i] != ol[i] {
				fail("line %d changed but only line %d holds the target", i+1, li+1)
			}
		}
		// bytes before the node are untouched
		pre := runePrefix(sl[li], target.Column-1)
		if !strings.HasPrefix(ol[li], pre) {
			fail("bytes before the target changed: %q -> %q", sl[li], ol[li])
		}
		// a trailing comment survives
		if i := strings.Index(sl[li][len(pre):], " #"); i >= 0 && target.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) == 0 && !strings.ContainsAny(target.Value, "#") {
			tail := sl[li][len(pre)+i:]
			if !strings.HasSuffix(ol[li], tail) {
				fail("trailing comment changed: %q -> %q", sl[li], ol[li])
			}
		}
	case "delete":
		as := removedRanges(sl, ol)
		if len(as) == 0 {
			// first key on a "- " line is removed inside the line
			if rem, add := lineDiffCounts(sl, ol); rem > 0 && add > 0 {
				return
			}
			fail("result is not the source with whole lines removed")
		}
		k := len(sl) - len(ol)
		first := tn.n.Line
		if tn.key != nil {
			first = tn.key.Line
		}
		if tn.key == nil {
			// an item whose value starts on a later line than its lone "-"
			j := first - 2
			for j >= 0 && strings.HasPrefix(strings.TrimSpace(sl[j]), "#") {
				j--
			}
			if j >= 0 {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(sl[j]), "-"); ok && (strings.TrimSpace(rest) == "" || strings.HasPrefix(strings.TrimSpace(rest), "#")) {
					first = j + 1
				}
			}
		}
		last := lastLine(tn.n)
		var problem string
		for _, a := range as {
			problem = ""
			for i := a; i < a+k && problem == ""; i++ {
				trim := strings.TrimSpace(sl[i])
				inSubtree := i+1 >= first && i+1 <= last
				switch {
				case trim == "" && !inSubtree:
					problem = fmt.Sprintf("blank line %d removed", i+1)
				case !inSubtree && !strings.HasPrefix(trim, "#"):
					problem = fmt.Sprintf("removed line %d (%q) is outside the deleted entry", i+1, sl[i])
				}
			}
			if problem == "" && !(a+1 <= first && first <= a+k) {
				problem = fmt.Sprintf("the entry's own line %d was not removed", first)
			}
			if problem == "" {
				return
			}
		}
		fail("%s", problem)
	case "insert", "append":
		if tn.n.Style&yaml.FlowStyle != 0 {
			return
		}
		as := removedRanges(ol, sl) // ol is sl with the inserted lines added
		if len(as) == 0 {
			// a key inserted before the first key of "- k: v" moves that key down
			if rem, add := lineDiffCounts(sl, ol); kind == "insert" && !lastInsertDefault && rem == 1 && add == 2 {
				return
			}
			fail("result is not the source with whole lines inserted")
		}
		if kind == "append" || insertedAtEnd(tn, ptrs) {
			want := lastLine(tn.n)
			for _, a := range as {
				if a == want {
					return
				}
			}
			fail("inserted at line %v, expected after the last sibling at line %d", as, want)
		}
	}
}

// insertedAtEnd is set by the caller through the position it chose.
func insertedAtEnd(tn tnode, ptrs string) bool { return lastInsertDefault }

var lastInsertDefault bool

func runePrefix(s string, n int) string {
	i := 0
	for ; n > 0 && i < len(s); n-- {
		_, w := utf8DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i]
}

func reflectEqualLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ = bytes.Equal

func utf8DecodeRuneInString(s string) (rune, int) { return utf8.DecodeRuneInString(s) }

type recorder struct{ msg string }

func (r *recorder) Helper() {}
func (r *recorder) Fatalf(f string, a ...any) {
	r.msg = fmt.Sprintf(f, a...)
	panic(r)
}

// TestPropertyNegativeControl shows the byte-preservation check is not vacuous:
// a yaml.v3 round trip keeps the data but is caught.
func TestPropertyNegativeControl(t *testing.T) {
	src := "a: 1\n\n# keep me\nb:   [1,2]\nc:\n    - x\n"
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(src), &n); err != nil {
		t.Fatal(err)
	}
	n.Content[0].Content[1].Value = "9"
	out, err := yaml.Marshal(&n)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ := collectNodes([]byte(src))
	var tn tnode
	for _, x := range nodes {
		if len(x.path) == 1 && x.path[0] == "a" {
			tn = x
		}
	}
	rec := &recorder{}
	func() {
		defer func() {
			if recover() == nil {
				t.Errorf("round trip was not flagged:\n%s", out)
			}
		}()
		checkUntouched(rec, "set", "/a", src, string(out), tn, "")
	}()
	if rec.msg == "" {
		t.Fatal("no message recorded")
	}
	t.Logf("flagged as expected: %s", rec.msg)
}
