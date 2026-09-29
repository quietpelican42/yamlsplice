package yamlsplice

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Raw is a YAML fragment inserted verbatim as a value. It must parse as a
// single YAML node. Use it for values that need exact control, such as a
// block scalar or a flow collection with specific spacing.
type Raw string

var (
	yaml11Bool = regexp.MustCompile(`^(?i:y|yes|n|no|on|off)$`)
	yaml11Int  = regexp.MustCompile(`^[-+]?(0[0-7_]+|0b[01_]+|0x[0-9a-fA-F_]+|[0-9][0-9_]*|[0-9][0-9_]*(:[0-5]?[0-9])+)$`)
	yaml11Flt  = regexp.MustCompile(`^[-+]?([0-9][0-9_]*)?\.[0-9_]*([eE][-+]?[0-9]+)?$|^[-+]?[0-9][0-9_]*(:[0-5]?[0-9])+\.[0-9_]*$|^[-+]?[0-9][0-9_]*[eE][-+]?[0-9]+$`)
)

// plainSafe reports whether s can be written as a plain scalar that every
// common parser reads back as the string s. It applies the YAML 1.2 plain
// scalar rules and additionally rejects the YAML 1.1 bool, int and float
// spellings (yes, no, on, off, 0777, 1_000, 1:30, ...).
func plainSafe(s string, flow bool) bool {
	if s == "" || !utf8.ValidString(s) {
		return false
	}
	if s[0] == ' ' || s[len(s)-1] == ' ' || s[0] == '\t' || s[len(s)-1] == '\t' {
		return false
	}
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == 0xFEFF || (unicode.IsControl(r)) || !unicode.IsPrint(r) && r != ' ' {
			return false
		}
	}
	switch s[0] {
	case '[', ']', '{', '}', ',', '#', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`':
		return false
	case '-', '?', ':':
		if len(s) == 1 || s[1] == ' ' {
			return false
		}
	}
	if strings.Contains(s, ": ") || strings.Contains(s, " #") || strings.HasSuffix(s, ":") {
		return false
	}
	if flow && strings.ContainsAny(s, ",[]{}") {
		return false
	}
	if s == "<<" || s == "=" || s == "~" || s == "---" || s == "..." || strings.HasPrefix(s, "--- ") || strings.HasPrefix(s, "... ") {
		return false
	}
	if yaml11Bool.MatchString(s) || yaml11Int.MatchString(s) || yaml11Flt.MatchString(s) {
		return false
	}
	// Ask the parser what it makes of it, in the three positions we write scalars.
	probes := []struct {
		doc string
		get func(root *yaml.Node) *yaml.Node
	}{
		{"k: " + s + "\n", func(r *yaml.Node) *yaml.Node {
			if r.Kind == yaml.MappingNode && len(r.Content) == 2 {
				return r.Content[1]
			}
			return nil
		}},
		{"- " + s + "\n", func(r *yaml.Node) *yaml.Node {
			if r.Kind == yaml.SequenceNode && len(r.Content) == 1 {
				return r.Content[0]
			}
			return nil
		}},
		{s + ": v\n", func(r *yaml.Node) *yaml.Node {
			if r.Kind == yaml.MappingNode && len(r.Content) == 2 {
				return r.Content[0]
			}
			return nil
		}},
	}
	for _, pr := range probes {
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(pr.doc), &n); err != nil || len(n.Content) == 0 {
			return false
		}
		got := pr.get(n.Content[0])
		if got == nil || got.Kind != yaml.ScalarNode || got.Value != s || got.ShortTag() != "!!str" || got.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0 || got.Anchor != "" {
			return false
		}
	}
	return true
}

func canSingleQuote(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || r == 0xFEFF || unicode.IsControl(r) || (!unicode.IsPrint(r) && r != ' ') {
			return false
		}
	}
	return true
}

func doubleQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case 0:
			b.WriteString(`\0`)
		default:
			switch {
			case r == utf8.RuneError:
				b.WriteString(`�`)
			case r < 0x20 || r == 0x7f:
				fmt.Fprintf(&b, `\x%02x`, r)
			case r == 0x85 || r == 0xa0 || r == 0x2028 || r == 0x2029 || r == 0xFEFF:
				fmt.Fprintf(&b, `\u%04x`, r)
			case !unicode.IsPrint(r):
				if r > 0xffff {
					fmt.Fprintf(&b, `\U%08x`, r)
				} else {
					fmt.Fprintf(&b, `\u%04x`, r)
				}
			default:
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func singleQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

type quoteStyle int

const (
	stylePlain quoteStyle = iota
	styleSingle
	styleDouble
)

// renderString renders s as a scalar. want is the preferred style; the result
// falls back to a quoted form only when the preferred style cannot represent s.
func renderString(s string, want quoteStyle, quoteFallback quoteStyle, flow bool) string {
	switch want {
	case stylePlain:
		if plainSafe(s, flow) {
			return s
		}
		want = quoteFallback
		if want == stylePlain {
			want = styleDouble
		}
		fallthrough
	case styleSingle:
		if want == styleSingle {
			if canSingleQuote(s) {
				return singleQuote(s)
			}
		}
	}
	return doubleQuote(s)
}

func styleOf(y *yaml.Node) quoteStyle {
	switch {
	case y.Style&yaml.DoubleQuotedStyle != 0:
		return styleDouble
	case y.Style&yaml.SingleQuotedStyle != 0:
		return styleSingle
	}
	return stylePlain
}

// scalarText renders a typed leaf. Strings follow the style hints; other
// kinds are written plain.
func scalarText(y *yaml.Node, want quoteStyle, fallback quoteStyle, flow bool) string {
	if y.ShortTag() == "!!str" {
		return renderString(y.Value, want, fallback, flow)
	}
	if y.ShortTag() == "!!null" {
		return "null"
	}
	return y.Value
}

// toNode converts a Go value into a yaml.Node describing it.
func toNode(v any) (*yaml.Node, error) {
	switch x := v.(type) {
	case *yaml.Node:
		return x, nil
	case Raw:
		var n yaml.Node
		if err := yaml.Unmarshal([]byte(x), &n); err != nil {
			return nil, fmt.Errorf("%w: raw value: %v", ErrValue, err)
		}
		if len(n.Content) != 1 {
			return nil, fmt.Errorf("%w: raw value must be a single YAML node", ErrValue)
		}
		return n.Content[0], nil
	case float32:
		return toNode(float64(x))
	case float64:
		if math.IsNaN(x) {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: ".nan"}, nil
		}
		if math.IsInf(x, 1) {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: ".inf"}, nil
		}
		if math.IsInf(x, -1) {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: "-.inf"}, nil
		}
		s := strconv.FormatFloat(x, 'g', -1, 64)
		if !strings.ContainsAny(s, ".e") {
			s += ".0"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: s}, nil
	}
	return reflectNode(reflect.ValueOf(v), v)
}

// reflectNode converts common Go values without going through the YAML
// encoder, which is lossy for some strings. Anything unusual (structs,
// timestamps) falls back to yaml.Node.Encode.
func reflectNode(rv reflect.Value, orig any) (*yaml.Node, error) {
	if !rv.IsValid() {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
		}
		return reflectNode(rv.Elem(), nil)
	case reflect.String:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: rv.String()}, nil
	case reflect.Bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(rv.Bool())}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(rv.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatUint(rv.Uint(), 10)}, nil
	case reflect.Float32, reflect.Float64:
		return toNode(rv.Float())
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
			break
		}
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for i := 0; i < rv.Len(); i++ {
			c, err := reflectNode(rv.Index(i), nil)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, c)
		}
		return n, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			break
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range keys {
			c, err := reflectNode(rv.MapIndex(k), nil)
			if err != nil {
				return nil, err
			}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k.String()}, c)
		}
		return n, nil
	}
	if orig == nil && rv.CanInterface() {
		orig = rv.Interface()
	}
	var n yaml.Node
	if err := n.Encode(orig); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValue, err)
	}
	return &n, nil
}

// layout carries the style decisions for rendering new content.
type layout struct {
	step     int        // indentation step
	seqFlush bool       // "- " at the same column as the parent key
	want     quoteStyle // preferred style for new string values
	keyWant  quoteStyle // preferred style for new keys
	quote    quoteStyle // quote style to use when the preferred one is unsafe
	flow     bool       // render collections in flow style
}

// rendered is the text of a value: Inline goes after "key: " or "- ", Lines are
// block lines (already indented, without trailing newline) that follow the key.
type rendered struct {
	inline string
	lines  []string
}

// renderValue renders y. base is the column at which the key (or dash) that
// owns the value sits. want is the style to keep when y is a string.
func (l layout) renderValue(y *yaml.Node, base int, want quoteStyle, inSeq bool) rendered {
	switch y.Kind {
	case yaml.ScalarNode:
		if y.ShortTag() == "!!str" && strings.Contains(y.Value, "\n") {
			return rendered{inline: doubleQuote(y.Value)}
		}
		return rendered{inline: scalarText(y, want, l.quote, false)}
	case yaml.AliasNode:
		return rendered{inline: "*" + y.Value}
	case yaml.DocumentNode:
		if len(y.Content) == 1 {
			return l.renderValue(y.Content[0], base, want, inSeq)
		}
	}
	if l.flow {
		return rendered{inline: l.flowText(y)}
	}
	if len(y.Content) == 0 {
		if y.Kind == yaml.MappingNode {
			return rendered{inline: "{}"}
		}
		return rendered{inline: "[]"}
	}
	var lines []string
	switch y.Kind {
	case yaml.MappingNode:
		col := base + l.step
		lines = l.blockMapLines(y, col)
	case yaml.SequenceNode:
		col := base + l.step
		if l.seqFlush {
			col = base
		}
		lines = l.blockSeqLines(y, col)
	}
	return rendered{lines: lines}
}

func (l layout) blockMapLines(y *yaml.Node, col int) []string {
	var out []string
	pad := strings.Repeat(" ", col)
	for i := 0; i+1 < len(y.Content); i += 2 {
		k, v := y.Content[i], y.Content[i+1]
		ktxt := renderKey(k.Value, l.keyWant, l.quote, false)
		r := l.renderValue(v, col, l.want, false)
		if r.lines == nil {
			out = append(out, pad+ktxt+": "+r.inline)
		} else {
			out = append(out, pad+ktxt+":")
			out = append(out, r.lines...)
		}
	}
	return out
}

func (l layout) blockSeqLines(y *yaml.Node, col int) []string {
	var out []string
	pad := strings.Repeat(" ", col)
	for _, v := range y.Content {
		if v.Kind == yaml.MappingNode && len(v.Content) > 0 && !l.flow {
			sub := l.blockMapLines(v, col+2)
			sub[0] = pad + "- " + strings.TrimLeft(sub[0], " ")
			out = append(out, sub...)
			continue
		}
		if v.Kind == yaml.SequenceNode && len(v.Content) > 0 && !l.flow {
			sub := l.blockSeqLines(v, col+2)
			sub[0] = pad + "- " + strings.TrimLeft(sub[0], " ")
			out = append(out, sub...)
			continue
		}
		r := l.renderValue(v, col, l.want, true)
		out = append(out, pad+"- "+r.inline)
	}
	return out
}

func (l layout) flowText(y *yaml.Node) string {
	switch y.Kind {
	case yaml.ScalarNode:
		return scalarText(y, l.want, l.quote, true)
	case yaml.AliasNode:
		return "*" + y.Value
	case yaml.MappingNode:
		parts := make([]string, 0, len(y.Content)/2)
		for i := 0; i+1 < len(y.Content); i += 2 {
			parts = append(parts, renderKey(y.Content[i].Value, l.keyWant, l.quote, true)+": "+l.flowText(y.Content[i+1]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case yaml.SequenceNode:
		parts := make([]string, 0, len(y.Content))
		for _, c := range y.Content {
			parts = append(parts, l.flowText(c))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return ""
}

// renderKey renders a mapping key. Keys stay strings.
func renderKey(k string, want quoteStyle, fallback quoteStyle, flow bool) string {
	return renderString(k, want, fallback, flow)
}
