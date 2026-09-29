package yamlsplice

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// hook describes a pending edit in terms of the parsed tree so that the
// expected structure of the result can be computed without touching bytes.
type hook struct {
	kind      string // set, rename, delete, insert, append
	doc       int
	target    *yaml.Node // set: value node; rename: key node; delete: key node (pair) or item value node
	container *yaml.Node // insert, append
	newNode   *yaml.Node // set, insert, append: the value
	newKey    string     // rename, insert
	pos       int        // insert: index among the pairs
}

// sig writes a structural signature of y, applying h when it is non-nil.
func sig(y *yaml.Node, h *hook, sb *strings.Builder) {
	if h != nil && h.kind == "set" && y == h.target {
		// The splice keeps an existing anchor on the replaced node.
		if y.Anchor != "" {
			sb.WriteString("&" + y.Anchor + " ")
		}
		nn := *h.newNode
		nn.Anchor = ""
		sig(&nn, nil, sb)
		return
	}
	if y.Anchor != "" {
		sb.WriteString("&" + y.Anchor + " ")
	}
	switch y.Kind {
	case yaml.ScalarNode:
		tag := y.ShortTag()
		val := y.Value
		if tag == "!!null" {
			val = "null"
		}
		sb.WriteString("s:" + tag + ":" + strconv.Quote(val))
	case yaml.AliasNode:
		sb.WriteString("*" + y.Value)
	case yaml.DocumentNode:
		for _, c := range y.Content {
			sig(c, h, sb)
		}
	case yaml.MappingNode:
		sb.WriteString("{")
		n := len(y.Content) / 2
		emit := func(k, v *yaml.Node, key string, val *yaml.Node) {
			if k != nil {
				if h != nil && h.kind == "rename" && k == h.target {
					sb.WriteString("s:!!str:" + strconv.Quote(h.newKey))
				} else {
					sig(k, h, sb)
				}
			} else {
				sb.WriteString("s:!!str:" + strconv.Quote(key))
			}
			sb.WriteString("=")
			if v != nil {
				sig(v, h, sb)
			} else {
				sig(val, nil, sb)
			}
			sb.WriteString(";")
		}
		for i := 0; i <= n; i++ {
			if h != nil && h.kind == "insert" && y == h.container && i == h.pos {
				emit(nil, nil, h.newKey, h.newNode)
			}
			if i == n {
				break
			}
			k, v := y.Content[2*i], y.Content[2*i+1]
			if h != nil && h.kind == "delete" && k == h.target {
				continue
			}
			emit(k, v, "", nil)
		}
		sb.WriteString("}")
	case yaml.SequenceNode:
		sb.WriteString("[")
		for _, c := range y.Content {
			if h != nil && h.kind == "delete" && c == h.target {
				continue
			}
			sig(c, h, sb)
			sb.WriteString(";")
		}
		if h != nil && h.kind == "append" && y == h.container {
			sig(h.newNode, nil, sb)
			sb.WriteString(";")
		}
		sb.WriteString("]")
	}
}

func docSigs(ix *index, h *hook) []string {
	out := make([]string, len(ix.docs))
	for i, d := range ix.docs {
		var sb strings.Builder
		var hh *hook
		if h != nil && h.doc == i {
			hh = h
		}
		sig(d.y, hh, &sb)
		out[i] = sb.String()
	}
	return out
}
