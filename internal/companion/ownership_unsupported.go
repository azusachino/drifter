//go:build !darwin && !linux

package companion

import "os"

func checkLockSupport() error             { return ErrOwnershipUnsupported }
func tryExclusiveFileLock(*os.File) error { return ErrOwnershipUnsupported }
func unlockFile(*os.File) error           { return ErrOwnershipUnsupported }
func requireSingleLink(os.FileInfo) error { return ErrOwnershipUnsupported }
