// Package embed carries the gogo-agent binaries that scripts/build-agent.sh
// cross-compiles, for provisioning to upload (docs/SPEC.md §2.2). Import it
// under another name where the standard embed package is in use.
package embed

import (
	"embed"
	"fmt"
)

//go:embed agent
var agent embed.FS

// Binary returns gogo-agent for linux/arch, "amd64" or "arm64".
func Binary(arch string) ([]byte, error) {
	b, err := agent.ReadFile("agent/gogo-agent-linux-" + arch)
	if err != nil {
		return nil, fmt.Errorf("gogo-agent for linux/%s is not built; run scripts/build-agent.sh", arch)
	}
	return b, nil
}
