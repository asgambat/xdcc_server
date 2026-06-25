package api

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"xdcc_server/internal/logging"
	"xdcc_server/internal/store"
)

// templateDBPath is the path to a pre-migrated SQLite database template. It is
// created once in TestMain and copied by each test via newTestAPI, avoiding
// the expensive Migrate() call (with VACUUM INTO backups) for every test.
var templateDBPath string

func TestMain(m *testing.M) {
	// Create a template database with all migrations applied.
	dir, err := os.MkdirTemp("", "xdcc-api-test-main-*")
	if err != nil {
		panic("cannot create temp dir for template DB: " + err.Error())
	}
	dbPath := filepath.Join(dir, "template.db")
	testLog := logging.New(logging.LevelDebug, "", 0)
	s, err := store.NewSQLiteStore(dbPath, 2000, 3, testLog)
	if err != nil {
		panic("cannot create template store: " + err.Error())
	}
	if err := s.Migrate(context.Background()); err != nil {
		s.Close()
		panic("cannot migrate template store: " + err.Error())
	}
	s.Close()

	// Apply speed PRAGMAs to the template.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		panic("cannot open template for PRAGMAs: " + err.Error())
	}
	if _, err := db.Exec("PRAGMA synchronous=OFF"); err != nil {
		db.Close()
		panic("PRAGMA synchronous: " + err.Error())
	}
	if _, err := db.Exec("PRAGMA journal_mode=MEMORY"); err != nil {
		db.Close()
		panic("PRAGMA journal_mode: " + err.Error())
	}
	db.Close()

	templateDBPath = dbPath

	code := m.Run()

	// Explicit cleanup (no defer — os.Exit skips deferred functions).
	os.RemoveAll(dir)
	os.Exit(code)
}

// copyFile copies a file from src to dst. It skips fsync — tests do not need
// crash-safe durability, only speed. io.Copy allocates its own 32 KiB buffer
// per call, which is safe under concurrent t.Parallel() usage.
func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()

	_, err = io.Copy(d, s)
	return err
}
