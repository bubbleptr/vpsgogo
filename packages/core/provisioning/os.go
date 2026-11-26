package provisioning

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrUnsupportedOS stops provisioning before anything is changed.
var ErrUnsupportedOS = errors.New("provisioning: unsupported operating system")

// OS is what /etc/os-release says.
type OS struct {
	ID        string
	VersionID string
	Pretty    string
}

func parseOSRelease(s string) OS {
	var o OS
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			o.ID = v
		case "VERSION_ID":
			o.VersionID = v
		case "PRETTY_NAME":
			o.Pretty = v
		}
	}
	return o
}

// minVersion is the oldest supported release per distribution (docs/SPEC.md §1).
var minVersion = map[string][2]int{"debian": {12, 0}, "ubuntu": {22, 4}}

func (o OS) supported() error {
	min, known := minVersion[o.ID]
	if !known {
		return fmt.Errorf("%w: %q (Debian 12+ or Ubuntu 22.04+ is needed)", ErrUnsupportedOS, o.ID)
	}
	major, minor, ok := parseVersion(o.VersionID)
	if !ok || major < min[0] || major == min[0] && minor < min[1] {
		return fmt.Errorf("%w: %s %q", ErrUnsupportedOS, o.ID, o.VersionID)
	}
	return nil
}

func parseVersion(v string) (major, minor int, ok bool) {
	a, b, _ := strings.Cut(v, ".")
	major, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, false
	}
	if b != "" {
		if minor, err = strconv.Atoi(b); err != nil {
			return 0, 0, false
		}
	}
	return major, minor, true
}
