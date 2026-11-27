package embed

import (
	"strings"
	"testing"
)

func TestBinaryNotBuiltSaysHowToBuildIt(t *testing.T) {
	_, err := Binary("riscv64")
	if err == nil || !strings.Contains(err.Error(), "gogo-agent for linux/riscv64 is not built; run scripts/build-agent.sh") {
		t.Fatalf("err = %v", err)
	}
}
