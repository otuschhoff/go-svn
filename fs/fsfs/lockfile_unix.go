//go:build !windows

package fsfs

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(file *os.File) error {
	return unix.FcntlFlock(file.Fd(), unix.F_SETLKW, &unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0})
}

func unlockFile(file *os.File) error {
	return unix.FcntlFlock(file.Fd(), unix.F_SETLK, &unix.Flock_t{Type: unix.F_UNLCK, Whence: 0, Start: 0, Len: 0})
}
