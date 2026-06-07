package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// ---------------------------------------------------------------------------
// Export — 2.8: Export configuration and state
// ---------------------------------------------------------------------------

// ExportData holds a complete snapshot of database state for export.
// (Defined in store.go for consistency.)

// ExportDataVersion is the current export format version.
const ExportDataVersion = 1

// ExportData compiles a complete export of all relevant data from the store.
func (s *SQLiteStore) ExportData(ctx context.Context) (*ExportData, error) {
	version, err := getCurrentVersion(s.db)
	if err != nil {
		return nil, fmt.Errorf("getting schema version for export: %w", err)
	}

	servers, err := s.ListServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing servers for export: %w", err)
	}

	var allChannels []ChannelRecord
	for _, srv := range servers {
		channels, err := s.GetChannelsByServer(ctx, srv.ID)
		if err != nil {
			return nil, fmt.Errorf("getting channels for server %d: %w", srv.ID, err)
		}
		allChannels = append(allChannels, channels...)
	}

	// Export downloads that are still relevant (queued, downloading, paused)
	queue, err := s.GetQueue(ctx)
	if err != nil {
		return nil, fmt.Errorf("getting queue for export: %w", err)
	}

	presets, err := s.ListSearchPresets(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing presets for export: %w", err)
	}

	watchlists, err := s.ListWatchlists(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing watchlists for export: %w", err)
	}

	export := &ExportData{
		SchemaVersion: version,
		ExportedAt:    time.Now(),
		Servers:       servers,
		Channels:      allChannels,
		Downloads:     queue,
		SearchPresets: presets,
		Watchlists:    watchlists,
	}

	return export, nil
}

// ExportToFile exports the database state to a JSON file.
func (s *SQLiteStore) ExportToFile(ctx context.Context, path string) error {
	data, err := s.ExportData(ctx)
	if err != nil {
		return err
	}

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling export data: %w", err)
	}

	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return fmt.Errorf("writing export file %s: %w", path, err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Import — 2.8: Import configuration and state
// ---------------------------------------------------------------------------

// ImportData imports previously exported data into the store.
// It validates schema compatibility before importing.
// The entire import runs in a single transaction for atomicity.
func (s *SQLiteStore) ImportData(ctx context.Context, data *ExportData) error {
	if data == nil {
		return fmt.Errorf("import data is nil")
	}

	// Validate schema version compatibility
	if data.SchemaVersion > currentSchemaVersion {
		return fmt.Errorf(
			"export schema version %d is newer than current %d",
			data.SchemaVersion, currentSchemaVersion,
		)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning import transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback is a no-op after Commit

	// Import servers
	for _, srv := range data.Servers {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO irc_servers (address, port, auto_connect, status, retry_count)
			 VALUES (?, ?, ?, ?, ?)`,
			srv.Address, srv.Port, boolToInt(srv.AutoConnect), srv.Status, srv.RetryCount,
		)
		if err != nil {
			return fmt.Errorf("importing server %s:%d: %w", srv.Address, srv.Port, err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("getting new server id for %s:%d: %w", srv.Address, srv.Port, err)
		}

		// Import channels for this server (match by old server ID)
		for _, ch := range data.Channels {
			if ch.ServerID == srv.ID {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO irc_channels (server_id, name, topic, auto_join, joined, avg_speed_bps)
					 VALUES (?, ?, ?, ?, ?, ?)`,
					newID, ch.Name, ch.Topic, boolToInt(ch.AutoJoin), boolToInt(ch.Joined), ch.AvgSpeedBPS,
				); err != nil {
					return fmt.Errorf("importing channel %s for server %s: %w", ch.Name, srv.Address, err)
				}
			}
		}
	}

	// Import downloads (reset to queued status)
	for _, d := range data.Downloads {
		d.Status = DownloadStatusQueued
		d.ProgressBytes = 0
		d.SpeedBPS = 0
		d.StartedAt = nil
		d.CompletedAt = nil
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO downloads (pack_message, bot, server_address, channel, filename, file_size, status, retry_count, priority, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, 'queued', 0, ?, datetime('now'))`,
			d.PackMessage, d.Bot, d.ServerAddress, d.Channel, d.Filename, d.FileSize, d.Priority,
		); err != nil {
			return fmt.Errorf("importing download %s from %s: %w", d.Filename, d.Bot, err)
		}
	}

	// Import presets
	for _, p := range data.SearchPresets {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO search_presets (name, query, filters_json, is_default)
			 VALUES (?, ?, ?, ?)`,
			p.Name, p.Query, p.FiltersJSON, boolToInt(p.IsDefault),
		); err != nil {
			return fmt.Errorf("importing preset %s: %w", p.Name, err)
		}
	}

	// Import watchlists
	for _, w := range data.Watchlists {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO watchlists (name, query, interval_minutes, filters_json, enabled, auto_enqueue, last_results_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			w.Name, w.Query, w.IntervalMinutes, w.FiltersJSON,
			boolToInt(w.Enabled), boolToInt(w.AutoEnqueue), string(w.LastResultsJSON),
		); err != nil {
			return fmt.Errorf("importing watchlist %s: %w", w.Name, err)
		}
	}

	return tx.Commit()
}

// ImportFromFile imports state from a JSON export file.
func (s *SQLiteStore) ImportFromFile(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading import file %s: %w", path, err)
	}

	var export ExportData
	if err := json.Unmarshal(data, &export); err != nil {
		return fmt.Errorf("parsing import file: %w", err)
	}

	return s.ImportData(ctx, &export)
}

// ---------------------------------------------------------------------------
// Database backup — 2.8
// ---------------------------------------------------------------------------

// BackupDatabase creates a snapshot backup of the SQLite database to destPath.
// Uses SQLite's VACUUM INTO for a consistent snapshot of a live database.
func (s *SQLiteStore) BackupDatabase(ctx context.Context, destPath string) error {
	return backupDB(ctx, s.db, destPath)
}
