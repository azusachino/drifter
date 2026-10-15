//go:build unix

package catalog

import (
	"fmt"
	"os"
	"syscall"
)

func openRegularFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("catalog file is not regular")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("catalog file unavailable")
	}
	file := os.NewFile(uintptr(fd), path)
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("catalog file is not regular")
	}
	return file, nil
}
