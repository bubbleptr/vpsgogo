//go:build !linux

package monitoring

import "errors"

func sysStatfs(string) (used, total uint64, err error) {
	return 0, 0, errors.New("monitoring: statfs is only implemented on linux")
}
