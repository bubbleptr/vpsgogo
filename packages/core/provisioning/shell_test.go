package provisioning

import (
	"os/exec"
	"testing"
)

// shellQuote must survive a real shell unchanged, whatever the input.
func TestShellQuoteRoundTrips(t *testing.T) {
	for _, s := range []string{"plain", "it's", `"double"`, "$HOME `id` $(id)", "a\nb", "back\\slash", "", "'"} {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != s {
			t.Fatalf("shellQuote(%q) came back as %q", s, out)
		}
	}
}
