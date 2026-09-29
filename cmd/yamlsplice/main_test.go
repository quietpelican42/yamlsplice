package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(stdin string, args ...string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

func TestCLI(t *testing.T) {
	const src = "# top\nname: app # n\n\nports:\n  - 80\nlabels: {a: b}\n"
	tests := []struct {
		name     string
		args     []string
		wantCode int
		want     string // expected stdout
		errHas   string
	}{
		{"set int", []string{"set", "-", "name", "8080"}, 0, "# top\nname: 8080 # n\n\nports:\n  - 80\nlabels: {a: b}\n", ""},
		{"set string flag", []string{"set", "-s", "-", "name", "8080"}, 0, "# top\nname: \"8080\" # n\n\nports:\n  - 80\nlabels: {a: b}\n", ""},
		{"set 1.1 trap is quoted", []string{"set", "-", "name", "yes"}, 0, "# top\nname: \"yes\" # n\n\nports:\n  - 80\nlabels: {a: b}\n", ""},
		{"delete", []string{"delete", "-", "name"}, 0, "# top\n\nports:\n  - 80\nlabels: {a: b}\n", ""},
		{"rename", []string{"rename", "-", "name", "title"}, 0, "# top\ntitle: app # n\n\nports:\n  - 80\nlabels: {a: b}\n", ""},
		{"append", []string{"append", "-", "ports", "443"}, 0, "# top\nname: app # n\n\nports:\n  - 80\n  - 443\nlabels: {a: b}\n", ""},
		{"insert after", []string{"insert", "-after", "name", "-", "", "env", "prod"}, 0, "# top\nname: app # n\nenv: prod\n\nports:\n  - 80\nlabels: {a: b}\n", ""},
		{"json pointer", []string{"set", "-", "/labels/a", "z"}, 0, "# top\nname: app # n\n\nports:\n  - 80\nlabels: {a: z}\n", ""},
		{"missing path is exit 1", []string{"set", "-", "nope.x", "1"}, 1, "", "path not found"},
		{"exists is exit 1", []string{"rename", "-", "name", "ports"}, 1, "", "already exists"},
		{"usage is exit 2", []string{"set", "-"}, 2, "", "want 3 arguments"},
		{"unknown command is exit 2", []string{"frob"}, 2, "", "unknown command"},
		{"no args is exit 2", nil, 2, "", "usage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errs := runCLI(src, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit %d, want %d (stderr %q)", code, tc.wantCode, errs)
			}
			if out != tc.want {
				t.Fatalf("stdout:\n%s\nwant:\n%s", out, tc.want)
			}
			if !strings.Contains(errs, tc.errHas) {
				t.Fatalf("stderr %q does not contain %q", errs, tc.errHas)
			}
		})
	}
}

func TestCLIInPlaceAndInvalid(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.yaml")
	if err := os.WriteFile(f, []byte("a: 1\n\nb: 2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := runCLI("", "set", "-i", f, "a", "5"); code != 0 || out != "" {
		t.Fatalf("code %d out %q err %q", code, out, errs)
	}
	got, _ := os.ReadFile(f)
	if string(got) != "a: 5\n\nb: 2\n" {
		t.Fatalf("file: %q", got)
	}
	if info, _ := os.Stat(f); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed: %v", info.Mode())
	}
	// a refused edit leaves the file alone
	if code, _, _ := runCLI("", "delete", "-i", f, "zzz"); code != 1 {
		t.Fatalf("code %d", code)
	}
	got, _ = os.ReadFile(f)
	if string(got) != "a: 5\n\nb: 2\n" {
		t.Fatalf("file changed: %q", got)
	}
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("a: [1\n"), 0o644)
	if code, _, errs := runCLI("", "set", bad, "a", "1"); code != 2 || !strings.Contains(errs, "invalid YAML") {
		t.Fatalf("invalid yaml: code %d err %q", code, errs)
	}
	if code, _, _ := runCLI("", "set", filepath.Join(dir, "missing.yaml"), "a", "1"); code != 2 {
		t.Fatalf("missing file: code %d", code)
	}
	if code, _, _ := runCLI("a: 1\n", "set", "-i", "-", "a", "1"); code != 2 {
		t.Fatalf("-i with stdin: code %d", code)
	}
}

func TestCLIMultiDocument(t *testing.T) {
	src := "a: 1\n---\na: 2\n"
	code, out, _ := runCLI(src, "set", "-d", "1", "-", "a", "9")
	if code != 0 || out != "a: 1\n---\na: 9\n" {
		t.Fatalf("code %d out %q", code, out)
	}
	if code, _, _ := runCLI(src, "set", "-d", "5", "-", "a", "9"); code != 1 {
		t.Fatalf("missing doc: code %d", code)
	}
}
