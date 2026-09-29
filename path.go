package yamlsplice

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// seg is one parsed path segment.
type seg struct {
	key     string
	index   int
	isIndex bool // written as [n]: must address a sequence
	numeric bool // a bare number: an index on sequences, a key on mappings
}

func (s seg) String() string {
	if s.isIndex {
		return "[" + strconv.Itoa(s.index) + "]"
	}
	return strconv.Quote(s.key)
}

// parsePath parses the dotted path syntax or a JSON Pointer.
func parsePath(p string) ([]seg, error) {
	if p == "" || p == "$" || p == "." {
		return nil, nil
	}
	if strings.HasPrefix(p, "/") {
		return parsePointer(p)
	}
	p = strings.TrimPrefix(p, "$")
	var segs []seg
	i := 0
	expectKey := true
	for i < len(p) {
		c := p[i]
		switch {
		case c == '.':
			if i == 0 && len(segs) == 0 {
				i++
				continue
			}
			i++
			expectKey = true
			if i >= len(p) {
				return nil, fmt.Errorf("%w: %q ends with a dot", ErrPath, p)
			}
		case c == '[':
			j := i + 1
			if j < len(p) && (p[j] == '"' || p[j] == '\'') {
				k, n, err := readQuoted(p, j)
				if err != nil {
					return nil, err
				}
				j = n
				if j >= len(p) || p[j] != ']' {
					return nil, fmt.Errorf("%w: missing ] in %q", ErrPath, p)
				}
				segs = append(segs, seg{key: k})
				i = j + 1
			} else {
				e := strings.IndexByte(p[j:], ']')
				if e < 0 {
					return nil, fmt.Errorf("%w: missing ] in %q", ErrPath, p)
				}
				n, err := strconv.Atoi(strings.TrimSpace(p[j : j+e]))
				if err != nil {
					return nil, fmt.Errorf("%w: bad index %q in %q", ErrPath, p[j:j+e], p)
				}
				segs = append(segs, seg{index: n, isIndex: true})
				i = j + e + 1
			}
			expectKey = false
		case c == '"' || c == '\'':
			if !expectKey {
				return nil, fmt.Errorf("%w: unexpected quote in %q", ErrPath, p)
			}
			k, n, err := readQuoted(p, i)
			if err != nil {
				return nil, err
			}
			segs = append(segs, seg{key: k})
			i = n
			expectKey = false
		default:
			if !expectKey {
				return nil, fmt.Errorf("%w: unexpected %q in %q", ErrPath, string(c), p)
			}
			j := i
			for j < len(p) && p[j] != '.' && p[j] != '[' {
				j++
			}
			k := p[i:j]
			s := seg{key: k}
			if n, err := strconv.Atoi(k); err == nil && n >= 0 && strconv.Itoa(n) == k {
				s.numeric, s.index = true, n
			}
			segs = append(segs, s)
			i = j
			expectKey = false
		}
	}
	return segs, nil
}

func readQuoted(p string, i int) (string, int, error) {
	q := p[i]
	var b strings.Builder
	j := i + 1
	for j < len(p) {
		c := p[j]
		if c == '\\' && j+1 < len(p) {
			b.WriteByte(p[j+1])
			j += 2
			continue
		}
		if c == q {
			return b.String(), j + 1, nil
		}
		b.WriteByte(c)
		j++
	}
	return "", 0, fmt.Errorf("%w: unterminated quote in %q", ErrPath, p)
}

func parsePointer(p string) ([]seg, error) {
	parts := strings.Split(p[1:], "/")
	segs := make([]seg, 0, len(parts))
	for _, part := range parts {
		k := strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		s := seg{key: k}
		if n, err := strconv.Atoi(k); err == nil && n >= 0 && strconv.Itoa(n) == k {
			s.numeric, s.index = true, n
		}
		segs = append(segs, s)
	}
	return segs, nil
}

// ref is a resolved location.
type ref struct {
	n    *node
	pair *pair // set when n is a mapping value
	item *item // set when n is a sequence item
}

func describe(segs []seg) string {
	var b strings.Builder
	for i, s := range segs {
		if s.isIndex {
			b.WriteString(s.String())
		} else {
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s.key)
		}
	}
	return b.String()
}

func keyMatches(k *node, name string) bool {
	return k.y.Kind == yaml.ScalarNode && k.y.Value == name
}

// resolve walks segs from the root. It fails with ErrAlias when a segment
// would have to pass through an alias or a merge key.
func (di *docIndex) resolve(segs []seg) (ref, error) {
	if di.root == nil {
		if len(segs) == 0 {
			return ref{}, nil
		}
		return ref{}, fmt.Errorf("%w: document is empty", ErrNotFound)
	}
	cur := ref{n: di.root}
	for i, s := range segs {
		here := describe(segs[:i+1])
		n := cur.n
		if n.y.Kind == yaml.AliasNode {
			return ref{}, fmt.Errorf("%w: %s passes through alias *%s", ErrAlias, here, n.y.Value)
		}
		switch n.y.Kind {
		case yaml.MappingNode:
			if s.isIndex {
				return ref{}, fmt.Errorf("%w: %s: index applied to a mapping", ErrNotFound, here)
			}
			var found *pair
			hasMerge := false
			for _, p := range n.pairs {
				if keyMatches(p.key, s.key) {
					found = p
					break
				}
				if keyMatches(p.key, "<<") && p.key.y.Tag == "!!merge" {
					hasMerge = true
				}
			}
			if found == nil {
				if hasMerge {
					return ref{}, fmt.Errorf("%w: %s may come from a merge key (<<)", ErrAlias, here)
				}
				return ref{}, fmt.Errorf("%w: %s", ErrNotFound, here)
			}
			cur = ref{n: found.val, pair: found}
		case yaml.SequenceNode:
			if !s.isIndex && !s.numeric {
				return ref{}, fmt.Errorf("%w: %s: key applied to a sequence", ErrNotFound, here)
			}
			idx := s.index
			if idx < 0 {
				idx += len(n.items)
			}
			if idx < 0 || idx >= len(n.items) {
				return ref{}, fmt.Errorf("%w: %s: index out of range (length %d)", ErrNotFound, here, len(n.items))
			}
			it := n.items[idx]
			cur = ref{n: it.val, item: it}
		default:
			return ref{}, fmt.Errorf("%w: %s: parent is a scalar", ErrNotFound, here)
		}
	}
	return cur, nil
}
