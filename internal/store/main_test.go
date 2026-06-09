package store

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"xdcc_server/internal/logging"
)

// templateDBPath is the path to a pre-migrated SQLite database template. It is
// created once in TestMain and copied by each test via newTestStore, avoiding
// the expensive Migrate() call (with VACUUM INTO backups) for every test.
var templateDBPath string

func TestMain(m *testing.M) {
	// Create a template database with all migrations applied.
	dir, err := os.MkdirTemp("", "xdcc-store-test-main-*")
	if err != nil {
		panic("cannot create temp dir for template DB: " + err.Error())
	}
	defer os.RemoveAll(dir)

	dbPath := filepath.Join(dir, "template.db")
	testLog := logging.New(logging.LevelDebug, "", 0)
	s, err := NewSQLiteStore(dbPath, 2000, testLog)
	if err != nil {
		panic("cannot create template store: " + err.Error())
	}
	if err := s.Migrate(context.Background()); err != nil {
		s.Close()
		panic("cannot migrate template store: " + err.Error())
	}
	s.Close()

	// Apply speed PRAGMAs to the template connection (connection-level settings
	// are NOT persisted in the file — newTestStore re-applies them per test).
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

// copyFile copies a file from src to dst.
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

	if _, err := io.Copy(d, s); err != nil {
		return err
	}
	return d.Sync()
}
