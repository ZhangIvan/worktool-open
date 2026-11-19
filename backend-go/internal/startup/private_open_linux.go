//go:build linux

package startup

import (
	"os"
	"syscall"
)

// Nonblocking open prevents a replaced FIFO from hanging configuration/gate
// checks; O_NOFOLLOW rejects a replaced symlink. readPrivate checks the opened
// inode's identity, type and permissions before reading any bytes.
func openPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
