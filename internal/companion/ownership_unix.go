//go:build darwin || linux

package companion

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func checkLockSupport() error { return nil }

func tryExclusiveFileLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrDatabaseOwned
	}
	return err
}

func unlockFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func requireSingleLink(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot identify database file")
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("database must not have hard-link aliases")
	}
	return nil
}
