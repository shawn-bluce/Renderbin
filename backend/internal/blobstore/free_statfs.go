//go:build linux || darwin || freebsd

package blobstore

import "syscall"

// freeBytes is the space available to an unprivileged process on path's
// filesystem (Bavail, not Bfree: the root-reserved blocks are not ours).
// The conversions are there because the field types differ by platform.
func freeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
