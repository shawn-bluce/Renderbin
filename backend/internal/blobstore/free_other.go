//go:build !(linux || darwin || freebsd)

package blobstore

import "errors"

func freeBytes(string) (uint64, error) {
	return 0, errors.New("free space is not known on this platform")
}
