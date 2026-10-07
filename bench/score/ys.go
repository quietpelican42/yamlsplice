package main

import ys "github.com/quietpelican42/yamlsplice"

func editYamlsplice(src []byte, e Edit) ([]byte, error) {
	d, err := ys.Edit(src)
	if err != nil {
		return nil, err
	}
	p := goccyPath(e.Path)
	switch e.Op {
	case OpSet:
		err = d.Set(p, e.Value)
	case OpRename:
		err = d.Rename(p, e.NewKey)
	case OpDelete:
		err = d.Delete(p)
	case OpAdd:
		err = d.Insert(p, e.NewKey, e.Value)
	case OpAppend:
		err = d.Append(p, e.Value)
	}
	if err != nil {
		return nil, err
	}
	return d.Bytes(), nil
}
