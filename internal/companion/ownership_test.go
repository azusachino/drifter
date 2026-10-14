package companion

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"
)

const ownershipHelperEnv = "DRIFTER_OWNERSHIP_HELPER"
const ownershipPauseDriver = "sqlite3_ownership_pause_test"

var (
	registerOwnershipPauseDriver sync.Once
	ownershipPauseState          struct {
		sync.Mutex
		entered chan struct{}
		release chan struct{}
	}
)

func configureOwnershipPauseDriver() {
	registerOwnershipPauseDriver.Do(func() {
		sql.Register(ownershipPauseDriver, &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
			return conn.RegisterFunc("hold_mapping_write", func() int64 {
				ownershipPauseState.Lock()
				entered, release := ownershipPauseState.entered, ownershipPauseState.release
				ownershipPauseState.Unlock()
				if entered != nil && release != nil {
					close(entered)
					<-release
				}
				return 1
			}, false)
		}})
	})
}

func TestOwnershipSubprocessHelper(t *testing.T) {
	mode := os.Getenv(ownershipHelperEnv)
	if mode == "" {
		return
	}
	path := os.Getenv("DRIFTER_OWNERSHIP_DB")
	owner, err := AcquireDatabaseOwnership(path)
	if errors.Is(err, ErrDatabaseOwned) {
		fmt.Println("BUSY")
		return
	}
	if err != nil {
		t.Fatalf("acquire ownership: %v", err)
	}
	if mode == "try" {
		owner.Close()
		fmt.Println("ACQUIRED")
		return
	}
	if mode != "hold" {
		t.Fatalf("unknown helper mode %q", mode)
	}
	fmt.Println("READY")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	if err := owner.Close(); err != nil {
		t.Fatalf("release ownership: %v", err)
	}
}

func TestDaemonOwnershipIsExclusiveAndReleasedOnProcessDeath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnershipSubprocessHelper$")
	cmd.Env = append(os.Environ(), ownershipHelperEnv+"=hold", "DRIFTER_OWNERSHIP_DB="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "READY" {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("owner subprocess did not acquire state: %q %v", line, err)
	}

	competitor := exec.Command(os.Args[0], "-test.run=^TestOwnershipSubprocessHelper$")
	competitor.Env = append(os.Environ(), ownershipHelperEnv+"=try", "DRIFTER_OWNERSHIP_DB="+path)
	output, err := competitor.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "BUSY") {
		t.Fatalf("second process ownership result: %q %v", output, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	_ = cmd.Wait()
	if _, err := os.Stat(path + ".owner.lock"); err != nil {
		t.Fatalf("persistent lock file disappeared: %v", err)
	}
	owner, err := AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatalf("kernel did not release ownership after process death: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("writer could not recover after owner death: %v", err)
	}
	defer store.Close()
	if err := store.Confirm(context.Background(), testMapping(), false); err != nil {
		t.Fatalf("write did not recover after owner death: %v", err)
	}
}

func TestDatabaseOwnershipCanonicalizesSymlinkAndHardlinkAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.sqlite")
	owner, err := AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	aliasDir := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, aliasDir); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{
		filepath.Join(aliasDir, "state.sqlite"),
		filepath.Join(dir, "nested", "..", "state.sqlite"),
	} {
		if _, err := AcquireDatabaseOwnership(alias); !errors.Is(err, ErrDatabaseOwned) {
			t.Errorf("alias %q did not resolve to active owner: %v", alias, err)
		}
	}
	hardlink := filepath.Join(dir, "hardlink.sqlite")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireDatabaseOwnership(hardlink); err == nil || !strings.Contains(err.Error(), "hard-link aliases") {
		t.Fatalf("hardlink alias was not refused: %v", err)
	}
}

func TestStoreMutationGuardChecksOwnershipAtWriteTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner, err := AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := store.Confirm(context.Background(), testMapping(), false); !errors.Is(err, ErrDatabaseOwned) {
		t.Fatalf("open store bypassed daemon ownership: %v", err)
	}
	if err := store.Unlink(context.Background(), "weread", "source", "123", "target"); !errors.Is(err, ErrDatabaseOwned) {
		t.Fatalf("open store unlink bypassed daemon ownership: %v", err)
	}
}

func TestOwnerCloseWaitsForInFlightOwnedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	setup, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(`CREATE TRIGGER pause_mapping_write BEFORE INSERT ON mappings BEGIN SELECT hold_mapping_write(); END`); err != nil {
		t.Fatal(err)
	}
	if err := setup.Close(); err != nil {
		t.Fatal(err)
	}

	owner, err := AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	store, err = OpenOwnedStore(path, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}

	configureOwnershipPauseDriver()
	entered, releaseWrite := make(chan struct{}), make(chan struct{})
	ownershipPauseState.Lock()
	ownershipPauseState.entered, ownershipPauseState.release = entered, releaseWrite
	ownershipPauseState.Unlock()
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_busy_timeout=5000&_journal_mode=DELETE"}).String()
	store.db, err = sql.Open(ownershipPauseDriver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	store.db.SetMaxOpenConns(1)
	defer store.Close()
	defer func() {
		select {
		case <-releaseWrite:
		default:
			close(releaseWrite)
		}
		ownershipPauseState.Lock()
		ownershipPauseState.entered, ownershipPauseState.release = nil, nil
		ownershipPauseState.Unlock()
	}()

	writeDone := make(chan error, 1)
	go func() { writeDone <- store.Confirm(context.Background(), testMapping(), false) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("owned write did not enter the blocking SQLite trigger")
	}

	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- owner.Close()
	}()
	<-closeStarted
	secondCloseStarted := make(chan struct{})
	secondCloseDone := make(chan error, 1)
	go func() {
		close(secondCloseStarted)
		secondCloseDone <- owner.Close()
	}()
	<-secondCloseStarted
	deadline := time.After(5 * time.Second)
	for !owner.closing.Load() {
		select {
		case <-deadline:
			t.Fatal("owner close did not begin")
		default:
			runtime.Gosched()
		}
	}
	for owner.mu.TryRLock() {
		owner.mu.RUnlock()
		select {
		case err := <-closeDone:
			t.Fatalf("owner closed before protected write finished: %v", err)
		case <-deadline:
			t.Fatal("owner close did not queue behind the in-flight write")
		default:
			runtime.Gosched()
		}
	}
	competing, err := AcquireDatabaseOwnership(path)
	if err == nil {
		_ = competing.Close()
		t.Fatal("second owner acquired state during a protected write")
	}
	if !errors.Is(err, ErrDatabaseOwned) {
		t.Fatalf("second owner check during protected write returned wrong error: %v", err)
	}
	select {
	case err := <-closeDone:
		t.Fatalf("owner closed before protected write finished: %v", err)
	default:
	}

	close(releaseWrite)
	if err := <-writeDone; err != nil {
		t.Fatalf("protected write failed: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("owner close failed after write completed: %v", err)
	}
	if err := <-secondCloseDone; err != nil {
		t.Fatalf("concurrent idempotent close failed: %v", err)
	}
	stale := testMapping()
	stale.BookID = "456"
	if err := store.Confirm(context.Background(), stale, false); err == nil {
		t.Fatal("store mutated through a closed ownership capability")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatalf("second owner could not acquire after protected write and close: %v", err)
	}
	defer second.Close()
	verified, err := OpenOwnedStore(path, second)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	rows, err := verified.List(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("protected write did not persist: rows=%d err=%v", len(rows), err)
	}
}

func TestReadOnlyStoreUnderOwnershipDoesNotMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE mappings (
		provider TEXT NOT NULL, source_account TEXT NOT NULL, book_id TEXT NOT NULL,
		target_account TEXT NOT NULL, work_id INTEGER NOT NULL, edition_id INTEGER NOT NULL,
		source_title TEXT NOT NULL, source_isbn TEXT NOT NULL,
		PRIMARY KEY(provider, source_account, book_id, target_account))`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	owner, err := AcquireDatabaseOwnership(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := OpenStoreForRead(path); err == nil {
		t.Fatal("read-only fallback silently migrated an old schema")
	}
	check, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var version int
	if err := check.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("owner did not preserve old schema version: %d", version)
	}
}
