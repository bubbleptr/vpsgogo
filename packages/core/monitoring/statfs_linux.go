//go:build linux

package monitoring

import "syscall"

func sysStatfs(mount string) (used, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return (st.Blocks - st.Bfree) * bsize, st.Blocks * bsize, nil
}
