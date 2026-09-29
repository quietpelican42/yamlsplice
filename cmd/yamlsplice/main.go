// Command yamlsplice edits a YAML file, changing only the bytes of the edited
// value or key.
//
// Usage:
//
//	yamlsplice set    [-i] [-d N] [-s] file path value
//	yamlsplice delete [-i] [-d N] file path
//	yamlsplice rename [-i] [-d N] file path newkey
//	yamlsplice append [-i] [-d N] [-s] file path value
//	yamlsplice insert [-i] [-d N] [-s] [-after K | -before K | -first] file path key value
//
// Exit codes: 0 the edit was applied, 1 the edit was refused (path not found,
// key exists, edit through an alias), 2 usage error, unreadable file, invalid
// YAML or a construct that cannot be edited safely.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/__GH_OWNER__/yamlsplice"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

const usage = `usage:
  yamlsplice set    [-i] [-d N] [-s] file path value
  yamlsplice delete [-i] [-d N] file path
  yamlsplice rename [-i] [-d N] file path newkey
  yamlsplice append [-i] [-d N] [-s] file path value
  yamlsplice insert [-i] [-d N] [-s] [-after K | -before K | -first] file path key value

file "-" reads standard input. Without -i the result goes to standard output.
Values are read as YAML scalars (8080 is an integer); -s forces a string.
Paths look like a.b[2].c, or a JSON Pointer such as /a/b/2/c.
`

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	inPlace := fs.Bool("i", false, "write the result back to the file")
	docN := fs.Int("d", 0, "document index in a multi-document stream")
	asString := fs.Bool("s", false, "treat the value as a string")
	after := fs.String("after", "", "insert: place the key after this sibling")
	before := fs.String("before", "", "insert: place the key before this sibling")
	first := fs.Bool("first", false, "insert: place the key first")
	var need int
	switch cmd {
	case "set", "rename", "append":
		need = 3
	case "delete":
		need = 2
	case "insert":
		need = 4
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "yamlsplice: unknown command %q\n%s", cmd, usage)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != need {
		fmt.Fprintf(stderr, "yamlsplice %s: want %d arguments, got %d\n%s", cmd, need, len(rest), usage)
		return 2
	}
	file := rest[0]
	var src []byte
	var err error
	if file == "-" {
		if *inPlace {
			fmt.Fprintln(stderr, "yamlsplice: -i needs a file, not standard input")
			return 2
		}
		src, err = io.ReadAll(stdin)
	} else {
		src, err = os.ReadFile(file)
	}
	if err != nil {
		fmt.Fprintf(stderr, "yamlsplice: %v\n", err)
		return 2
	}
	doc, err := yamlsplice.Edit(src)
	if err != nil {
		fmt.Fprintf(stderr, "yamlsplice: %s: %v\n", file, err)
		return 2
	}
	d, err := doc.Document(*docN)
	if err != nil {
		fmt.Fprintf(stderr, "yamlsplice: %s: %v\n", file, err)
		return 1
	}
	switch cmd {
	case "set":
		err = d.Set(rest[1], parseValue(rest[2], *asString))
	case "delete":
		err = d.Delete(rest[1])
	case "rename":
		err = d.Rename(rest[1], rest[2])
	case "append":
		err = d.Append(rest[1], parseValue(rest[2], *asString))
	case "insert":
		pos := yamlsplice.Position{}
		switch {
		case *first:
			pos = yamlsplice.First()
		case *after != "":
			pos = yamlsplice.After(*after)
		case *before != "":
			pos = yamlsplice.Before(*before)
		}
		err = d.Insert(rest[1], rest[2], parseValue(rest[3], *asString), pos)
	}
	if err != nil {
		fmt.Fprintf(stderr, "yamlsplice: %s: %v\n", file, err)
		if errors.Is(err, yamlsplice.ErrNotFound) || errors.Is(err, yamlsplice.ErrAlias) || errors.Is(err, yamlsplice.ErrExists) {
			return 1
		}
		return 2
	}
	out := d.Bytes()
	if *inPlace {
		info, serr := os.Stat(file)
		mode := os.FileMode(0o644)
		if serr == nil {
			mode = info.Mode().Perm()
		}
		if werr := os.WriteFile(file, out, mode); werr != nil {
			fmt.Fprintf(stderr, "yamlsplice: %v\n", werr)
			return 2
		}
		return 0
	}
	if _, werr := stdout.Write(out); werr != nil {
		return 2
	}
	return 0
}

// parseValue reads s as a YAML scalar or collection. Anything that does not
// parse, or that YAML would turn into a timestamp, stays a string.
func parseValue(s string, asString bool) any {
	if asString {
		return s
	}
	var v any
	if err := yaml.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	switch x := v.(type) {
	case nil:
		if s == "" {
			return s
		}
		return nil
	case string:
		return x
	case bool, int, int64, uint64, float64, []any, map[string]any:
		return x
	}
	return s
}
