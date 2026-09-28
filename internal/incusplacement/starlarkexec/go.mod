// Nested module: packing tests import go.starlark.net here so the root
// go.mod/go.sum stay identical to the pinned provider derivative.
module github.com/NDDev-OpenNetwork/github-actions/internal/incusplacement/starlarkexec

go 1.27.1

require (
	github.com/NDDev-OpenNetwork/github-actions v0.0.0
	github.com/lxc/incus/v7 v7.5.1
	go.starlark.net v0.0.0-20260908191801-89a6a09411d5
)

require (
	go.yaml.in/yaml/v4 v4.0.0-rc.6 // indirect
	golang.org/x/sys v0.48.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/NDDev-OpenNetwork/github-actions => ../../..
