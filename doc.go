// Package yamlsplice edits YAML documents by splicing new text into the
// original bytes. Everything outside the edited value or key is left as it
// was: comments, blank lines, indentation, quoting, flow or block style, key
// order and trailing whitespace.
//
// A YAML parser is used only to find where each node sits in the source. The
// edit itself is a byte splice. After every edit the result is parsed again and
// compared with the structure that the edit should have produced; a mismatch
// returns ErrVerify and leaves the document unchanged.
package yamlsplice

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Errors returned by Doc methods. Use errors.Is to test for them.
var (
	// ErrSyntax means the input is not valid YAML.
	ErrSyntax = errors.New("yamlsplice: invalid YAML")
	// ErrPath means a path expression could not be parsed.
	ErrPath = errors.New("yamlsplice: invalid path")
	// ErrNotFound means a path does not exist in the document.
	ErrNotFound = errors.New("yamlsplice: path not found")
	// ErrAlias means the edit would go through an alias or a merge key. The
	// aliased content is shared, so editing it here would change every place
	// that refers to it. Edit the anchored node instead.
	ErrAlias = errors.New("yamlsplice: edit through an alias")
	// ErrExists means a key to insert or rename to already exists.
	ErrExists = errors.New("yamlsplice: key already exists")
	// ErrUnsupported means the source uses a construct the splicer cannot
	// edit safely, such as an explicit "? " key.
	ErrUnsupported = errors.New("yamlsplice: unsupported construct")
	// ErrValue means a value could not be converted to YAML.
	ErrValue = errors.New("yamlsplice: invalid value")
	// ErrVerify means the edit did not produce the expected structure. The
	// document is unchanged.
	ErrVerify = errors.New("yamlsplice: verification failed")
)

type state struct {
	buf []byte
	ix  *index
}

// Doc is a YAML stream opened for editing. A stream may hold several
// documents separated by "---"; Doc addresses one of them and shares its
// bytes with the Docs returned by Doc.Document.
type Doc struct {
	s *state
	i int
}

// Edit parses src and returns a handle for editing it. The returned Doc
// addresses the first document of the stream.
func Edit(src []byte) (*Doc, error) {
	buf := append([]byte(nil), src...)
	ix, err := buildIndex(buf)
	if err != nil {
		return nil, err
	}
	return &Doc{s: &state{buf: buf, ix: ix}}, nil
}

// Bytes returns the current bytes of the whole stream.
func (d *Doc) Bytes() []byte { return append([]byte(nil), d.s.buf...) }

// Len returns the number of documents in the stream.
func (d *Doc) Len() int { return len(d.s.ix.docs) }

// Document returns a handle for the i-th document (zero based) of the same
// stream. Edits through either handle change the shared bytes.
func (d *Doc) Document(i int) (*Doc, error) {
	if i < 0 || i >= len(d.s.ix.docs) {
		return nil, fmt.Errorf("%w: document %d (stream has %d)", ErrNotFound, i, len(d.s.ix.docs))
	}
	return &Doc{s: d.s, i: i}, nil
}

func (d *Doc) idx() *docIndex { return d.s.ix.docs[d.i] }

type splice struct {
	start, end int
	text       string
}

// commit applies the splices, re-parses, and checks the result against h.
func (d *Doc) commit(sp []splice, h *hook) error {
	sort.SliceStable(sp, func(a, b int) bool { return sp[a].start < sp[b].start })
	for i := 1; i < len(sp); i++ {
		if sp[i].start < sp[i-1].end {
			return fmt.Errorf("%w: overlapping splices", ErrVerify)
		}
	}
	old := d.s.buf
	crlf := bytes.Contains(old, []byte("\r\n"))
	if crlf {
		for i := range sp {
			sp[i].text = strings.ReplaceAll(sp[i].text, "\n", "\r\n")
		}
	}
	nb := make([]byte, 0, len(old)+64)
	pos := 0
	for _, s := range sp {
		nb = append(nb, old[pos:s.start]...)
		nb = append(nb, s.text...)
		pos = s.end
	}
	nb = append(nb, old[pos:]...)
	nix, err := buildIndex(nb)
	if err != nil {
		return fmt.Errorf("%w: result does not parse: %v", ErrVerify, err)
	}
	h.doc = d.i
	want, got := docSigs(d.s.ix, h), docSigs(nix, nil)
	if len(want) != len(got) {
		return fmt.Errorf("%w: document count changed", ErrVerify)
	}
	for i := range want {
		if want[i] != got[i] {
			return fmt.Errorf("%w: document %d differs from the expected result", ErrVerify, i)
		}
	}
	d.s.buf, d.s.ix = nb, nix
	return nil
}
