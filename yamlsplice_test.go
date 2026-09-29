package yamlsplice

import (
	"errors"
	"testing"
)

type step struct {
	op   string // set, delete, rename, insert, append
	path string
	key  string // rename: new key; insert: key
	val  any
	pos  []Position
}

func apply(d *Doc, s step) error {
	switch s.op {
	case "set":
		return d.Set(s.path, s.val)
	case "delete":
		return d.Delete(s.path)
	case "rename":
		return d.Rename(s.path, s.key)
	case "insert":
		return d.Insert(s.path, s.key, s.val, s.pos...)
	case "append":
		return d.Append(s.path, s.val)
	}
	panic("bad op " + s.op)
}

func TestEdits(t *testing.T) {
	tests := []struct {
		name string
		in   string
		step step
		want string
	}{
		// --- Set: quoting and style
		{"set plain keeps comment", "a: old   # c\nb: 1\n", step{op: "set", path: "a", val: "new"}, "a: new   # c\nb: 1\n"},
		{"set single quote kept", "a: 'old'\n", step{op: "set", path: "a", val: "it's"}, "a: 'it''s'\n"},
		{"set double quote kept", "a: \"old\"\n", step{op: "set", path: "a", val: "new"}, "a: \"new\"\n"},
		{"set double with escapes", "a: \"old\"\n", step{op: "set", path: "a", val: "say \"x\"\n"}, "a: \"say \\\"x\\\"\\n\"\n"},
		{"set single falls back to double", "a: 'old'\n", step{op: "set", path: "a", val: "tab\there"}, "a: \"tab\\there\"\n"},
		{"set plain yes is quoted", "a: old\n", step{op: "set", path: "a", val: "yes"}, "a: \"yes\"\n"},
		{"set plain on is quoted", "a: old\n", step{op: "set", path: "a", val: "on"}, "a: \"on\"\n"},
		{"set plain true is quoted", "a: old\n", step{op: "set", path: "a", val: "true"}, "a: \"true\"\n"},
		{"set plain number-like is quoted", "a: old\n", step{op: "set", path: "a", val: "0777"}, "a: \"0777\"\n"},
		{"set plain null-like is quoted", "a: old\n", step{op: "set", path: "a", val: "~"}, "a: \"~\"\n"},
		{"set plain colon space is quoted", "a: old\n", step{op: "set", path: "a", val: "x: y"}, "a: \"x: y\"\n"},
		{"set plain hash is quoted", "a: old\n", step{op: "set", path: "a", val: "x #y"}, "a: \"x #y\"\n"},
		{"set plain leading dash is quoted", "a: old\n", step{op: "set", path: "a", val: "- x"}, "a: \"- x\"\n"},
		{"set plain url stays plain", "a: old\n", step{op: "set", path: "a", val: "http://x.io/a?b=1"}, "a: http://x.io/a?b=1\n"},
		{"set empty string", "a: old\n", step{op: "set", path: "a", val: ""}, "a: \"\"\n"},
		{"set date-like is quoted", "a: old\n", step{op: "set", path: "a", val: "2001-12-14"}, "a: \"2001-12-14\"\n"},
		{"set int over quoted string drops quotes", "a: '8080'\n", step{op: "set", path: "a", val: 9090}, "a: 9090\n"},
		{"set bool", "a: false\n", step{op: "set", path: "a", val: true}, "a: true\n"},
		{"set nil", "a: 1\n", step{op: "set", path: "a", val: nil}, "a: null\n"},
		{"set float", "a: 1\n", step{op: "set", path: "a", val: 2.0}, "a: 2.0\n"},
		{"set string over int quotes when numeric", "a: 1\n", step{op: "set", path: "a", val: "42"}, "a: \"42\"\n"},
		{"set keeps anchor", "a: &x 1\nb: *x\n", step{op: "set", path: "a", val: 2}, "a: &x 2\nb: *x\n"},
		{"set drops tag on type change", "a: !!str 1\n", step{op: "set", path: "a", val: 2}, "a: 2\n"},
		{"set alias value replaced", "a: &x 1\nb: *x\n", step{op: "set", path: "b", val: 5}, "a: &x 1\nb: 5\n"},
		{"set in flow map", "a: {x: 1, y: 2}\n", step{op: "set", path: "a.y", val: 5}, "a: {x: 1, y: 5}\n"},
		{"set in flow seq", "a: [1, 2, 3]\n", step{op: "set", path: "a[1]", val: "two"}, "a: [1, two, 3]\n"},
		{"set seq item", "a:\n  - 1\n  - 2 # two\n", step{op: "set", path: "a[1]", val: 3}, "a:\n  - 1\n  - 3 # two\n"},
		{"set empty value", "a:\nb: 1\n", step{op: "set", path: "a", val: "x"}, "a: x\nb: 1\n"},
		{"set multibyte neighbours", "héllo: wörld # ü\nz: 1\n", step{op: "set", path: "héllo", val: "x"}, "héllo: x # ü\nz: 1\n"},
		{"set block scalar stays block", "a: |\n  one\n  two\nb: 1\n", step{op: "set", path: "a", val: "x\ny\nz\n"}, "a: |\n  x\n  y\n  z\nb: 1\n"},
		{"set block scalar strip", "a: |-\n  one\nb: 1\n", step{op: "set", path: "a", val: "x\ny"}, "a: |-\n  x\n  y\nb: 1\n"},
		{"set block scalar to short", "a: |\n  one\nb: 1\n", step{op: "set", path: "a", val: "x"}, "a: x\nb: 1\n"},
		{"set multiline plain", "a: one\n  two\nb: 1\n", step{op: "set", path: "a", val: "x"}, "a: x\nb: 1\n"},
		{"set block map to scalar", "a:\n  x: 1\n  y: 2\nb: 1\n", step{op: "set", path: "a", val: 7}, "a: 7\nb: 1\n"},
		{"set block map to scalar keeps key comment", "a: # about a\n  x: 1\nb: 1\n", step{op: "set", path: "a", val: 7}, "a: 7 # about a\nb: 1\n"},
		{"set scalar to block map", "a: 1 # c\nb: 1\n", step{op: "set", path: "a", val: map[string]any{"x": 1, "yy": "z"}}, "a: # c\n  x: 1\n  yy: z\nb: 1\n"},
		{"set upsert appends key", "a: 1\n\nb: 2\n", step{op: "set", path: "c", val: 3}, "a: 1\n\nb: 2\nc: 3\n"},
		{"set upsert nested", "a:\n  x: 1\nb: 2\n", step{op: "set", path: "a.yy", val: 3}, "a:\n  x: 1\n  yy: 3\nb: 2\n"},
		{"set root scalar doc", "hello\n", step{op: "set", path: "", val: "bye"}, "bye\n"},
		{"set no trailing newline", "a: 1\nb: 2", step{op: "set", path: "b", val: 3}, "a: 1\nb: 3"},
		{"set crlf", "a: 1\r\nb: 2\r\n", step{op: "set", path: "b", val: 3}, "a: 1\r\nb: 3\r\n"},

		// --- Delete
		{"delete keeps blank lines", "a: 1\n\nb: 2\n\nc: 3\n", step{op: "delete", path: "b"}, "a: 1\n\n\nc: 3\n"},
		{"delete takes head comment", "a: 1\n# about b\n# more\nb: 2\nc: 3\n", step{op: "delete", path: "b"}, "a: 1\nc: 3\n"},
		{"delete keeps comment above a blank line", "a: 1\n# free comment\n\nb: 2\nc: 3\n", step{op: "delete", path: "b"}, "a: 1\n# free comment\n\nc: 3\n"},
		{"delete keeps file header", "# license\nname: x\nb: 2\n", step{op: "delete", path: "name"}, "# license\nb: 2\n"},
		{"delete trailing comment goes with line", "a: 1 # one\nb: 2 # two\n", step{op: "delete", path: "a"}, "b: 2 # two\n"},
		{"delete nested block", "a:\n  x: 1\n  y:\n    z: 2\nb: 3\n", step{op: "delete", path: "a.y"}, "a:\n  x: 1\nb: 3\n"},
		{"delete whole block value", "a: 1\nb:\n  - x\n  - y\nc: 3\n", step{op: "delete", path: "b"}, "a: 1\nc: 3\n"},
		{"delete deeper foot comment", "a:\n  x: 1\n  # trailing\nb: 3\n", step{op: "delete", path: "a"}, "b: 3\n"},
		{"delete block scalar", "a: |\n  t1\n  t2\nb: 1\n", step{op: "delete", path: "a"}, "b: 1\n"},
		{"delete last key at eof", "a: 1\nb: 2", step{op: "delete", path: "b"}, "a: 1\n"},
		{"delete seq item", "a:\n  - x\n  - y\n  - z\n", step{op: "delete", path: "a[1]"}, "a:\n  - x\n  - z\n"},
		{"delete seq item negative", "a:\n  - x\n  - y\n", step{op: "delete", path: "a[-1]"}, "a:\n  - x\n"},
		{"delete first key of seq item", "s:\n  - a: 1\n    b: 2\n  - a: 3\n", step{op: "delete", path: "s[0].a"}, "s:\n  - b: 2\n  - a: 3\n"},
		{"delete second key of seq item", "s:\n  - a: 1\n    b: 2\n", step{op: "delete", path: "s[0].b"}, "s:\n  - a: 1\n"},
		{"delete only key nested", "a:\n  x: 1\nb: 2\n", step{op: "delete", path: "a.x"}, "a: {}\nb: 2\n"},
		{"delete only key keeps comment", "a: # c\n  x: 1\nb: 2\n", step{op: "delete", path: "a.x"}, "a: {} # c\nb: 2\n"},
		{"delete only item", "a:\n  - x\nb: 2\n", step{op: "delete", path: "a[0]"}, "a: []\nb: 2\n"},
		{"delete flow first", "a: {x: 1, y: 2, z: 3}\n", step{op: "delete", path: "a.x"}, "a: {y: 2, z: 3}\n"},
		{"delete flow last", "a: {x: 1, y: 2, z: 3}\n", step{op: "delete", path: "a.z"}, "a: {x: 1, y: 2}\n"},
		{"delete flow seq item", "a: [1, 2, 3]\n", step{op: "delete", path: "a[1]"}, "a: [1, 3]\n"},
		{"delete flow only", "a: {x: 1}\nb: 1\n", step{op: "delete", path: "a.x"}, "a: {}\nb: 1\n"},

		// --- Rename
		{"rename plain", "a: 1 # c\nb: 2\n", step{op: "rename", path: "a", key: "z"}, "z: 1 # c\nb: 2\n"},
		{"rename quoted key stays quoted", "\"a\": 1\n", step{op: "rename", path: "a", key: "z"}, "\"z\": 1\n"},
		{"rename to yaml 1.1 bool", "a: 1\n", step{op: "rename", path: "a", key: "no"}, "\"no\": 1\n"},
		{"rename to key with colon", "a: 1\n", step{op: "rename", path: "a", key: "x: y"}, "\"x: y\": 1\n"},
		{"rename in flow", "a: {x: 1}\n", step{op: "rename", path: "a.x", key: "yy"}, "a: {yy: 1}\n"},
		{"rename in flow avoids comma", "a: {x: 1}\n", step{op: "rename", path: "a.x", key: "y,z"}, "a: {\"y,z\": 1}\n"},
		{"rename nested", "a:\n  b:\n    c: 1\n", step{op: "rename", path: "a.b.c", key: "d"}, "a:\n  b:\n    d: 1\n"},

		// --- Insert
		{"insert after last sibling keeps group", "a: 1\nb: 2\n\nc: 3\n", step{op: "insert", path: "", key: "nk", val: 9, pos: []Position{After("b")}}, "a: 1\nb: 2\nnk: 9\n\nc: 3\n"},
		{"insert at end", "a: 1\n\n# tail\nb: 2\n", step{op: "insert", path: "", key: "nk", val: 9}, "a: 1\n\n# tail\nb: 2\nnk: 9\n"},
		{"insert before keeps head comment with sibling", "a: 1\n# about b\nb: 2\n", step{op: "insert", path: "", key: "nk", val: 9, pos: []Position{Before("b")}}, "a: 1\nnk: 9\n# about b\nb: 2\n"},
		{"insert first", "# top\na: 1\n", step{op: "insert", path: "", key: "nk", val: 9, pos: []Position{First()}}, "# top\nnk: 9\na: 1\n"},
		{"insert nested indent 4", "a:\n    x: 1\n    y: 2\nb: 3\n", step{op: "insert", path: "a", key: "z", val: 3}, "a:\n    x: 1\n    y: 2\n    z: 3\nb: 3\n"},
		{"insert in seq item mapping", "s:\n  - a: 1\n    b: 2\n", step{op: "insert", path: "s[0]", key: "c", val: 3}, "s:\n  - a: 1\n    b: 2\n    c: 3\n"},
		{"insert before first key of seq item", "s:\n  - a: 1\n    b: 2\n", step{op: "insert", path: "s[0]", key: "c", val: 3, pos: []Position{First()}}, "s:\n  - c: 3\n    a: 1\n    b: 2\n"},
		{"insert matches sibling quotes", "a: \"x\"\nb: \"y\"\n", step{op: "insert", path: "", key: "c", val: "z"}, "a: \"x\"\nb: \"y\"\nc: \"z\"\n"},
		{"insert plain when siblings plain", "a: x\nb: y\n", step{op: "insert", path: "", key: "c", val: "z"}, "a: x\nb: y\nc: z\n"},
		{"insert quotes trap words", "a: x\nb: y\n", step{op: "insert", path: "", key: "c", val: "off"}, "a: x\nb: y\nc: \"off\"\n"},
		{"insert map value indented seq", "a:\n  - 1\nb: 2\n", step{op: "insert", path: "", key: "l", val: []any{"p", "q"}}, "a:\n  - 1\nb: 2\nl:\n  - p\n  - q\n"},
		{"insert map value flush seq", "a:\n- 1\nb: 2\n", step{op: "insert", path: "", key: "l", val: []any{"p", "q"}}, "a:\n- 1\nb: 2\nl:\n- p\n- q\n"},
		{"insert nested map value", "a: 1\n", step{op: "insert", path: "", key: "m", val: map[string]any{"x": 1, "yy": []any{1}}}, "a: 1\nm:\n  x: 1\n  yy:\n    - 1\n"},
		{"insert flow value when siblings flow", "a: [1]\nb: [2]\n", step{op: "insert", path: "", key: "c", val: []any{3, 4}}, "a: [1]\nb: [2]\nc: [3, 4]\n"},
		{"insert empty collections", "a: 1\n", step{op: "insert", path: "", key: "e", val: []any{}}, "a: 1\ne: []\n"},
		{"insert no trailing newline", "a: 1", step{op: "insert", path: "", key: "b", val: 2}, "a: 1\nb: 2"},
		{"insert into empty value", "a:\nb: 1\n", step{op: "insert", path: "a", key: "x", val: 1}, "a:\n  x: 1\nb: 1\n"},
		{"insert into flow map", "a: {x: 1}\n", step{op: "insert", path: "a", key: "yy", val: 2}, "a: {x: 1, yy: 2}\n"},
		{"insert into empty flow map", "a: {}\n", step{op: "insert", path: "a", key: "yy", val: 2}, "a: {yy: 2}\n"},
		{"insert after with trailing comment", "a: 1 # one\nb: 2\n", step{op: "insert", path: "", key: "nk", val: 9, pos: []Position{After("a")}}, "a: 1 # one\nnk: 9\nb: 2\n"},
		{"insert after block value", "a:\n  x: 1\n  y: 2\nb: 2\n", step{op: "insert", path: "", key: "nk", val: 9, pos: []Position{After("a")}}, "a:\n  x: 1\n  y: 2\nnk: 9\nb: 2\n"},

		// --- Append
		{"append indented seq", "a:\n  - x\n  - yy # why\nb: 1\n", step{op: "append", path: "a", val: "z"}, "a:\n  - x\n  - yy # why\n  - z\nb: 1\n"},
		{"append flush seq", "a:\n- x\n- y\nb: 1\n", step{op: "append", path: "a", val: "z"}, "a:\n- x\n- y\n- z\nb: 1\n"},
		{"append root seq", "- x\n- y\n", step{op: "append", path: "", val: "z"}, "- x\n- y\n- z\n"},
		{"append quoted siblings", "a:\n  - \"x\"\n  - \"y\"\n", step{op: "append", path: "a", val: "z"}, "a:\n  - \"x\"\n  - \"y\"\n  - \"z\"\n"},
		{"append map item", "s:\n  - name: a\n    v: 1\n", step{op: "append", path: "s", val: map[string]any{"name": "b", "v": 2}}, "s:\n  - name: a\n    v: 1\n  - name: b\n    v: 2\n"},
		{"append flow", "a: [1, 2]\n", step{op: "append", path: "a", val: 3}, "a: [1, 2, 3]\n"},
		{"append flow empty", "a: []\n", step{op: "append", path: "a", val: 3}, "a: [3]\n"},
		{"append flow multiline", "a: [\n  x,\n  y\n]\n", step{op: "append", path: "a", val: "z"}, "a: [\n  x,\n  y,\n  z\n]\n"},
		{"append flow multiline trailing comma", "a: [\n  x,\n  y,\n]\n", step{op: "append", path: "a", val: "z"}, "a: [\n  x,\n  y,\n  z,\n]\n"},
		{"append to empty value", "a:\nb: 1\n", step{op: "append", path: "a", val: "x"}, "a:\n  - x\nb: 1\n"},
		{"append no trailing newline", "a:\n  - x", step{op: "append", path: "a", val: "yy"}, "a:\n  - x\n  - yy"},
		{"append keeps blank line after", "a:\n  - x\n\nb: 1\n", step{op: "append", path: "a", val: "yy"}, "a:\n  - x\n  - yy\n\nb: 1\n"},
		{"append nested seq item mapping", "a:\n  - b:\n      - 1\n", step{op: "append", path: "a[0].b", val: 2}, "a:\n  - b:\n      - 1\n      - 2\n"},
		{"append crlf", "a:\r\n  - x\r\n", step{op: "append", path: "a", val: "yy"}, "a:\r\n  - x\r\n  - yy\r\n"},
		{"append string needing quotes", "a:\n  - x\n", step{op: "append", path: "a", val: "no"}, "a:\n  - x\n  - \"no\"\n"},

		// --- paths
		{"path quoted segment", "\"a.b\": 1\n", step{op: "set", path: "\"a.b\"", val: 2}, "\"a.b\": 2\n"},
		{"path bracket quoted", "a:\n  \"x y\": 1\n", step{op: "set", path: "a['x y']", val: 2}, "a:\n  \"x y\": 2\n"},
		{"path json pointer", "a:\n  b/c: 1\n  d:\n    - 5\n", step{op: "set", path: "/a/b~1c", val: 2}, "a:\n  b/c: 2\n  d:\n    - 5\n"},
		{"path json pointer index", "a:\n  d:\n    - 5\n    - 6\n", step{op: "set", path: "/a/d/1", val: 7}, "a:\n  d:\n    - 5\n    - 7\n"},
		{"path dotted numeric index", "a:\n  - 5\n  - 6\n", step{op: "set", path: "a.1", val: 7}, "a:\n  - 5\n  - 7\n"},
		{"path numeric key on mapping", "a:\n  \"1\": x\n", step{op: "set", path: "a.1", val: "yy"}, "a:\n  \"1\": yy\n"},
		{"path leading dollar", "a:\n  b: 1\n", step{op: "set", path: "$.a.b", val: 2}, "a:\n  b: 2\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Edit([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if err := apply(d, tc.step); err != nil {
				t.Fatalf("error: %v", err)
			}
			if got := string(d.Bytes()); got != tc.want {
				t.Fatalf("mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tc.want)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	base := "a: &x\n  b: 1\nc: *x\nd:\n  <<: *x\n  e: 2\nf: [1]\ng: 1\n"
	tests := []struct {
		name string
		in   string
		step step
		want error
	}{
		{"through alias set", base, step{op: "set", path: "c.b", val: 2}, ErrAlias},
		{"through alias delete", base, step{op: "delete", path: "c.b"}, ErrAlias},
		{"through alias append", "a: &x [1]\nb: *x\n", step{op: "append", path: "b", val: 2}, ErrAlias},
		{"through alias insert", base, step{op: "insert", path: "c", key: "k", val: 1}, ErrAlias},
		{"through merge key", base, step{op: "set", path: "d.b", val: 2}, ErrAlias},
		{"not found", base, step{op: "set", path: "zzz.y", val: 2}, ErrNotFound},
		{"delete not found", base, step{op: "delete", path: "nope"}, ErrNotFound},
		{"index out of range", base, step{op: "set", path: "f[3]", val: 2}, ErrNotFound},
		{"key on sequence", base, step{op: "set", path: "f.x", val: 2}, ErrNotFound},
		{"rename exists", "a: 1\nb: 2\n", step{op: "rename", path: "a", key: "b"}, ErrExists},
		{"insert exists", "a: 1\n", step{op: "insert", path: "", key: "a", val: 2}, ErrExists},
		{"insert sibling missing", "a: 1\n", step{op: "insert", path: "", key: "b", val: 2, pos: []Position{After("q")}}, ErrNotFound},
		{"bad path", "a: 1\n", step{op: "set", path: "a[", val: 1}, ErrPath},
		{"delete root", "a: 1\n", step{op: "delete", path: ""}, ErrPath},
		{"append to scalar", "a: 1\n", step{op: "append", path: "a", val: 1}, ErrNotFound},
		{"bad raw", "a: 1\n", step{op: "set", path: "a", val: Raw("[unclosed")}, ErrValue},
		{"explicit key delete", "? a\n: 1\nb: 2\n", step{op: "delete", path: "a"}, ErrUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Edit([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			err = apply(d, tc.step)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if string(d.Bytes()) != tc.in {
				t.Fatalf("document changed after an error:\n%s", d.Bytes())
			}
		})
	}
	if _, err := Edit([]byte("a: [1\n")); !errors.Is(err, ErrSyntax) {
		t.Fatalf("invalid yaml: got %v", err)
	}
}

func TestAliasAnchorEditAllowed(t *testing.T) {
	// The anchored definition itself can be edited; aliases see the change.
	d, _ := Edit([]byte("a: &x\n  b: 1\nc: *x\n"))
	if err := d.Set("a.b", 2); err != nil {
		t.Fatal(err)
	}
	if got, want := string(d.Bytes()), "a: &x\n  b: 2\nc: *x\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMultiDocument(t *testing.T) {
	in := "# first\na: 1\n---\n# second\na: 2\nb: [1]\n...\n---\na: 3\n"
	d, err := Edit([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if d.Len() != 3 {
		t.Fatalf("Len = %d", d.Len())
	}
	d1, err := d.Document(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d1.Set("a", 20); err != nil {
		t.Fatal(err)
	}
	if err := d1.Append("b", 2); err != nil {
		t.Fatal(err)
	}
	d2, _ := d.Document(2)
	if err := d2.Insert("", "z", true); err != nil {
		t.Fatal(err)
	}
	want := "# first\na: 1\n---\n# second\na: 20\nb: [1, 2]\n...\n---\na: 3\nz: true\n"
	if got := string(d.Bytes()); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if _, err := d.Document(3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Document(3): %v", err)
	}
}

func TestChainedEdits(t *testing.T) {
	d, _ := Edit([]byte("a: 1\nb:\n  - x\n"))
	steps := []step{
		{op: "set", path: "a", val: 2},
		{op: "append", path: "b", val: "yy"},
		{op: "insert", path: "", key: "c", val: "v"},
		{op: "rename", path: "c", key: "d"},
		{op: "delete", path: "b[0]"},
	}
	for _, s := range steps {
		if err := apply(d, s); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := string(d.Bytes()), "a: 2\nb:\n  - yy\nd: v\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEmptyStringSlices(t *testing.T) {
	d, err := Edit(nil)
	if err != nil || d.Len() != 0 {
		t.Fatalf("empty input: %v len=%d", err, d.Len())
	}
}
