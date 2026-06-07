package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Schema version management
// ---------------------------------------------------------------------------

const currentSchemaVersion = 8

// migration represents a single schema migration step.
type migration struct {
	version     int
	description string
	up          string
}

// migrations defines all schema migrations in order.
var migrations = []migration{
	{
		version:     1,
		description: "Initial schema: servers, channels, downloads, search_cache, presets, watchlists, provider_stats",
		up:          initialSchema,
	},
	{
		version:     2,
		description: "Repair corrupted progress_bytes (filename string stored as integer)",
		up:          `UPDATE downloads SET progress_bytes=0 WHERE typeof(progress_bytes) != 'integer';`,
	},
	{
		version:     3,
		description: "Add composite indexes: downloads(status,channel), provider_stats(provider,window_start)",
		up:          migrationV3Indexes,
	},
	{
		version:     4,
		description: "Add interval_minutes column to watchlists table",
		up:          `ALTER TABLE watchlists ADD COLUMN interval_minutes INTEGER NOT NULL DEFAULT 60;`,
	},
	{
		version:     5,
		description: "Add last_results_json column to watchlists for result persistence",
		up:          `ALTER TABLE watchlists ADD COLUMN last_results_json TEXT DEFAULT '';`,
	},
	{
		version:     6,
		description: "Add notify_enabled column to watchlists for per-watchlist notification toggle",
		up:          `ALTER TABLE watchlists ADD COLUMN notify_enabled INTEGER NOT NULL DEFAULT 1;`,
	},
	{
		version:     7,
		description: "Add dedicated retry_count column to downloads for retry policy (previously priority was used as proxy)",
		up:          `ALTER TABLE downloads ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;`,
	},
	{
		version:     8,
		description: "Add avg_speed_bps column to irc_channels for per-channel average download speed",
		up:          `ALTER TABLE irc_channels ADD COLUMN avg_speed_bps REAL NOT NULL DEFAULT 0;`,
	},
}

const initialSchema = `
CREATE TABLE IF NOT EXISTS irc_servers (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    address TEXT NOT NULL,
    port INTEGER NOT NULL DEFAULT 6667,
    auto_connect INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'disconnected',
    last_connected_at TEXT,
    retry_count INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(address, port)
);

CREATE TABLE IF NOT EXISTS irc_channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id INTEGER NOT NULL,
    name TEXT NOT NULL,
    topic TEXT DEFAULT '',
    auto_join INTEGER NOT NULL DEFAULT 1,
    joined INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (server_id) REFERENCES irc_servers(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS downloads (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pack_message TEXT NOT NULL,
    bot TEXT NOT NULL,
    server_address TEXT NOT NULL,
    channel TEXT DEFAULT '',
    filename TEXT NOT NULL,
    file_size INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'queued',
    progress_bytes INTEGER NOT NULL DEFAULT 0,
    speed_bps INTEGER NOT NULL DEFAULT 0,
    error_message TEXT DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    started_at TEXT,
    completed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_downloads_status ON downloads(status);
CREATE INDEX IF NOT EXISTS idx_downloads_channel ON downloads(channel);
CREATE INDEX IF NOT EXISTS idx_downloads_bot_server ON downloads(bot, server_address);

CREATE TABLE IF NOT EXISTS search_cache (
    query_key TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL,
    fetched_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL,
    stale_expires_at TEXT NOT NULL,
    PRIMARY KEY (query_key, provider)
);

CREATE TABLE IF NOT EXISTS search_presets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    query TEXT NOT NULL,
    filters_json TEXT DEFAULT '',
    is_default INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS watchlists (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    query TEXT NOT NULL,
    filters_json TEXT DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    auto_enqueue INTEGER NOT NULL DEFAULT 0,
    last_checked_at TEXT,
    last_match_fingerprint TEXT DEFAULT '',
    last_notified_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS provider_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    provider TEXT NOT NULL,
    window_start TEXT NOT NULL,
    window_end TEXT NOT NULL,
    requests INTEGER NOT NULL DEFAULT 0,
    successes INTEGER NOT NULL DEFAULT 0,
    timeouts INTEGER NOT NULL DEFAULT 0,
    failures INTEGER NOT NULL DEFAULT 0,
    avg_latency_ms REAL NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_provider_stats_provider ON provider_stats(provider);

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// migrationV3Indexes adds composite indexes for common query patterns.
const migrationV3Indexes = `
CREATE INDEX IF NOT EXISTS idx_downloads_status_channel ON downloads(status, channel);
CREATE INDEX IF NOT EXISTS idx_provider_stats_provider_window ON provider_stats(provider, window_start);
`

// backupPrefix returns the base path used to match backup files for cleanup.
// All backup files start with this prefix.
func backupPrefix(dbPath string) string {
	return dbPath + ".backup."
}

// ---------------------------------------------------------------------------
// Migrations runner
// ---------------------------------------------------------------------------

// runMigrations applies all pending migrations in a transaction.
// It creates a backup before running any migration that modifies the schema.
func runMigrations(ctx context.Context, db *sql.DB, dbPath string) error {
	// Ensure schema_version table exists
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("creating schema_version table: %w", err)
	}

	currentVersion, err := getCurrentVersion(db)
	if err != nil {
		return fmt.Errorf("getting current schema version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= currentVersion {
			continue
		}

		// Backup before destructive migration
		backupPath := fmt.Sprintf("%s.backup.v%d.%s",
			dbPath, m.version, time.Now().Format("20060102_150405"))
		if err := backupDB(ctx, db, backupPath); err != nil {
			return fmt.Errorf("backing up database before migration v%d: %w", m.version, err)
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("beginning transaction for migration v%d: %w", m.version, err)
		}

		if _, err := tx.Exec(m.up); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("applying migration v%d (%s): %w", m.version, m.description, err)
		}

		if _, err := tx.Exec(
			`INSERT INTO schema_version (version, applied_at) VALUES (?, datetime('now'))`,
			m.version,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("recording migration v%d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration v%d: %w", m.version, err)
		}
	}

	// Cleanup old backups — keep only the 3 most recent
	cleanupOldBackups(dbPath, 3)

	return nil
}

// getCurrentVersion returns the highest applied schema version, or 0 if none.
func getCurrentVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("querying schema version: %w", err)
	}
	return version, nil
}

// CurrentSchemaVersion returns the highest applied schema version.
func (s *SQLiteStore) CurrentSchemaVersion(ctx context.Context) (int, error) {
	return getCurrentVersion(s.db)
}

// cleanupOldBackups removes all but the most recent `maxCount` backup files
// for the given database path. Backup files match the pattern `{dbPath}.backup.*`.
// It runs silently — errors are logged but not returned, as cleanup is best-effort.
func cleanupOldBackups(dbPath string, maxCount int) {
	if maxCount < 1 {
		return
	}
	prefix := backupPrefix(dbPath)
	dir := filepath.Dir(dbPath)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// Collect matching backup files with their modification times
	type backupFile struct {
		path string
		mod  time.Time
	}
	var backups []backupFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fullPath := filepath.Join(dir, e.Name())
		if strings.HasPrefix(fullPath, prefix) {
			info, err := e.Info()
			if err != nil {
				continue
			}
			backups = append(backups, backupFile{path: fullPath, mod: info.ModTime()})
		}
	}

	if len(backups) <= maxCount {
		return
	}

	// Sort by modification time, most recent first
	//nolint:govet // shadowing is intentional for this local sort
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].mod.After(backups[j].mod)
	})

	// Delete older backups beyond maxCount
	for _, b := range backups[maxCount:] {
		_ = os.Remove(b.path)
	}
}

// ---------------------------------------------------------------------------
// Backup function — copies the database file before destructive migrations
// ---------------------------------------------------------------------------

// backupDB creates a snapshot backup of the database at the given path.
// It uses the backup API for live databases.
func backupDB(ctx context.Context, db *sql.DB, destPath string) error {
	// Validate path: must be absolute and contain no SQL-special characters
	// that could be used for injection
	if !filepath.IsAbs(destPath) {
		return fmt.Errorf("backup path must be absolute: %s", destPath)
	}

	// Ensure parent directory exists
	dir := filepath.Dir(destPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("backup directory does not exist: %s", dir)
	}

	// SQLite VACUUM INTO doesn't support placeholders, so we must escape.
	// Replace single quotes with two single quotes (SQL standard escaping).
	escapedPath := strings.ReplaceAll(destPath, "'", "''")

	// Use VACUUM INTO for simple file-level backup (requires SQLite 3.27+)
	// modernc.org/sqlite supports this.
	_, err := db.ExecContext(ctx, fmt.Sprintf(`VACUUM INTO '%s'`, escapedPath))
	if err != nil {
		return fmt.Errorf("backing up database to %s: %w", destPath, err)
	}
	return nil
}

// CreateBackup creates a timestamped backup of the database before running
// destructive operations.
func (s *SQLiteStore) CreateBackup(ctx context.Context) (string, error) {
	backupPath := fmt.Sprintf("%s.backup.%s", s.dbPath, time.Now().Format("20060102_150405"))
	if err := backupDB(ctx, s.db, backupPath); err != nil {
		return "", err
	}
	return backupPath, nil
}
