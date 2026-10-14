//go:build !unix

package matchingcatalog

import (
	"fmt"
	"os"
)

func openRegularFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("catalog file is not regular")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("catalog file unavailable")
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("catalog file is not regular")
	}
	return file, nil
}
