module yamlsplice-bench

go 1.26.1

replace github.com/__GH_OWNER__/yamlsplice => ../..

require (
	github.com/__GH_OWNER__/yamlsplice v0.0.0
	github.com/braydonk/yaml v0.9.0
	github.com/goccy/go-yaml v1.19.2
	github.com/suzuki-shunsuke/go-yamledit v0.0.5
	github.com/yamledit/yamledit v0.0.0-20260814163200-b31bf9fe9274
	gopkg.in/yaml.v3 v3.0.1
)

require github.com/evanphx/json-patch/v5 v5.9.11 // indirect
