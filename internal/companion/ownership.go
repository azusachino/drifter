package companion

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	errDatabaseParentMissing = errors.New("database parent does not exist")
	// ErrDatabaseOwned means another process currently owns or writes this state.
	ErrDatabaseOwned = errors.New("mapping database is owned or write-locked by another process")
	// ErrOwnershipUnsupported means this build has no verified OS lock backend.
	ErrOwnershipUnsupported = errors.New("database ownership locks are supported only on macOS and Linux")
)

type databaseFileLock struct {
	path string
	side *os.File
}

// DatabaseOwnership is the daemon's exclusive, process-lifetime database lease.
// Its kernel locks are released automatically when its process exits.
type DatabaseOwnership struct {
	path string
	lock *databaseFileLock

	// Active scopes hold a read lock. Close rejects new scopes, then waits for
	// existing scopes to finish before releasing the OS lock.
	mu        sync.RWMutex
	closing   atomic.Bool
	closed    bool
	closeOnce sync.Once
	closeErr  error
}

func canonicalDatabasePath(path string, createParent bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("database path required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("database path unavailable")
	}
	dir := filepath.Dir(absolute)
	if createParent {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("database directory unavailable")
		}
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		if !createParent && errors.Is(err, os.ErrNotExist) {
			return "", errDatabaseParentMissing
		}
		return "", fmt.Errorf("database directory unavailable")
	}
	info, err := os.Stat(resolvedDir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return "", fmt.Errorf("database parent must not be writable by other users")
	}
	return filepath.Join(resolvedDir, filepath.Base(absolute)), nil
}

func openPrivateRegular(path string, create bool) (*os.File, error) {
	if create {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("state lock or database must be a regular, non-symlink mode-600 file")
	}
	if err := requireSingleLink(info); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, fmt.Errorf("state path changed while opening")
	}
	return file, nil
}

func acquireDatabaseFileLock(path string, create bool) (*databaseFileLock, error) {
	if err := validateExistingDatabaseFile(path); err != nil {
		return nil, err
	}
	side, err := openPrivateRegular(path+".owner.lock", create)
	if err != nil {
		return nil, fmt.Errorf("database ownership lock unavailable")
	}
	if err := tryExclusiveFileLock(side); err != nil {
		_ = side.Close()
		if errors.Is(err, ErrDatabaseOwned) || errors.Is(err, ErrOwnershipUnsupported) {
			return nil, err
		}
		return nil, fmt.Errorf("database ownership lock unavailable")
	}
	db, err := openPrivateRegular(path, create)
	if err != nil {
		_ = unlockFile(side)
		_ = side.Close()
		return nil, fmt.Errorf("database file unavailable: %w", err)
	}
	if err := db.Close(); err != nil {
		_ = unlockFile(side)
		_ = side.Close()
		return nil, fmt.Errorf("database file unavailable")
	}
	return &databaseFileLock{path: path, side: side}, nil
}

func (lock *databaseFileLock) Close() error {
	if lock == nil {
		return nil
	}
	var result error
	if lock.side != nil {
		if err := unlockFile(lock.side); result == nil && err != nil {
			result = err
		}
		if err := lock.side.Close(); result == nil && err != nil {
			result = err
		}
	}
	return result
}

// AcquireDatabaseOwnership takes the daemon lease before state initialization.
func AcquireDatabaseOwnership(path string) (*DatabaseOwnership, error) {
	if err := checkLockSupport(); err != nil {
		return nil, err
	}
	canonical, err := canonicalDatabasePath(path, true)
	if err != nil {
		return nil, err
	}
	lock, err := acquireDatabaseFileLock(canonical, true)
	if err != nil {
		return nil, err
	}
	return &DatabaseOwnership{path: canonical, lock: lock}, nil
}

func (owner *DatabaseOwnership) Close() error {
	if owner == nil {
		return nil
	}
	owner.closeOnce.Do(func() {
		owner.closing.Store(true)
		owner.mu.Lock()
		defer owner.mu.Unlock()
		owner.closed = true
		owner.closeErr = owner.lock.Close()
	})
	return owner.closeErr
}

func (owner *DatabaseOwnership) acquire(path string) (func(), error) {
	if owner == nil {
		return nil, fmt.Errorf("database ownership capability does not match this path")
	}
	owner.mu.RLock()
	if owner.closing.Load() || owner.closed || path != owner.path {
		owner.mu.RUnlock()
		return nil, fmt.Errorf("database ownership capability is no longer active")
	}
	return owner.mu.RUnlock, nil
}

func (owner *DatabaseOwnership) owns(path string) bool {
	canonical, err := canonicalDatabasePath(path, false)
	if err != nil {
		return false
	}
	release, err := owner.acquire(canonical)
	if err != nil {
		return false
	}
	release()
	return true
}

// CheckDatabaseWriteAvailable is a non-creating preflight for core operations
// that must reject daemon-owned state before contacting a provider. Store writes
// still take their own guard to close the check/use race.
func CheckDatabaseWriteAvailable(path string) error {
	if err := checkLockSupport(); err != nil {
		return err
	}
	canonical, err := canonicalDatabasePath(path, false)
	if errors.Is(err, errDatabaseParentMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	lock, err := acquireExistingDatabaseFileLock(canonical)
	if err != nil {
		return err
	}
	return lock.Close()
}

func validateExistingDatabaseFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("database file unavailable")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return fmt.Errorf("database must be a regular, non-symlink mode-600 file")
	}
	return requireSingleLink(info)
}

func acquireExistingDatabaseFileLock(path string) (*databaseFileLock, error) {
	var side *os.File
	lockPath := path + ".owner.lock"
	_, sideStatErr := os.Lstat(lockPath)
	if sideStatErr == nil {
		var err error
		side, err = openPrivateRegular(lockPath, false)
		if err != nil {
			return nil, fmt.Errorf("database ownership lock unavailable")
		}
		if err := tryExclusiveFileLock(side); err != nil {
			_ = side.Close()
			return nil, err
		}
	} else if !errors.Is(sideStatErr, os.ErrNotExist) {
		return nil, fmt.Errorf("database ownership lock unavailable")
	}

	var db *os.File
	_, dbStatErr := os.Lstat(path)
	if dbStatErr == nil {
		var err error
		db, err = openPrivateRegular(path, false)
		if err != nil {
			if side != nil {
				_ = unlockFile(side)
				_ = side.Close()
			}
			return nil, fmt.Errorf("database file unavailable: %w", err)
		}
		if err := db.Close(); err != nil {
			if side != nil {
				_ = unlockFile(side)
				_ = side.Close()
			}
			return nil, fmt.Errorf("database file unavailable")
		}
		db = nil
	} else if !errors.Is(dbStatErr, os.ErrNotExist) {
		if side != nil {
			_ = unlockFile(side)
			_ = side.Close()
		}
		return nil, fmt.Errorf("database file unavailable")
	}
	return &databaseFileLock{path: path, side: side}, nil
}
