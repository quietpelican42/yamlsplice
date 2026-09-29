package yamlsplice

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// tnode is a location in a yaml.v3 tree used by the tests to choose edits
// independently of the package's own index.
type tnode struct {
	path    []string // JSON Pointer segments (unescaped)
	isIndex []bool
	n       *yaml.Node
	key     *yaml.Node // set for mapping values
	parent  *yaml.Node
	doc     int
}

func ptr(segs []string) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString("/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(s))
	}
	return b.String()
}

// collectNodes lists every node reachable without passing through an alias.
func collectNodes(src []byte) ([]tnode, error) {
	dec := yaml.NewDecoder(bytes.NewReader(src))
	var out []tnode
	for doc := 0; ; doc++ {
		var y yaml.Node
		err := dec.Decode(&y)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if len(y.Content) == 0 {
			continue
		}
		var rec func(n *yaml.Node, path []string, idx []bool, key, parent *yaml.Node)
		rec = func(n *yaml.Node, path []string, idx []bool, key, parent *yaml.Node) {
			out = append(out, tnode{append([]string(nil), path...), append([]bool(nil), idx...), n, key, parent, doc})
			if n.Kind == yaml.AliasNode {
				return
			}
			switch n.Kind {
			case yaml.MappingNode:
				for i := 0; i+1 < len(n.Content); i += 2 {
					k := n.Content[i]
					if k.Kind != yaml.ScalarNode || k.Value == "<<" {
						continue
					}
					rec(n.Content[i+1], append(path, k.Value), append(idx, false), k, n)
				}
			case yaml.SequenceNode:
				for i, c := range n.Content {
					rec(c, append(path, fmt.Sprint(i)), append(idx, true), nil, n)
				}
			}
		}
		rec(y.Content[0], nil, nil, nil, nil)
	}
}

// decodeAny decodes every document into generic values.
func decodeAny(src []byte) ([]any, error) {
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

// normalize converts map[any]any to map[string]any so maps compare equal.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = normalize(e)
		}
		return x
	case map[any]any:
		m := map[string]any{}
		for k, e := range x {
			m[fmt.Sprint(k)] = normalize(e)
		}
		return m
	case []any:
		for i := range x {
			x[i] = normalize(x[i])
		}
		return x
	}
	return v
}

var errOracle = errors.New("oracle cannot follow path")

func walkAny(root any, path []string) (any, error) {
	cur := root
	for _, s := range path {
		switch c := cur.(type) {
		case map[string]any:
			v, ok := c[s]
			if !ok {
				return nil, errOracle
			}
			cur = v
		case []any:
			var i int
			if _, err := fmt.Sscan(s, &i); err != nil || i < 0 || i >= len(c) {
				return nil, errOracle
			}
			cur = c[i]
		default:
			return nil, errOracle
		}
	}
	return cur, nil
}

// expectedAny applies the edit to the generic data. kind is one of
// set, delete, rename, insert, append.
func expectedAny(docs []any, doc int, kind string, path []string, key string, val any) error {
	root := docs[doc]
	if kind == "set" && len(path) == 0 {
		docs[doc] = normalize(val)
		return nil
	}
	target := path
	if kind == "set" || kind == "delete" || kind == "rename" {
		target = path[:len(path)-1]
	}
	par, err := walkAny(root, target)
	if err != nil {
		return err
	}
	last := ""
	if len(path) > 0 {
		last = path[len(path)-1]
	}
	switch kind {
	case "set", "delete", "rename":
		switch p := par.(type) {
		case map[string]any:
			old, ok := p[last]
			if !ok {
				return errOracle
			}
			switch kind {
			case "set":
				p[last] = normalize(val)
			case "delete":
				delete(p, last)
			case "rename":
				delete(p, last)
				p[key] = old
			}
		case []any:
			var i int
			fmt.Sscan(last, &i)
			if kind == "set" {
				p[i] = normalize(val)
				return nil
			}
			if kind == "delete" {
				// rebuild parent slice in its own parent
				np := append(append([]any{}, p[:i]...), p[i+1:]...)
				if len(path) == 1 {
					docs[doc] = np
					return nil
				}
				gp, err := walkAny(root, path[:len(path)-2])
				if err != nil {
					return err
				}
				pk := path[len(path)-2]
				switch g := gp.(type) {
				case map[string]any:
					g[pk] = np
				case []any:
					var j int
					fmt.Sscan(pk, &j)
					g[j] = np
				default:
					docs[doc] = np
				}
				return nil
			}
			return errOracle
		default:
			return errOracle
		}
	case "insert":
		m, ok := par.(map[string]any)
		if !ok {
			return errOracle
		}
		m[key] = normalize(val)
	case "append":
		s, ok := par.([]any)
		if !ok {
			return errOracle
		}
		np := append(s, normalize(val))
		if len(path) == 0 {
			docs[doc] = np
			return nil
		}
		gp, err := walkAny(root, path[:len(path)-1])
		if err != nil {
			return err
		}
		pk := path[len(path)-1]
		switch g := gp.(type) {
		case map[string]any:
			g[pk] = np
		case []any:
			var j int
			fmt.Sscan(pk, &j)
			g[j] = np
		}
	}
	return nil
}

func sameAny(a, b []any) bool {
	for i := range a {
		a[i] = normalize(a[i])
	}
	for i := range b {
		b[i] = normalize(b[i])
	}
	return reflect.DeepEqual(a, b)
}

// lineDiffCounts returns how many lines were removed from a and added in b.
func lineDiffCounts(a, b []string) (removed, added int) {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	am, bm := a[p:len(a)-s], b[p:len(b)-s]
	n, m := len(am), len(bm)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case am[i] == bm[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	l := dp[0][0]
	return n - l, m - l
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	return strings.Split(s, "\n")
}

// lastLine returns the 1-based last line occupied by n, when computable from
// the parse: single-line scalars and literal block scalars only.
func lastLine(n *yaml.Node) int {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return n.Line + strings.Count(strings.TrimRight(n.Value, "\n"), "\n") + 1
		}
		return n.Line
	case yaml.AliasNode:
		return n.Line
	}
	m := n.Line
	for _, c := range n.Content {
		if l := lastLine(c); l > m {
			m = l
		}
	}
	return m
}
