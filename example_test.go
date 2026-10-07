package yamlsplice_test

import (
	"fmt"

	"github.com/quietpelican42/yamlsplice"
)

func Example() {
	src := "# service config\nname: api   # keep this comment\n\nports:\n  - 80\n\nimage: 'nginx:1.25'\n"
	doc, _ := yamlsplice.Edit([]byte(src))
	_ = doc.Set("image", "nginx:1.27") // keeps the single quotes
	_ = doc.Append("ports", 443)       // new line after the last item
	_ = doc.Insert("", "replicas", 3)  // after the last key
	_ = doc.Rename("name", "service")  // only the key bytes change
	fmt.Print(string(doc.Bytes()))
	// Output:
	// # service config
	// service: api   # keep this comment
	//
	// ports:
	//   - 80
	//   - 443
	//
	// image: 'nginx:1.27'
	// replicas: 3
}
