package main

import (
	"bytes"
	"errors"
	"io"
	"strings"

	y "github.com/braydonk/yaml"
)

func detectIndentB(src []byte) int {
	hist := map[int]int{}
	prev := 0
	for _, l := range lines(src) {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") || t == "---" {
			continue
		}
		ind := len(l) - len(strings.TrimLeft(l, " "))
		if ind > prev {
			hist[ind-prev]++
		}
		prev = ind
	}
	best, bc := 2, 0
	for k := 2; k <= 8; k++ {
		if hist[k] > bc {
			best, bc = k, hist[k]
		}
	}
	return best
}

func nodeNavB(n *y.Node, p []Seg) *y.Node {
	for _, s := range p {
		if n.Kind == y.DocumentNode {
			n = n.Content[0]
		}
		if s.IsIdx {
			n = n.Content[s.Idx]
			continue
		}
		var nx *y.Node
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == s.Key {
				nx = n.Content[i+1]
			}
		}
		n = nx
	}
	return n
}

func editBraydonk(src []byte, e Edit) ([]byte, error) {
	dec := y.NewDecoder(bytes.NewReader(src))
	var docs []*y.Node
	for {
		var d y.Node
		err := dec.Decode(&d)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, &d)
	}
	root := docs[0].Content[0]
	switch e.Op {
	case OpSet:
		n := nodeNavB(root, e.Path)
		n.Tag = "!!str"
		n.Value = e.Value
	case OpRename:
		par := nodeNavB(root, e.Path[:len(e.Path)-1])
		for i := 0; i+1 < len(par.Content); i += 2 {
			if par.Content[i].Value == e.Path[len(e.Path)-1].Key {
				par.Content[i].Value = e.NewKey
			}
		}
	case OpDelete:
		par := nodeNavB(root, e.Path[:len(e.Path)-1])
		for i := 0; i+1 < len(par.Content); i += 2 {
			if par.Content[i].Value == e.Path[len(e.Path)-1].Key {
				par.Content = append(par.Content[:i], par.Content[i+2:]...)
				break
			}
		}
	case OpAdd:
		n := nodeNavB(root, e.Path)
		n.Content = append(n.Content,
			&y.Node{Kind: y.ScalarNode, Tag: "!!str", Value: e.NewKey},
			&y.Node{Kind: y.ScalarNode, Tag: "!!str", Value: e.Value})
	case OpAppend:
		n := nodeNavB(root, e.Path)
		n.Content = append(n.Content, &y.Node{Kind: y.ScalarNode, Tag: "!!str", Value: e.Value})
	default:
		return nil, errors.New("op")
	}
	var buf bytes.Buffer
	enc := y.NewEncoder(&buf)
	enc.SetIndent(detectIndentB(src))
	for _, d := range docs {
		if err := enc.Encode(d); err != nil {
			return nil, err
		}
	}
	enc.Close()
	return buf.Bytes(), nil
}
