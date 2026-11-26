// Command gogo-agent runs on the VPS: it collects metrics and sends alerts and
// renewal reminders while the desktop app is closed. See docs/SPEC.md §4.2.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: gogo-agent version")
	os.Exit(2)
}
