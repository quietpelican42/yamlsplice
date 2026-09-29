# yamlsplice

A Go library and CLI that edits YAML by splicing new text into the original bytes, so comments, blank lines, indentation, quoting, flow versus block style, key order and trailing whitespace outside the edited value or key stay as they were.

```go
doc, _ := yamlsplice.Edit(src)
doc.Set("spec.replicas", 3)              // keeps the quoting and the trailing comment
doc.Append("spec.ports", 8443)           // one new line after the last item
doc.Insert("spec", "paused", true)       // after the last key, blank-line groups intact
doc.Rename("metadata.name", "title")     // only the key bytes change
doc.Delete("spec.old")                   // its comment lines go with it
out := doc.Bytes()
```

```sh
yamlsplice set -i deploy.yaml spec.replicas 3
yamlsplice delete deploy.yaml metadata.annotations.owner
```

## Why

Parse-and-re-encode libraries rewrite the whole file. Blank lines vanish, indentation is normalized, comment spacing changes and quotes are dropped. A line-based diff of the result is noisy (see mikefarah/yq#515, "yq write strips completely blank lines"). yamlsplice uses a YAML parser only to find where each node sits, then replaces just those bytes.

Measured: `bench/score` applies five edits (set, rename, delete, add, append) to each of 61 real YAML files (GitHub workflows, Kubernetes manifests, Helm values, docker-compose files, OpenAPI specs, GitLab CI templates), 294 edits in total. An edit is clean when the result re-parses to exactly the expected data and a line diff shows only the intended lines changed (delete may also remove comment lines; it may not remove blank lines or other content). Six of the files are multi-document streams; the edit targets the first document.

```
$ sh bench/fetch-corpus.sh && (cd bench/score && go run . -corpus ../corpus)
usable files: 61  kinds: map[compose:9 gh-workflow:9 gitlab:7 helm:7 k8s:25 openapi:4]

adapter         set         rename      delete      add         append      set+del+add   all           
yaml.v3-node    6/60        6/60        7/60        6/61        5/53        19/181=10.5%  30/294=10.2%  
braydonk-yaml   6/60        6/60        7/60        6/61        5/53        19/181=10.5%  30/294=10.2%  
goccy-ast       31/60       n/a         35/60       36/61       31/53       102/181=56.4% 133/234=56.8% 
go-yamledit     30/60       27/60       31/60       31/61       28/53       92/181=50.8%  147/294=50.0% 
yamledit        51/60       n/a         44/60       52/61       41/53       147/181=81.2% 188/234=80.3% 
yamlsplice      60/60       60/60       60/60       61/61       53/53       181/181=100.0%294/294=100.0%
```

Five runs gave identical numbers. Notes on the incumbents:

- `yaml.v3-node` is a yaml.v3 Node round trip with an indent detector. `braydonk-yaml` is the yamlfmt fork of yaml.v3 (v0.9.0); it scores the same because its blank-line handling lives in yamlfmt, not in the fork. Neither has a path API; the adapters navigate the Node tree.
- `goccy-ast` is goccy/go-yaml v1.19.2 through its path API (set, add, append) plus a manual AST removal for delete. It has no rename.
- `go-yamledit` is suzuki-shunsuke/go-yamledit v0.0.5, a high-level API over the goccy AST.
- `yamledit` is github.com/yamledit/yamledit (a pseudo-version; the repository has no tags and no LICENSE file). It also aims at byte-level edits. It requires a mapping at the root and rejects multi-document streams, which accounts for most of its errors here. Restricted to the 54 single-document files it scores 147/160 = 91.9% on set, delete and add; it scores 97.5% there if delete is also allowed to remove a neighbouring blank line. It has no rename.
- vmware-labs/go-yaml-edit (archived) was not scored: as a dependency it does not compile against released yaml.v3, because it needs a `replace` to an unmerged fork that adds `Node.Index`.

The corpus files are fetched from URLs pinned to a commit with a sha256 each (`bench/corpus.tsv`) and are not stored in this repository. Adapters for the incumbents are in `bench/score`; the paths are chosen by a seeded pick per file and operation, so results are repeatable.

## Installation

```sh
go get github.com/__GH_OWNER__/yamlsplice
go install github.com/__GH_OWNER__/yamlsplice/cmd/yamlsplice@latest
```

The only dependency is gopkg.in/yaml.v3, used to find where nodes are in the source and to check results (see How it works).

## Usage

### Edit values

```go
doc, err := yamlsplice.Edit(src)      // parse; ErrSyntax if src is not YAML
err = doc.Set("a.b[2].c", "value")    // replace, or add the key if the parent exists
err = doc.Set("/a/b/2/c", 42)         // JSON Pointer works too
out := doc.Bytes()
```

A string keeps the quote style of the value it replaces. It is quoted when it would otherwise read back as another type, so `"yes"`, `"on"`, `"0777"`, `"~"`, `"2001-12-14"` and `"x: y"` are written quoted. Integers, booleans, floats and nil are written plain. `Raw("[1, 2]")` inserts a YAML fragment verbatim.

### Add keys and items

```go
doc.Insert("spec", "paused", true)                          // after the last key
doc.Insert("spec", "paused", true, yamlsplice.After("replicas"))
doc.Insert("spec", "paused", true, yamlsplice.Before("replicas"))
doc.Insert("spec", "paused", true, yamlsplice.First())
doc.Append("spec.args", "--verbose")
```

New content follows its surroundings: the indentation step is measured from sibling keys (falling back to the rest of the document), sequences are written flush (`- x` at the key's column) or indented to match, strings take the dominant quote style of their siblings, and collections are written in flow style when the sibling collections are. A new key goes on the line after its last sibling, so blank-line groups stay intact.

### Multi-document files

```go
doc, _ := yamlsplice.Edit(src)         // addresses document 0
second, _ := doc.Document(1)           // shares the same bytes
second.Set("metadata.name", "x")
```

## Reference

### Library

| Function | Description |
| --- | --- |
| `Edit(src []byte) (*Doc, error)` | Parses a stream. The result addresses the first document. |
| `(*Doc).Bytes() []byte` | Current bytes of the whole stream. |
| `(*Doc).Len() int` | Number of documents. |
| `(*Doc).Document(i int) (*Doc, error)` | Handle for document `i` on the same bytes. |
| `(*Doc).Set(path string, value any) error` | Replaces the value; adds the key at the end of an existing mapping if the last segment is missing. |
| `(*Doc).Delete(path string) error` | Removes a mapping key or sequence item. |
| `(*Doc).Rename(path, newKey string) error` | Renames a mapping key. |
| `(*Doc).Insert(path, key string, value any, pos ...Position) error` | Adds a key to the mapping at `path`. |
| `(*Doc).Append(path string, value any) error` | Adds an item to the sequence at `path`. |
| `After(k)`, `Before(k)`, `First()` | Positions for `Insert`. |
| `Raw` | A string type whose contents are inserted as a YAML fragment. |

Values may be `string`, `bool`, integers, floats, `nil`, `Raw`, slices, and maps with string keys (written in sorted key order). `Insert` and `Append` also work on a key whose value is empty (`key:`), which becomes a mapping or sequence.

Paths: `a.b[2].c`, a quoted segment `a."b.c"` or `a['b.c']`, a negative index `a[-1]`, a leading `$` is ignored, and a leading `/` selects JSON Pointer syntax (`/a/b~1c/2`). A bare number after a dot (`a.2`) is an index on a sequence and a key on a mapping. The empty path is the document root.

| Error | Meaning |
| --- | --- |
| `ErrSyntax` | The input is not valid YAML. |
| `ErrPath` | The path expression is malformed. |
| `ErrNotFound` | The path does not exist. |
| `ErrAlias` | The edit would go through an alias or a merge key. |
| `ErrExists` | The key to insert or rename to already exists. |
| `ErrUnsupported` | A construct the splicer cannot edit safely. |
| `ErrValue` | The value cannot be converted to YAML. |
| `ErrVerify` | The result did not match the expected structure. The document is unchanged. |

### Rules

- Editing through an alias is an error (`ErrAlias`). `c.b` where `c: *x` is refused, as is a key that could only come from a `<<` merge. Edit the anchored node instead (`a.b` where `a: &x`). Aliases to it see the new value. Replacing an alias value itself (`Set("c", 5)`) is allowed and removes the reference.
- Deleting a key removes the whole entry: the key line, its value, and the trailing comment on those lines. It also removes the comment lines directly above the key (no blank line between) and comment lines nested deeper below the value. A comment block at the top of a document is never taken. Blank lines are never removed, so deleting the last key of a group leaves the blank line that followed it.
- Replacing a block collection with another value drops the comments inside it. A comment on the `key:` line is kept.
- Inserted lines use CRLF when the file does.

### CLI

```
yamlsplice set    [-i] [-d N] [-s] file path value
yamlsplice delete [-i] [-d N] file path
yamlsplice rename [-i] [-d N] file path newkey
yamlsplice append [-i] [-d N] [-s] file path value
yamlsplice insert [-i] [-d N] [-s] [-after K | -before K | -first] file path key value
```

| Flag | Description |
| --- | --- |
| `-i` | Write the result back to the file. Without it the result goes to standard output. |
| `-d N` | Document index in a multi-document stream (default 0). |
| `-s` | Always treat the value as a string. |
| `-t` | Always parse the value as YAML, even over an existing string. |
| `-after K`, `-before K`, `-first` | `insert` only: where the key goes. |

A file of `-` reads standard input. Values are parsed as YAML, so `8080` is an integer, `true` a boolean and `[a, b]` a sequence. The exception is `set` over an existing string scalar: the value stays a string and keeps its quote style, so `'1.26'` becomes `'1.27'` and `latest` becomes `"1.20"` (plain `1.20` would be a float). Use `-t` to override. Flags may appear anywhere; put `--` before a value that starts with a dash.

| Exit code | Meaning |
| --- | --- |
| 0 | The edit was applied. |
| 1 | The edit was refused: path not found, key exists, edit through an alias, or no such document. The file is not touched. |
| 2 | Usage error, unreadable file, invalid YAML, or a construct that cannot be edited safely. |

## How it works

yaml.v3 parses the stream and reports where each node starts. yamlsplice works out where each node ends (scanning quoted scalars, block scalars, flow collections and plain scalars in the source text), and turns every edit into one or more splices of the original bytes. Key and value positions come from the parse, not from string search, so a key name that also appears in a comment or a string is not confused with the real one.

The result is parsed again and its structure is compared with the structure the edit should have produced (the old tree with the edit applied to it). On a mismatch the edit returns `ErrVerify` and the document keeps its previous bytes. The check covers a wrong position as well as a wrong value.

yaml.v3 reports exact start positions for every node, which is what the splicer needs, and it parsed all 61 corpus files. goccy/go-yaml was not compared as a locator. yaml.v3 is used only for locating and checking; no output comes from its encoder. The upstream repository (go-yaml/yaml) was archived on 2025-04-01; braydonk/yaml is a fork with the same API and can be substituted by changing the import.

## Limitations

- Line endings other than LF and CRLF (a bare carriage return, NEL, LS, PS) are refused with `ErrUnsupported`.
- Explicit keys (`? key`) can be read and set through, but deleting one is refused.
- A block scalar that ends a file with no final newline changes value when a line is added after it, so that edit is refused with `ErrVerify`.
- Replacing a block collection with a scalar or another collection is less polished than the other edits. Comments inside the replaced block are dropped.
- Anchors on a replaced block collection are dropped; the verification step then refuses the edit.
- Documents with duplicate keys are located by the first match.
- Flow collections spanning several lines are handled for append (including a trailing comma). Other edits inside them rely on the verification step, and comments inside them are not specially handled.
- New flow mappings are written as `{a: 1}` without inner padding.
- Edits are applied one at a time, and each one re-parses the stream. This is fast for configuration files (the measured corpus runs in seconds) but not meant for megabyte files edited thousands of times.
- YAML 1.1 quoting rules (`yes`, `no`, `on`, `off`, `y`, `n`, octal and sexagesimal numbers) are applied on write, so strings that need no quotes under YAML 1.2 are still quoted when a 1.1 parser would misread them.

## Compatibility

Built and tested with Go 1.27.1 on macOS arm64. The `go.mod` directive is Go 1.24; other Go versions and platforms were not run. The CI workflow runs the checks on the current stable Go on Linux ("run in CI", not tested here).

## Development

```sh
go build ./...
go vet ./...
go test -race -count=1 ./...
gofmt -l .                                   # prints nothing
go test -run xxx -fuzz FuzzEdit -fuzztime 60s .
sh bench/fetch-corpus.sh && (cd bench/score && go run . -corpus ../corpus)
```

`bench/score` is a separate Go module; it exits 1 if yamlsplice is not 100% on the corpus. It skips with exit 2 and a message when the corpus has not been fetched. Useful flags: `-seeds N` (N edits per operation per file), `-maxdocs 1` (single-document files only), `-lenient-blank` (delete may also remove blank lines), `-only a,b`, `-v`.

The tests include table-driven edits, a randomized property test (about 5,000 edits on generated files, checked against an independent data oracle and for untouched lines before and after the edit), a negative control showing that a yaml.v3 round trip is caught by that check, and a fuzz target.

## License

MIT. See [LICENSE](LICENSE).
