// Package profiles carries the ready-made business profiles inside the binary, so `joblox up` can start
// a shop on a computer that has nothing but the joblox executable.
package profiles

import "embed"

//go:embed *.yaml
var FS embed.FS

// Default is the profile a fresh installation starts with.
const Default = "bakery.yaml"
