package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// Seg is one path segment.
type Seg struct {
	Key   string
	Idx   int
	IsIdx bool
}

const (
	OpSet    = "set"
	OpRename = "rename"
	OpDelete = "delete"
	OpAdd    = "add"
	OpAppend = "append"
)

var ops = []string{OpSet, OpRename, OpDelete, OpAdd, OpAppend}

// Edit is a single edit request. Path addresses the target node; for add/append the
// path addresses the container (mapping/sequence).
type Edit struct {
	Op     string
	Path   []Seg
	Value  string // set: new value, add: new key's value, append: item
	NewKey string // rename: new key; add: new key
	// expectations for line diff
	Line    int // 1-based line of the target
	EndLine int // delete: last line of the removed subtree
}

var ErrUnsupported = errors.New("unsupported")

type Adapter struct {
	Name string
	Fn   func(src []byte, e Edit) ([]byte, error)
}

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

type cand struct {
	path []Seg
	e    Edit
}

func lines(src []byte) []string {
	s := strings.ReplaceAll(string(src), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

func isSkipNode(n *yaml.Node) bool {
	return n.Anchor != "" || n.Kind == yaml.AliasNode || n.Style&yaml.TaggedStyle != 0
}

func singleLineScalar(n *yaml.Node, ls []string) bool {
	if n.Kind != yaml.ScalarNode || n.Line < 1 || n.Line > len(ls) {
		return false
	}
	if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || strings.Contains(n.Value, "\n") || n.Value == "" {
		return false
	}
	l := ls[n.Line-1]
	switch {
	case n.Style&yaml.SingleQuotedStyle != 0:
		return strings.Contains(l, "'"+strings.ReplaceAll(n.Value, "'", "''")+"'")
	case n.Style&yaml.DoubleQuotedStyle != 0:
		if strings.ContainsAny(n.Value, "\\\"") || !isASCII(n.Value) {
			return false
		}
		return strings.Contains(l, `"`+n.Value+`"`)
	default:
		return strings.Contains(l, n.Value)
	}
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > 126 || r < 32 {
			return false
		}
	}
	return true
}

func maxLeafLine(n *yaml.Node, ls []string) (int, bool) {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" && n.Value == "" {
			return n.Line, true
		}
		if !singleLineScalar(n, ls) {
			return 0, false
		}
		return n.Line, true
	case yaml.MappingNode, yaml.SequenceNode:
		if n.Style&yaml.FlowStyle != 0 || isSkipNode(n) || len(n.Content) == 0 {
			return 0, false
		}
		m := 0
		for i, c := range n.Content {
			if isSkipNode(c) {
				return 0, false
			}
			if n.Kind == yaml.MappingNode && i%2 == 0 && c.Value == "<<" {
				return 0, false
			}
			l, ok := maxLeafLine(c, ls)
			if !ok {
				return 0, false
			}
			if l > m {
				m = l
			}
		}
		return m, true
	}
	return 0, false
}

func collect(n *yaml.Node, path []Seg, ls []string, out map[string][]cand) {
	if isSkipNode(n) {
		return
	}
	cp := func() []Seg { return append([]Seg(nil), path...) }
	switch n.Kind {
	case yaml.MappingNode:
		block := n.Style&yaml.FlowStyle == 0
		if block && len(n.Content) > 0 {
			out[OpAdd] = append(out[OpAdd], cand{cp(), Edit{Op: OpAdd, Path: cp(), NewKey: "added_key", Value: "added-value"}})
		}
		keys := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			keys[n.Content[i].Value] = true
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Value == "<<" || k.Kind != yaml.ScalarNode || k.Tag != "!!str" || !keyRe.MatchString(k.Value) || isSkipNode(k) {
				continue
			}
			kp := append(cp(), Seg{Key: k.Value})
			kOnLine := k.Line >= 1 && k.Line <= len(ls) && strings.Contains(ls[k.Line-1], k.Value)
			if kOnLine && !keys["renamed_key"] {
				out[OpRename] = append(out[OpRename], cand{kp, Edit{Op: OpRename, Path: kp, NewKey: "renamed_key", Line: k.Line}})
			}
			if kOnLine && v.Line == k.Line && singleLineScalar(v, ls) && !isSkipNode(v) {
				out[OpSet] = append(out[OpSet], cand{kp, Edit{Op: OpSet, Path: kp, Value: "edited-value", Line: v.Line}})
			}
			if kOnLine && block && len(n.Content) >= 4 {
				startsLine := strings.TrimSpace(ls[k.Line-1][:min(len(ls[k.Line-1]), k.Column-1)]) == ""
				if startsLine {
					if end, ok := maxLeafLine(v, ls); ok && end >= k.Line && end-k.Line < 60 {
						out[OpDelete] = append(out[OpDelete], cand{kp, Edit{Op: OpDelete, Path: kp, Line: k.Line, EndLine: end}})
					}
				}
			}
			collect(v, kp, ls, out)
		}
	case yaml.SequenceNode:
		block := n.Style&yaml.FlowStyle == 0
		if block && len(n.Content) > 0 {
			out[OpAppend] = append(out[OpAppend], cand{cp(), Edit{Op: OpAppend, Path: cp(), Value: "appended-item"}})
		}
		for i, c := range n.Content {
			collect(c, append(cp(), Seg{Idx: i, IsIdx: true}), ls, out)
		}
	}
}

func pickEdits(name string, src []byte, seeds int) ([]Edit, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("empty")
	}
	out := map[string][]cand{}
	collect(doc.Content[0], nil, lines(src), out)
	var edits []Edit
	for _, op := range ops {
		cs := out[op]
		if len(cs) == 0 {
			continue
		}
		for s := 0; s < seeds; s++ {
			h := fnv.New64a()
			h.Write([]byte(fmt.Sprintf("%s%s%d", name, op, s)))
			r := rand.New(rand.NewSource(int64(h.Sum64())))
			edits = append(edits, cs[r.Intn(len(cs))].e)
		}
	}
	return edits, nil
}

// ---- expected data ----

func decodeAll(src []byte) ([]any, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var docs []any
	for {
		var v any
		err := dec.Decode(&v)
		if err == io.EOF {
			return docs, nil
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, v)
	}
}

func getKey(m any, k string) (any, bool) {
	switch mm := m.(type) {
	case map[string]any:
		v, ok := mm[k]
		return v, ok
	case map[any]any:
		v, ok := mm[k]
		return v, ok
	}
	return nil, false
}
func setKey(m any, k string, v any) {
	switch mm := m.(type) {
	case map[string]any:
		mm[k] = v
	case map[any]any:
		mm[k] = v
	}
}
func delKey(m any, k string) {
	switch mm := m.(type) {
	case map[string]any:
		delete(mm, k)
	case map[any]any:
		delete(mm, k)
	}
}

func applyExpected(doc any, e Edit) (any, error) {
	// navigate to parent
	path := e.Path
	nav := func(root any, p []Seg) (any, error) {
		cur := root
		for _, s := range p {
			if s.IsIdx {
				sl, ok := cur.([]any)
				if !ok || s.Idx >= len(sl) {
					return nil, errors.New("bad idx")
				}
				cur = sl[s.Idx]
			} else {
				v, ok := getKey(cur, s.Key)
				if !ok {
					return nil, errors.New("bad key " + s.Key)
				}
				cur = v
			}
		}
		return cur, nil
	}
	switch e.Op {
	case OpAdd:
		t, err := nav(doc, path)
		if err != nil {
			return nil, err
		}
		setKey(t, e.NewKey, e.Value)
	case OpAppend:
		if len(path) == 0 {
			sl := doc.([]any)
			return append(sl, e.Value), nil
		}
		par, err := nav(doc, path[:len(path)-1])
		if err != nil {
			return nil, err
		}
		last := path[len(path)-1]
		if last.IsIdx {
			sl := par.([]any)
			sl[last.Idx] = append(sl[last.Idx].([]any), e.Value)
		} else {
			v, _ := getKey(par, last.Key)
			setKey(par, last.Key, append(v.([]any), e.Value))
		}
	default:
		par, err := nav(doc, path[:len(path)-1])
		if err != nil {
			return nil, err
		}
		k := path[len(path)-1].Key
		switch e.Op {
		case OpSet:
			setKey(par, k, e.Value)
		case OpDelete:
			delKey(par, k)
		case OpRename:
			// rebuild not needed for maps
			v, _ := getKey(par, k)
			delKey(par, k)
			setKey(par, e.NewKey, v)
		}
	}
	return doc, nil
}

// ---- diff ----

func lineDiff(a, b []string) (removed, added []int, tooBig bool) {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	am, bm := a[p:len(a)-s], b[p:len(b)-s]
	if len(am)*len(bm) > 30_000_000 {
		return nil, nil, true
	}
	n, m := len(am), len(bm)
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if am[i] == bm[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		if am[i] == bm[j] {
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			removed = append(removed, p+i)
			i++
		} else {
			added = append(added, p+j)
			j++
		}
	}
	for ; i < n; i++ {
		removed = append(removed, p+i)
	}
	for ; j < m; j++ {
		added = append(added, p+j)
	}
	return
}

func isCommentOrBlank(l string) (comment, blank bool) {
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "#"), t == ""
}

// judge returns "" when clean, else a reason.
var lenientBlank bool

func judge(src, res []byte, e Edit) string {
	want, err := decodeAll(src)
	if err != nil {
		return "corpus-parse"
	}
	want[0], err = applyExpected(want[0], e)
	if err != nil {
		return "expected-build: " + err.Error()
	}
	got, err := decodeAll(res)
	if err != nil {
		return "output-unparseable"
	}
	if !reflect.DeepEqual(want, got) {
		return "data-mismatch"
	}
	a, b := lines(src), lines(res)
	rem, add, big := lineDiff(a, b)
	if big {
		return "churn"
	}
	switch e.Op {
	case OpSet, OpRename:
		if len(rem) != 1 || len(add) != 1 || rem[0] != e.Line-1 {
			return fmt.Sprintf("diff-%d-%d", len(rem), len(add))
		}
	case OpAdd, OpAppend:
		if len(rem) != 0 || len(add) != 1 {
			return fmt.Sprintf("diff-%d-%d", len(rem), len(add))
		}
	case OpDelete:
		if len(add) != 0 {
			return fmt.Sprintf("diff-%d-%d", len(rem), len(add))
		}
		rs := map[int]bool{}
		for _, r := range rem {
			rs[r] = true
			if r+1 < e.Line || r+1 > e.EndLine {
				c, bl := isCommentOrBlank(a[r])
				if !c && !(lenientBlank && bl) {
					return fmt.Sprintf("diff-removed-outside")
				}
			}
		}
		for l := e.Line; l <= e.EndLine; l++ {
			c, bl := isCommentOrBlank(a[l-1])
			if !c && !bl && !rs[l-1] {
				return "diff-partial-delete"
			}
		}
	}
	return ""
}

type tally struct{ clean, total, unsup int }

func main() {
	dir := flag.String("corpus", "../corpus", "corpus dir")
	only := flag.String("only", "", "comma-separated adapter names")
	verbose := flag.Bool("v", false, "print failures")
	maxDocs := flag.Int("maxdocs", 0, "skip files with more documents than this (0 = no limit)")
	flag.BoolVar(&lenientBlank, "lenient-blank", false, "delete may also remove blank lines outside the entry")
	seeds := flag.Int("seeds", 1, "edits per operation per file")
	flag.Parse()
	files, _ := filepath.Glob(filepath.Join(*dir, "*"))
	sort.Strings(files)
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "SKIP: no corpus found. Run bench/fetch-corpus.sh first (downloads pinned files from GitHub).")
		os.Exit(2)
	}
	adapters := allAdapters()
	if *only != "" {
		var f []Adapter
		for _, a := range adapters {
			for _, n := range strings.Split(*only, ",") {
				if a.Name == n {
					f = append(f, a)
				}
			}
		}
		adapters = f
	}
	res := map[string]map[string]*tally{}
	reasons := map[string]map[string]int{}
	usable := 0
	kinds := map[string]int{}
	for _, f := range files {
		src, _ := os.ReadFile(f)
		name := filepath.Base(f)
		if docs, derr := decodeAll(src); *maxDocs > 0 && derr == nil && len(docs) > *maxDocs {
			continue
		}
		edits, err := pickEdits(name, src, *seeds)
		if err != nil || len(edits) == 0 {
			fmt.Fprintf(os.Stderr, "skip %s: %v\n", name, err)
			continue
		}
		usable++
		kinds[strings.SplitN(name, "__", 2)[0]]++
		for _, e := range edits {
			for _, a := range adapters {
				if res[a.Name] == nil {
					res[a.Name] = map[string]*tally{}
					reasons[a.Name] = map[string]int{}
				}
				t := res[a.Name][e.Op]
				if t == nil {
					t = &tally{}
					res[a.Name][e.Op] = t
				}
				out, err := safe(a.Fn, src, e)
				if errors.Is(err, ErrUnsupported) {
					t.unsup++
					continue
				}
				t.total++
				why := ""
				if err != nil {
					why = "error"
					if *verbose {
						fmt.Printf("ERR  %-14s %-7s %s %v: %v\n", a.Name, e.Op, name, e.Path, trunc(err.Error()))
					}
				} else {
					why = judge(src, out, e)
				}
				if why == "" {
					t.clean++
				} else {
					reasons[a.Name][e.Op+":"+regexp.MustCompile(`diff-\d+-\d+`).ReplaceAllString(why, "diff")]++
					if *verbose && why != "error" {
						fmt.Printf("FAIL %-14s %-7s %s %v: %s\n", a.Name, e.Op, name, e.Path, why)
					}
				}
			}
		}
	}
	fmt.Printf("usable files: %d  kinds: %v\n\n", usable, kinds)
	fmt.Printf("%-16s", "adapter")
	for _, op := range ops {
		fmt.Printf("%-12s", op)
	}
	fmt.Printf("%-14s%-14s\n", "set+del+add", "all")
	var names []string
	for _, a := range adapters {
		names = append(names, a.Name)
	}
	for _, n := range names {
		fmt.Printf("%-16s", n)
		c, t, c3, t3 := 0, 0, 0, 0
		for _, op := range ops {
			x := res[n][op]
			if x == nil {
				fmt.Printf("%-12s", "-")
				continue
			}
			if x.total == 0 {
				fmt.Printf("%-12s", "n/a")
				continue
			}
			fmt.Printf("%-12s", fmt.Sprintf("%d/%d", x.clean, x.total))
			c += x.clean
			t += x.total
			if op == OpSet || op == OpDelete || op == OpAdd {
				c3 += x.clean
				t3 += x.total
			}
		}
		fmt.Printf("%-14s%-14s\n", pct(c3, t3), pct(c, t))
	}
	exit := 0
	if x := res["yamlsplice"]; x != nil {
		for _, op := range ops {
			if t := x[op]; t != nil && t.clean != t.total {
				exit = 1
			}
		}
	}
	defer os.Exit(exit)
	fmt.Println("\nfailure reasons:")
	for _, n := range names {
		var ks []string
		for k := range reasons[n] {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		fmt.Printf("  %s:", n)
		for _, k := range ks {
			fmt.Printf(" %s=%d", k, reasons[n][k])
		}
		fmt.Println()
	}
}

func trunc(s string) string {
	if len(s) > 140 {
		return s[:140]
	}
	return s
}

func pct(a, b int) string {
	if b == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d=%.1f%%", a, b, 100*float64(a)/float64(b))
}

func safe(fn func([]byte, Edit) ([]byte, error), src []byte, e Edit) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return fn(src, e)
}
