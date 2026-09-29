package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	sy "github.com/suzuki-shunsuke/go-yamledit/yamledit"
	ye "github.com/yamledit/yamledit"
)

func allAdapters() []Adapter {
	return []Adapter{
		{"yaml.v3-node", editYamlV3},
		{"braydonk-yaml", editBraydonk},
		{"goccy-ast", editGoccy},
		{"go-yamledit", editSuzuki},
		{"yamledit", editYamledit},
		{"yamlsplice", editYamlsplice},
	}
}

func goccyPath(p []Seg) string {
	var b strings.Builder
	b.WriteString("$")
	for _, s := range p {
		if s.IsIdx {
			fmt.Fprintf(&b, "[%d]", s.Idx)
		} else {
			b.WriteString("." + s.Key)
		}
	}
	return b.String()
}

func jsonPtr(p []Seg) string {
	var b strings.Builder
	for _, s := range p {
		if s.IsIdx {
			fmt.Fprintf(&b, "/%d", s.Idx)
		} else {
			b.WriteString("/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(s.Key))
		}
	}
	return b.String()
}

func editGoccy(src []byte, e Edit) ([]byte, error) {
	file, err := parser.ParseBytes(src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	switch e.Op {
	case OpSet:
		p, err := yaml.PathString(goccyPath(e.Path))
		if err != nil {
			return nil, err
		}
		n, err := yaml.ValueToNode(e.Value)
		if err != nil {
			return nil, err
		}
		if err := p.ReplaceWithNode(file, n); err != nil {
			return nil, err
		}
	case OpAdd:
		p, err := yaml.PathString(goccyPath(e.Path))
		if err != nil {
			return nil, err
		}
		n, err := yaml.ValueToNode(map[string]string{e.NewKey: e.Value})
		if err != nil {
			return nil, err
		}
		if err := p.MergeFromNode(file, n); err != nil {
			return nil, err
		}
	case OpAppend:
		p, err := yaml.PathString(goccyPath(e.Path))
		if err != nil {
			return nil, err
		}
		n, err := yaml.ValueToNode([]string{e.Value})
		if err != nil {
			return nil, err
		}
		if err := p.MergeFromNode(file, n); err != nil {
			return nil, err
		}
	case OpDelete:
		// goccy has no delete API; remove the MappingValueNode from its parent's Values.
		p, err := yaml.PathString(goccyPath(e.Path[:len(e.Path)-1]))
		if err != nil {
			return nil, err
		}
		var parent ast.Node = file.Docs[0].Body
		if len(e.Path) > 1 {
			parent, err = p.FilterFile(file)
			if err != nil {
				return nil, err
			}
		}
		mn, ok := parent.(*ast.MappingNode)
		if !ok {
			return nil, errors.New("not a block mapping")
		}
		k := e.Path[len(e.Path)-1].Key
		for i, v := range mn.Values {
			if v.Key.GetToken().Value == k {
				mn.Values = append(mn.Values[:i], mn.Values[i+1:]...)
				break
			}
		}
	default:
		return nil, ErrUnsupported
	}
	return []byte(file.String()), nil
}

func editSuzuki(src []byte, e Edit) ([]byte, error) {
	var act sy.Action
	switch e.Op {
	case OpSet:
		act = sy.MapAction(goccyPath(e.Path[:len(e.Path)-1]), sy.SetKey(e.Path[len(e.Path)-1].Key, e.Value, nil))
	case OpRename:
		act = sy.MapAction(goccyPath(e.Path[:len(e.Path)-1]), sy.RenameKey(e.Path[len(e.Path)-1].Key, e.NewKey, sy.RaiseError))
	case OpDelete:
		act = sy.MapAction(goccyPath(e.Path[:len(e.Path)-1]), sy.RemoveKeys(e.Path[len(e.Path)-1].Key))
	case OpAdd:
		act = sy.MapAction(goccyPath(e.Path), sy.SetKey(e.NewKey, e.Value, nil))
	case OpAppend:
		act = sy.ListAction(goccyPath(e.Path), sy.AddValuesToList(-1, e.Value))
	}
	// EditBytes runs actions on every document body; restrict to the first document.
	out, err := sy.EditBytes("x.yaml", src, act)
	return []byte(out), err
}

func editYamledit(src []byte, e Edit) ([]byte, error) {
	if e.Op == OpRename {
		return nil, ErrUnsupported
	}
	doc, err := ye.Parse(src)
	if err != nil {
		return nil, err
	}
	var patch []map[string]any
	switch e.Op {
	case OpSet:
		patch = []map[string]any{{"op": "replace", "path": jsonPtr(e.Path), "value": e.Value}}
	case OpDelete:
		patch = []map[string]any{{"op": "remove", "path": jsonPtr(e.Path)}}
	case OpAdd:
		patch = []map[string]any{{"op": "add", "path": jsonPtr(e.Path) + "/" + e.NewKey, "value": e.Value}}
	case OpAppend:
		patch = []map[string]any{{"op": "add", "path": jsonPtr(e.Path) + "/-", "value": e.Value}}
	}
	pb, _ := json.Marshal(patch)
	if err := ye.ApplyJSONPatchBytes(doc, pb); err != nil {
		return nil, err
	}
	return ye.Marshal(doc)
}

var _ = bytes.NewReader
