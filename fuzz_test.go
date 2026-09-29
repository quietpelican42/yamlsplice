package yamlsplice

import (
	"bytes"
	"errors"
	"testing"
)

// FuzzEdit throws arbitrary bytes and edit choices at the package. Whatever
// the input, an edit must not panic; a failed edit must leave the bytes
// alone; a successful edit must produce YAML whose data matches the oracle.
func FuzzEdit(f *testing.F) {
	seeds := []string{
		"a: 1\n",
		"# c\na: {x: 1, y: [1, 2]}\nb:\n  - p\n  - q\n\nc: |\n  t\n",
		"- a: 1\n  b: 2\n- c\n",
		"a: &x 1\nb: *x\n---\nz: [1]\n",
		"a: 'q'   # c\r\nb: \"d\"\r\n",
		"? k\n: v\n",
		"a:\nb:\n  - \n  - x\n",
		"héllo: wörld\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s), uint8(0), uint16(0), "v")
		f.Add([]byte(s), uint8(2), uint16(1), "yes")
		f.Add([]byte(s), uint8(4), uint16(2), "x: y")
	}
	f.Fuzz(func(t *testing.T, data []byte, op uint8, pick uint16, val string) {
		if len(data) > 4096 {
			return
		}
		nodes, err := collectNodes(data)
		if err != nil || len(nodes) == 0 {
			return
		}
		before, derr := decodeAny(data)
		tn := nodes[int(pick)%len(nodes)]
		p := ptr(tn.path)
		d, err := Edit(data)
		if err != nil {
			if errors.Is(err, ErrUnsupported) {
				return
			}
			t.Fatalf("Edit rejected input that yaml.v3 accepts: %v", err)
		}
		dd, err := d.Document(tn.doc)
		if err != nil {
			return
		}
		var kind, key string
		var opErr error
		switch op % 5 {
		case 0:
			kind, opErr = "set", dd.Set(p, val)
		case 1:
			key = "k" + val
			kind, opErr = "rename", dd.Rename(p, key)
		case 2:
			kind, opErr = "delete", dd.Delete(p)
		case 3:
			key = "k" + val
			kind, opErr = "insert", dd.Insert(p, key, val)
		case 4:
			kind, opErr = "append", dd.Append(p, val)
		}
		if opErr != nil {
			if string(d.Bytes()) != string(data) {
				t.Fatalf("%s %s failed with %v but changed the bytes", kind, p, opErr)
			}
			return
		}
		out := d.Bytes()
		if derr != nil {
			// the input has duplicate keys or similar; only require valid output
			if _, err := buildIndex(out); err != nil {
				t.Fatalf("%s %s produced unparseable YAML: %v", kind, p, err)
			}
			return
		}
		got, err := decodeAny(out)
		if err != nil {
			t.Fatalf("%s %s produced unparseable YAML: %v\n--- in ---\n%q\n--- out ---\n%q", kind, p, err, data, out)
		}
		if bytes.ContainsAny(data, "*&") {
			return // editing an anchored node changes its aliases; the oracle does not model that
		}
		want := before
		if expectedAny(want, tn.doc, kind, tn.path, key, val) != nil {
			return
		}
		if !sameAny(want, got) {
			t.Fatalf("%s %s: data mismatch\n--- in ---\n%q\n--- out ---\n%q", kind, p, data, out)
		}
	})
}
