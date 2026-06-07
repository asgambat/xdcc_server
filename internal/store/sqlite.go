package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // sqlite driver registration via blank import

	"xdcc_server/internal/logging"
)

// nullTime implements sql.Scanner for SQLite datetime strings.
// Unlike sql.NullString, it stores the parsed time.Time directly,
// avoiding per-row allocations of string intermediates on history queries.
type nullTime struct {
	Time  time.Time
	Valid bool
}

func (nt *nullTime) Scan(value any) error {
	if value == nil {
		nt.Time = time.Time{}
		nt.Valid = false
		return nil
	}
	s, ok := value.(string)
	if !ok {
		return fmt.Errorf("nullTime: expected string, got %T", value)
	}
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		return err
	}
	nt.Time = t
	nt.Valid = true
	return nil
}

// ---------------------------------------------------------------------------
// SQLiteStore
// ---------------------------------------------------------------------------

// SQLiteStore implements the Store interface backed by SQLite.
type SQLiteStore struct {
	db     *sql.DB
	dbPath string
	log    *logging.Logger
}

// NewSQLiteStore creates a new SQLiteStore and runs migrations.
// The log parameter is used for warning messages (e.g. corrupted rows).
// A nil logger is tolerated and falls back to a silent discard.
func NewSQLiteStore(dbPath string, log *logging.Logger) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening SQLite database: %w", err)
	}

	// Configure connection pool — SQLite serializes writes but supports
	// concurrent reads with WAL mode. 3 connections allow up to 2 reads
	// to proceed while a write is in progress.
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(3)

	// Enable WAL mode for better concurrent read performance
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	s := &SQLiteStore{
		db:     db,
		dbPath: dbPath,
		log:    log,
	}

	return s, nil
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB (for advanced use).
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

// DBPath returns the path to the database file.
func (s *SQLiteStore) DBPath() string {
	return s.dbPath
}

// Migrate runs all pending schema migrations.
func (s *SQLiteStore) Migrate(ctx context.Context) error {
	return runMigrations(ctx, s.db, s.dbPath)
}

// =========================================================================
// IRC Servers
// =========================================================================

func (s *SQLiteStore) AddServer(ctx context.Context, srv ServerRecord) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO irc_servers (address, port, auto_connect, status, retry_count)
		 VALUES (?, ?, ?, ?, ?)`,
		srv.Address, srv.Port, boolToInt(srv.AutoConnect), srv.Status, srv.RetryCount,
	)
	if err != nil {
		return 0, fmt.Errorf("adding server: %w", err)
	}
	return res.LastInsertId()
}

func (s *SQLiteStore) GetServer(ctx context.Context, id int64) (*ServerRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, address, port, auto_connect, status, last_connected_at, retry_count, created_at, updated_at
		 FROM irc_servers WHERE id = ?`, id,
	)
	return scanServer(row)
}

func (s *SQLiteStore) ListServers(ctx context.Context) ([]ServerRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, address, port, auto_connect, status, last_connected_at, retry_count, created_at, updated_at
		 FROM irc_servers ORDER BY address, port`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}
	defer rows.Close()

	var servers []ServerRecord
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, *srv)
	}
	if servers == nil {
		servers = []ServerRecord{}
	}
	return servers, rows.Err()
}

func (s *SQLiteStore) UpdateServer(ctx context.Context, srv ServerRecord) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE irc_servers SET address=?, port=?, auto_connect=?, status=?, updated_at=datetime('now')
		 WHERE id=?`,
		srv.Address, srv.Port, boolToInt(srv.AutoConnect), srv.Status, srv.ID,
	)
	return err
}

func (s *SQLiteStore) DeleteServer(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM irc_servers WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) SetServerStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE irc_servers SET status=?, updated_at=datetime('now') WHERE id=?`,
		status, id,
	)
	return err
}

func (s *SQLiteStore) SetServerConnected(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE irc_servers SET status='connected', last_connected_at=datetime('now'), retry_count=0, updated_at=datetime('now') WHERE id=?`,
		id,
	)
	return err
}

func (s *SQLiteStore) IncrementServerRetry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE irc_servers SET retry_count=retry_count+1, status='reconnecting', updated_at=datetime('now') WHERE id=?`,
		id,
	)
	return err
}

// ResetAllServerStatuses resets all server statuses to "disconnected".
// This is called on startup to clear stale "connected"/"reconnecting" statuses
// that may have been persisted from a previous run. Only servers whose status
// is NOT already "disconnected" are updated to avoid unnecessary writes.
func (s *SQLiteStore) ResetAllServerStatuses(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE irc_servers SET status = 'disconnected', updated_at = datetime('now')
		 WHERE status != 'disconnected'`,
	)
	return err
}

// =========================================================================
// IRC Channels
// =========================================================================

func (s *SQLiteStore) AddChannel(ctx context.Context, ch ChannelRecord) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO irc_channels (server_id, name, topic, auto_join, joined)
		 VALUES (?, ?, ?, ?, ?)`,
		ch.ServerID, ch.Name, ch.Topic, boolToInt(ch.AutoJoin), boolToInt(ch.Joined),
	)
	if err != nil {
		return 0, fmt.Errorf("adding channel: %w", err)
	}
	return res.LastInsertId()
}

func (s *SQLiteStore) GetChannelsByServer(ctx context.Context, serverID int64) ([]ChannelRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, server_id, name, topic, auto_join, joined, avg_speed_bps
		 FROM irc_channels WHERE server_id = ? ORDER BY name`, serverID,
	)
	if err != nil {
		return nil, fmt.Errorf("getting channels for server %d: %w", serverID, err)
	}
	defer rows.Close()

	var channels []ChannelRecord
	for rows.Next() {
		var ch ChannelRecord
		if err := rows.Scan(&ch.ID, &ch.ServerID, &ch.Name, &ch.Topic, &ch.AutoJoin, &ch.Joined, &ch.AvgSpeedBPS); err != nil {
			return nil, fmt.Errorf("scanning channel: %w", err)
		}
		channels = append(channels, ch)
	}
	if channels == nil {
		channels = []ChannelRecord{}
	}
	return channels, rows.Err()
}

func (s *SQLiteStore) GetChannelsByServerAndName(ctx context.Context, serverID int64, name string) (*ChannelRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, server_id, name, topic, auto_join, joined, avg_speed_bps
		 FROM irc_channels WHERE server_id = ? AND name = ?`, serverID, name,
	)
	var ch ChannelRecord
	if err := row.Scan(&ch.ID, &ch.ServerID, &ch.Name, &ch.Topic, &ch.AutoJoin, &ch.Joined, &ch.AvgSpeedBPS); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning channel: %w", err)
	}
	return &ch, nil
}

func (s *SQLiteStore) UpdateChannel(ctx context.Context, ch ChannelRecord) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE irc_channels SET name=?, topic=?, auto_join=?, joined=? WHERE id=?`,
		ch.Name, ch.Topic, boolToInt(ch.AutoJoin), boolToInt(ch.Joined), ch.ID,
	)
	return err
}

// UpdateChannelAvgSpeed updates the exponential moving average of download
// speed for a channel. The EMA formula smooths the average over time:
//
//	newAvg = oldAvg * 0.7 + lastSpeed * 0.3
//
// On the first update (avg_speed_bps = 0), the value is set directly to
// avoid the EMA cold-start problem where 0 * 0.7 + speed * 0.3 = 0.3*speed.
func (s *SQLiteStore) UpdateChannelAvgSpeed(ctx context.Context, serverAddress, channelName string, lastSpeedBPS float64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE irc_channels SET avg_speed_bps = CASE
		 WHEN avg_speed_bps = 0 THEN ?
		 ELSE avg_speed_bps * 0.7 + ? * 0.3
		 END
		 WHERE name=? AND server_id IN (SELECT id FROM irc_servers WHERE address=?)`,
		lastSpeedBPS, lastSpeedBPS, channelName, serverAddress,
	)
	return err
}

func (s *SQLiteStore) DeleteChannel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM irc_channels WHERE id = ?`, id)
	return err
}

func (s *SQLiteStore) SetChannelJoined(ctx context.Context, id int64, joined bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE irc_channels SET joined=? WHERE id=?`, boolToInt(joined), id)
	return err
}

func (s *SQLiteStore) UpdateChannelTopic(ctx context.Context, id int64, topic string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE irc_channels SET topic=? WHERE id=?`, topic, id)
	return err
}

func (s *SQLiteStore) GetAutoJoinChannels(ctx context.Context) ([]ChannelRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ch.id, ch.server_id, ch.name, ch.topic, ch.auto_join, ch.joined, ch.avg_speed_bps
		 FROM irc_channels ch
		 JOIN irc_servers srv ON srv.id = ch.server_id
		 WHERE ch.auto_join = 1 AND srv.auto_connect = 1
		 ORDER BY ch.server_id, ch.name`,
	)
	if err != nil {
		return nil, fmt.Errorf("getting auto-join channels: %w", err)
	}
	defer rows.Close()

	var channels []ChannelRecord
	for rows.Next() {
		var ch ChannelRecord
		if err := rows.Scan(&ch.ID, &ch.ServerID, &ch.Name, &ch.Topic, &ch.AutoJoin, &ch.Joined, &ch.AvgSpeedBPS); err != nil {
			return nil, fmt.Errorf("scanning channel: %w", err)
		}
		channels = append(channels, ch)
	}
	if channels == nil {
		channels = []ChannelRecord{}
	}
	return channels, rows.Err()
}

// =========================================================================
// Downloads
// =========================================================================

func (s *SQLiteStore) EnqueueDownload(ctx context.Context, d DownloadRecord) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO downloads (pack_message, bot, server_address, channel, filename, file_size, status, retry_count, priority, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'queued', 0, ?, datetime('now'))`,
		d.PackMessage, d.Bot, d.ServerAddress, d.Channel, d.Filename, d.FileSize, d.Priority,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueueing download: %w", err)
	}
	return res.LastInsertId()
}

func (s *SQLiteStore) GetDownload(ctx context.Context, id int64) (*DownloadRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, pack_message, bot, server_address, channel, filename, file_size,
		        status, progress_bytes, speed_bps, error_message, retry_count, priority,
		        created_at, started_at, completed_at
		 FROM downloads WHERE id = ?`, id,
	)
	return scanDownload(row)
}

func (s *SQLiteStore) GetQueue(ctx context.Context) ([]DownloadRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, pack_message, bot, server_address, channel, filename, file_size,
		        status, progress_bytes, speed_bps, error_message, retry_count, priority,
		        created_at, started_at, completed_at
		 FROM downloads WHERE status IN ('queued', 'downloading', 'paused')
		 ORDER BY priority ASC, created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("getting queue: %w", err)
	}
	defer rows.Close()
	return s.scanDownloads(rows)
}

func (s *SQLiteStore) GetQueueByChannel(ctx context.Context, channel string) ([]DownloadRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, pack_message, bot, server_address, channel, filename, file_size,
		        status, progress_bytes, speed_bps, error_message, retry_count, priority,
		        created_at, started_at, completed_at
		 FROM downloads WHERE channel = ? AND status IN ('queued', 'downloading', 'paused')
		 ORDER BY priority ASC, created_at ASC`, channel,
	)
	if err != nil {
		return nil, fmt.Errorf("getting queue for channel %s: %w", channel, err)
	}
	defer rows.Close()
	return s.scanDownloads(rows)
}

func (s *SQLiteStore) GetActiveDownloads(ctx context.Context) ([]DownloadRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, pack_message, bot, server_address, channel, filename, file_size,
		        status, progress_bytes, speed_bps, error_message, retry_count, priority,
		        created_at, started_at, completed_at
		 FROM downloads WHERE status = 'downloading'
		 ORDER BY priority ASC, created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("getting active downloads: %w", err)
	}
	defer rows.Close()
	return s.scanDownloads(rows)
}

func (s *SQLiteStore) GetPendingByChannel(ctx context.Context, channel string) ([]DownloadRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, pack_message, bot, server_address, channel, filename, file_size,
		        status, progress_bytes, speed_bps, error_message, retry_count, priority,
		        created_at, started_at, completed_at
		 FROM downloads WHERE channel = ? AND status = 'queued'
		 ORDER BY priority ASC, created_at ASC`, channel,
	)
	if err != nil {
		return nil, fmt.Errorf("getting pending downloads for channel %s: %w", channel, err)
	}
	defer rows.Close()
	return s.scanDownloads(rows)
}

func (s *SQLiteStore) UpdateDownloadProgress(ctx context.Context, id, progressBytes, speedBPS int64) error {
	// Note: status is NOT set here — MarkDownloadStarted already sets it before
	// progress callbacks begin. This prevents a race where a concurrent
	// PauseDownload/RemoveDownload changes the status, only to have this
	// progress callback overwrite it back to 'downloading'.
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET progress_bytes=?, speed_bps=? WHERE id=?`,
		progressBytes, speedBPS, id,
	)
	if err != nil {
		return fmt.Errorf("updating download progress for id %d: %w", id, err)
	}
	return nil
}

func (s *SQLiteStore) MarkDownloadStarted(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status='downloading', started_at=datetime('now') WHERE id=?`, id,
	)
	return err
}

// MarkDownloadCompleted marks a download as completed and updates filename/file_size
// with values discovered during download (e.g. from bot notice). Pass empty string
// and 0 if no metadata was discovered.
func (s *SQLiteStore) MarkDownloadCompleted(ctx context.Context, id int64, filename string, fileSize int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status='completed', completed_at=datetime('now'),
		 filename=COALESCE(NULLIF(?, ''), filename),
		 progress_bytes=COALESCE(NULLIF(?, ''), progress_bytes),
		 file_size=CASE WHEN ? > 0 THEN ? ELSE file_size END
		 WHERE id=?`,
		filename, fileSize, fileSize, fileSize, id,
	)
	return err
}

func (s *SQLiteStore) MarkDownloadSkipped(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status='skipped_existing' WHERE id=? AND status IN ('downloading','queued')`,
		id,
	)
	return err
}

func (s *SQLiteStore) MarkDownloadFailed(ctx context.Context, id int64, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status='failed', error_message=?, completed_at=datetime('now') WHERE id=?`,
		errMsg, id,
	)
	return err
}

func (s *SQLiteStore) MarkDownloadPaused(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status='paused' WHERE id=? AND status IN ('queued', 'downloading')`, id,
	)
	return err
}

func (s *SQLiteStore) MarkDownloadRetry(ctx context.Context, id int64, newStatus string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status=?, error_message='' WHERE id=?`, newStatus, id,
	)
	return err
}

func (s *SQLiteStore) DeleteDownload(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM downloads WHERE id=?`, id)
	return err
}

func (s *SQLiteStore) RetryDownload(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status='queued', progress_bytes=0, error_message='', completed_at=NULL WHERE id=? AND status IN ('failed', 'paused', 'completed', 'skipped_existing')`,
		id,
	)
	return err
}

func (s *SQLiteStore) RequeueDownload(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status='queued', progress_bytes=0, error_message='' WHERE id=?`,
		id,
	)
	return err
}

func (s *SQLiteStore) SetDownloadPriority(ctx context.Context, id int64, priority int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET priority=? WHERE id=?`, priority, id)
	return err
}

// IncrementDownloadRetry atomically increments the retry_count for a download.
// This is used by handleFallback to track auto-retry attempts independently
// of the queue-ordering priority field.
func (s *SQLiteStore) IncrementDownloadRetry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET retry_count=retry_count+1 WHERE id=?`,
		id,
	)
	return err
}

// UpdateDownloadMetadata updates the filename and/or file_size for a download.
// This is called when the bot notice reveals the actual filename/size mid-download.
func (s *SQLiteStore) UpdateDownloadMetadata(ctx context.Context, id int64, filename string, fileSize int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET filename=COALESCE(NULLIF(?, ''), filename),
		 file_size=CASE WHEN ? > 0 THEN ? ELSE file_size END
		 WHERE id=?`,
		filename, fileSize, fileSize, id,
	)
	return err
}

func (s *SQLiteStore) GetTotalDownloadedBytes(ctx context.Context) (int64, error) {
	var total sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT SUM(progress_bytes) FROM downloads`,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("getting total downloaded bytes: %w", err)
	}
	if total.Valid {
		return total.Int64, nil
	}
	return 0, nil
}

func (s *SQLiteStore) GetDownloadHistory(ctx context.Context, page, pageSize int, filter HistoryFilter) ([]DownloadRecord, int, error) {
	whereClauses := []string{}
	args := []any{}

	// Default status filter if none provided
	if len(filter.StatusList) > 0 {
		placeholders := make([]string, len(filter.StatusList))
		for i, st := range filter.StatusList {
			placeholders[i] = "?"
			args = append(args, st)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("status IN (%s)", strings.Join(placeholders, ",")))
	} else {
		whereClauses = append(whereClauses, "status IN ('completed', 'failed', 'skipped_existing')")
	}

	if filter.Filename != "" {
		whereClauses = append(whereClauses, "filename LIKE ?")
		args = append(args, "%"+filter.Filename+"%")
	}
	if filter.Bot != "" {
		whereClauses = append(whereClauses, "bot LIKE ?")
		args = append(args, "%"+filter.Bot+"%")
	}
	if filter.MinBytes > 0 {
		whereClauses = append(whereClauses, "file_size >= ?")
		args = append(args, filter.MinBytes)
	}
	if filter.MaxBytes > 0 {
		whereClauses = append(whereClauses, "file_size <= ?")
		args = append(args, filter.MaxBytes)
	}
	if filter.DateFrom != "" {
		whereClauses = append(whereClauses, "date(completed_at) >= ?")
		args = append(args, filter.DateFrom)
	}
	if filter.DateTo != "" {
		whereClauses = append(whereClauses, "date(completed_at) <= ?")
		args = append(args, filter.DateTo)
	}

	whereSQL := strings.Join(whereClauses, " AND ")

	// Count total
	countSQL := "SELECT COUNT(*) FROM downloads WHERE " + whereSQL
	var total int
	err := s.db.QueryRowContext(ctx, countSQL, args...).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("counting download history: %w", err)
	}

	offset := (page - 1) * pageSize
	//nolint:gosec // whereSQL is built from trusted internal constants only (not user input), safe
	querySQL := `SELECT id, pack_message, bot, server_address, channel, filename, file_size,
	        status, progress_bytes, speed_bps, error_message, retry_count, priority,
	        created_at, started_at, completed_at
	 FROM downloads WHERE ` + whereSQL + `
	 ORDER BY completed_at DESC, created_at DESC
	 LIMIT ? OFFSET ?`
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, pageSize, offset)

	rows, err := s.db.QueryContext(ctx, querySQL, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("getting download history: %w", err)
	}
	defer rows.Close()

	downloads, err := s.scanDownloads(rows)
	if err != nil {
		return nil, 0, err
	}
	return downloads, total, nil
}

func (s *SQLiteStore) BulkActionDownloads(ctx context.Context, ids []int64, action string) (map[int64]string, error) {
	results := make(map[int64]string)
	for _, id := range ids {
		var err error
		switch strings.ToLower(action) {
		case "pause":
			err = s.MarkDownloadPaused(ctx, id)
		case "resume":
			err = s.RetryDownload(ctx, id)
		case "remove":
			err = s.DeleteDownload(ctx, id)
		default:
			results[id] = fmt.Sprintf("unknown action: %s", action)
			continue
		}
		if err != nil {
			results[id] = err.Error()
		} else {
			results[id] = "success"
		}
	}
	return results, nil
}

func (s *SQLiteStore) FindDuplicateDownload(ctx context.Context, bot, serverAddress string, packNumber int) (*DownloadRecord, error) {
	// Use exact pack message match to avoid LIKE matching wrong pack numbers
	// (e.g. '#1' matching '#10', '#11', etc.). Match both the full message and
	// messages that end with the exact pack reference (e.g. 'xdcc send #42').
	packExact := fmt.Sprintf("xdcc send #%d", packNumber)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, pack_message, bot, server_address, channel, filename, file_size,
		        status, progress_bytes, speed_bps, error_message, retry_count, priority,
		        created_at, started_at, completed_at
		 FROM downloads
		 WHERE bot = ? AND server_address = ? AND (pack_message = ? OR pack_message = ?)
		 ORDER BY created_at DESC LIMIT 1`,
		bot, serverAddress, packExact, "/msg "+bot+" "+packExact,
	)
	return scanDownload(row)
}

func (s *SQLiteStore) FilenamesExist(ctx context.Context, filenames []string) (map[string]bool, error) {
	if len(filenames) == 0 {
		return map[string]bool{}, nil
	}

	// Deduplicate input filenames
	unique := make([]string, 0, len(filenames))
	seen := make(map[string]struct{}, len(filenames))
	for _, fn := range filenames {
		if _, ok := seen[fn]; !ok {
			seen[fn] = struct{}{}
			unique = append(unique, fn)
		}
	}

	// Build placeholders for IN clause
	placeholders := make([]string, len(unique))
	args := make([]any, len(unique))
	for i, fn := range unique {
		placeholders[i] = "?"
		args[i] = fn
	}

	// Check both history (completed/failed/skipped) and active (queued/downloading/paused) downloads.
	// Use LOWER() for case-insensitive comparison — consistent with computeFingerprint.
	lowerPlaceholders := make([]string, len(unique))
	for i := range unique {
		lowerPlaceholders[i] = "LOWER(?) = LOWER(filename)"
	}
	//nolint:gosec // lowerPlaceholders are hardcoded literals, not user input
	query := fmt.Sprintf(
		`SELECT DISTINCT LOWER(filename) FROM downloads
		 WHERE (%s)
		   AND status IN ('completed', 'failed', 'skipped_existing', 'queued', 'downloading', 'paused')`,
		strings.Join(lowerPlaceholders, " OR "),
	)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("checking existing filenames: %w", err)
	}
	defer rows.Close()

	result := make(map[string]bool, len(unique))
	// Default all to false (not existing)
	for _, fn := range unique {
		result[fn] = false
	}

	for rows.Next() {
		var fn string
		if err := rows.Scan(&fn); err != nil {
			return nil, fmt.Errorf("scanning existing filename: %w", err)
		}
		result[fn] = true
	}

	return result, rows.Err()
}

func (s *SQLiteStore) GetDownloadByBotMessage(ctx context.Context, bot, packMessage string) (*DownloadRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, pack_message, bot, server_address, channel, filename, file_size,
		        status, progress_bytes, speed_bps, error_message, retry_count, priority,
		        created_at, started_at, completed_at
		 FROM downloads WHERE bot = ? AND pack_message = ?
		 ORDER BY created_at DESC LIMIT 1`,
		bot, packMessage,
	)
	return scanDownload(row)
}

// =========================================================================
// Search Cache
// =========================================================================

func (s *SQLiteStore) SetSearchCache(ctx context.Context, entry SearchCacheEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO search_cache (query_key, provider, payload_json, fetched_at, expires_at, stale_expires_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		entry.QueryKey, entry.Provider, entry.PayloadJSON,
		entry.FetchedAt.Format(time.RFC3339),
		entry.ExpiresAt.Format(time.RFC3339),
		entry.StaleExpiresAt.Format(time.RFC3339),
	)
	return err
}

func (s *SQLiteStore) GetSearchCache(ctx context.Context, queryKey, provider string) (*SearchCacheEntry, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT query_key, provider, payload_json, fetched_at, expires_at, stale_expires_at
		 FROM search_cache WHERE query_key = ? AND provider = ?`, queryKey, provider,
	)
	var entry SearchCacheEntry
	var fetchedAt, expiresAt, staleExpiresAt string
	if err := row.Scan(&entry.QueryKey, &entry.Provider, &entry.PayloadJSON,
		&fetchedAt, &expiresAt, &staleExpiresAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning search cache: %w", err)
	}
	var err error
	entry.FetchedAt, err = time.Parse(time.RFC3339, fetchedAt)
	if err != nil {
		return nil, err
	}
	entry.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return nil, err
	}
	entry.StaleExpiresAt, err = time.Parse(time.RFC3339, staleExpiresAt)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// GetSearchCacheByQuery returns all cache entries for a given query key in a single query.
// This avoids the need for nested queries which can deadlock on single-connection SQLite.
func (s *SQLiteStore) GetSearchCacheByQuery(ctx context.Context, queryKey string) ([]SearchCacheEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT query_key, provider, payload_json, fetched_at, expires_at, stale_expires_at
		 FROM search_cache WHERE query_key = ?`, queryKey,
	)
	if err != nil {
		return nil, fmt.Errorf("querying search cache by query: %w", err)
	}
	defer rows.Close()

	var entries []SearchCacheEntry
	for rows.Next() {
		var entry SearchCacheEntry
		var fetchedAt, expiresAt, staleExpiresAt string
		if err := rows.Scan(&entry.QueryKey, &entry.Provider, &entry.PayloadJSON,
			&fetchedAt, &expiresAt, &staleExpiresAt); err != nil {
			return nil, fmt.Errorf("scanning search cache row: %w", err)
		}
		entry.FetchedAt, err = time.Parse(time.RFC3339, fetchedAt)
		if err != nil {
			continue
		}
		entry.ExpiresAt, err = time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			continue
		}
		entry.StaleExpiresAt, err = time.Parse(time.RFC3339, staleExpiresAt)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *SQLiteStore) DeleteExpiredSearchCache(ctx context.Context, staleBefore time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM search_cache WHERE stale_expires_at < ?`,
		staleBefore.Format(time.RFC3339),
	)
	return err
}

// CleanupSearchCache removes stale cache entries beyond their stale TTL.
// Returns the number of entries deleted.
func (s *SQLiteStore) CleanupSearchCache(ctx context.Context) (int, error) {
	now := time.Now()
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM search_cache WHERE stale_expires_at < ?`,
		now.Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("cleaning up search cache: %w", err)
	}

	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// =========================================================================
// Search Presets
// =========================================================================

func (s *SQLiteStore) AddSearchPreset(ctx context.Context, p SearchPreset) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO search_presets (name, query, filters_json, is_default)
		 VALUES (?, ?, ?, ?)`,
		p.Name, p.Query, p.FiltersJSON, boolToInt(p.IsDefault),
	)
	if err != nil {
		return 0, fmt.Errorf("adding search preset: %w", err)
	}
	return res.LastInsertId()
}

func (s *SQLiteStore) GetSearchPreset(ctx context.Context, id int64) (*SearchPreset, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, query, filters_json, is_default, created_at, updated_at
		 FROM search_presets WHERE id = ?`, id,
	)
	return scanSearchPreset(row)
}

func (s *SQLiteStore) ListSearchPresets(ctx context.Context) ([]SearchPreset, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, query, filters_json, is_default, created_at, updated_at
		 FROM search_presets ORDER BY is_default DESC, name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing search presets: %w", err)
	}
	defer rows.Close()

	var presets []SearchPreset
	for rows.Next() {
		p, err := scanSearchPresetFromRows(rows)
		if err != nil {
			return nil, err
		}
		presets = append(presets, *p)
	}
	if presets == nil {
		presets = []SearchPreset{}
	}
	return presets, rows.Err()
}

func (s *SQLiteStore) UpdateSearchPreset(ctx context.Context, p SearchPreset) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE search_presets SET name=?, query=?, filters_json=?, is_default=?, updated_at=datetime('now') WHERE id=?`,
		p.Name, p.Query, p.FiltersJSON, boolToInt(p.IsDefault), p.ID,
	)
	return err
}

func (s *SQLiteStore) DeleteSearchPreset(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM search_presets WHERE id=?`, id)
	return err
}

func (s *SQLiteStore) SetDefaultSearchPreset(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // Rollback error on read-only setup is harmless

	// Clear all defaults
	if _, err := tx.ExecContext(ctx, `UPDATE search_presets SET is_default=0`); err != nil {
		return err
	}
	// Set new default
	if _, err := tx.ExecContext(ctx, `UPDATE search_presets SET is_default=1 WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// =========================================================================
// Watchlists
// =========================================================================

func (s *SQLiteStore) AddWatchlist(ctx context.Context, w Watchlist) (int64, error) {
	interval := w.IntervalMinutes
	if interval < 5 {
		interval = 60 // default to 60 minutes, minimum 5
	}
	resultsStr := ""
	if len(w.LastResultsJSON) > 0 {
		resultsStr = string(w.LastResultsJSON)
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO watchlists (name, query, interval_minutes, filters_json, notify_enabled, enabled, auto_enqueue, last_results_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Name, w.Query, interval, w.FiltersJSON, boolToInt(w.NotifyEnabled), boolToInt(w.Enabled), boolToInt(w.AutoEnqueue), resultsStr,
	)
	if err != nil {
		return 0, fmt.Errorf("adding watchlist: %w", err)
	}
	return res.LastInsertId()
}

func (s *SQLiteStore) GetWatchlist(ctx context.Context, id int64) (*Watchlist, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, query, interval_minutes, filters_json, notify_enabled, enabled, auto_enqueue,
		        last_checked_at, last_match_fingerprint, last_notified_at, last_results_json,
		        created_at, updated_at
		 FROM watchlists WHERE id = ?`, id,
	)
	return scanWatchlist(row)
}

func (s *SQLiteStore) ListWatchlists(ctx context.Context) ([]Watchlist, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, query, interval_minutes, filters_json, notify_enabled, enabled, auto_enqueue,
		        last_checked_at, last_match_fingerprint, last_notified_at, last_results_json,
		        created_at, updated_at
		 FROM watchlists ORDER BY name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing watchlists: %w", err)
	}
	defer rows.Close()

	var watchlists []Watchlist
	for rows.Next() {
		w, err := scanWatchlistFromRows(rows)
		if err != nil {
			return nil, err
		}
		watchlists = append(watchlists, *w)
	}
	if watchlists == nil {
		watchlists = []Watchlist{}
	}
	return watchlists, rows.Err()
}

func (s *SQLiteStore) UpdateWatchlist(ctx context.Context, w Watchlist) error {
	interval := w.IntervalMinutes
	if interval < 5 {
		interval = 60
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE watchlists SET name=?, query=?, interval_minutes=?, filters_json=?, notify_enabled=?, enabled=?, auto_enqueue=?, updated_at=datetime('now') WHERE id=?`,
		w.Name, w.Query, interval, w.FiltersJSON, boolToInt(w.NotifyEnabled), boolToInt(w.Enabled), boolToInt(w.AutoEnqueue), w.ID,
	)
	return err
}

func (s *SQLiteStore) DeleteWatchlist(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM watchlists WHERE id=?`, id)
	return err
}

func (s *SQLiteStore) SetWatchlistChecked(ctx context.Context, id int64, fingerprint, resultsJSON string) error {
	// When resultsJSON is empty, preserve the existing last_results_json.
	// This prevents overwriting previous results with an empty array when
	// a watchlist run finds no new packs (e.g. fingerprint unchanged).
	if resultsJSON != "" {
		_, err := s.db.ExecContext(ctx,
			`UPDATE watchlists SET last_checked_at=datetime('now'), last_match_fingerprint=?, last_results_json=? WHERE id=?`,
			fingerprint, resultsJSON, id,
		)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE watchlists SET last_checked_at=datetime('now'), last_match_fingerprint=? WHERE id=?`,
		fingerprint, id,
	)
	return err
}

func (s *SQLiteStore) SetWatchlistNotified(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE watchlists SET last_notified_at=datetime('now') WHERE id=?`, id,
	)
	return err
}

func (s *SQLiteStore) GetEnabledWatchlists(ctx context.Context) ([]Watchlist, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, query, interval_minutes, filters_json, notify_enabled, enabled, auto_enqueue,
		        last_checked_at, last_match_fingerprint, last_notified_at, last_results_json,
		        created_at, updated_at
		 FROM watchlists WHERE enabled = 1 ORDER BY name ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("getting enabled watchlists: %w", err)
	}
	defer rows.Close()

	var watchlists []Watchlist
	for rows.Next() {
		w, err := scanWatchlistFromRows(rows)
		if err != nil {
			return nil, err
		}
		watchlists = append(watchlists, *w)
	}
	if watchlists == nil {
		watchlists = []Watchlist{}
	}
	return watchlists, rows.Err()
}

// =========================================================================
// Provider Stats
// =========================================================================

func (s *SQLiteStore) RecordProviderStats(ctx context.Context, stats ProviderStats) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO provider_stats (provider, window_start, window_end, requests, successes, timeouts, failures, avg_latency_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		stats.Provider,
		stats.WindowStart.Format(time.RFC3339),
		stats.WindowEnd.Format(time.RFC3339),
		stats.Requests, stats.Successes, stats.Timeouts,
		stats.Failures, stats.AvgLatencyMs,
	)
	return err
}

func (s *SQLiteStore) GetProviderStats(ctx context.Context, provider string, since time.Time) ([]ProviderStats, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, window_start, window_end, requests, successes, timeouts, failures, avg_latency_ms, updated_at
		 FROM provider_stats WHERE provider = ? AND window_start >= ?
		 ORDER BY window_start DESC`, provider, since.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("getting provider stats: %w", err)
	}
	defer rows.Close()

	var stats []ProviderStats
	for rows.Next() {
		var st ProviderStats
		var ws, we, updated string
		if err := rows.Scan(&st.Provider, &ws, &we, &st.Requests, &st.Successes,
			&st.Timeouts, &st.Failures, &st.AvgLatencyMs, &updated); err != nil {
			return nil, fmt.Errorf("scanning provider stats: %w", err)
		}
		st.WindowStart, _ = time.Parse(time.RFC3339, ws)
		st.WindowEnd, _ = time.Parse(time.RFC3339, we)
		st.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		stats = append(stats, st)
	}
	if stats == nil {
		stats = []ProviderStats{}
	}
	return stats, rows.Err()
}

func (s *SQLiteStore) GetAllProviderStats(ctx context.Context, since time.Time) (map[string][]ProviderStats, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT provider, window_start, window_end, requests, successes, timeouts, failures, avg_latency_ms, updated_at
		 FROM provider_stats WHERE window_start >= ?
		 ORDER BY provider, window_start DESC`, since.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("getting all provider stats: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]ProviderStats)
	for rows.Next() {
		var st ProviderStats
		var ws, we, updated string
		if err := rows.Scan(&st.Provider, &ws, &we, &st.Requests, &st.Successes,
			&st.Timeouts, &st.Failures, &st.AvgLatencyMs, &updated); err != nil {
			return nil, fmt.Errorf("scanning provider stats: %w", err)
		}
		st.WindowStart, _ = time.Parse(time.RFC3339, ws)
		st.WindowEnd, _ = time.Parse(time.RFC3339, we)
		st.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
		result[st.Provider] = append(result[st.Provider], st)
	}
	return result, rows.Err()
}

// =========================================================================
// Scan helpers
// =========================================================================

func scanServer(row interface{ Scan(...any) error }) (*ServerRecord, error) {
	var srv ServerRecord
	var lastConnected, createdAtStr, updatedAtStr sql.NullString
	var autoConnect int
	err := row.Scan(&srv.ID, &srv.Address, &srv.Port, &autoConnect,
		&srv.Status, &lastConnected, &srv.RetryCount, &createdAtStr, &updatedAtStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning server: %w", err)
	}
	srv.AutoConnect = autoConnect != 0
	if lastConnected.Valid {
		t, err := time.Parse("2006-01-02 15:04:05", lastConnected.String)
		if err == nil {
			srv.LastConnectedAt = &t
		}
	}
	if createdAtStr.Valid {
		srv.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr.String)
	}
	if updatedAtStr.Valid {
		srv.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAtStr.String)
	}
	return &srv, nil
}

func scanDownload(row interface{ Scan(...any) error }) (*DownloadRecord, error) {
	var d DownloadRecord
	var startedAt, completedAt, createdAt nullTime
	err := row.Scan(&d.ID, &d.PackMessage, &d.Bot, &d.ServerAddress, &d.Channel,
		&d.Filename, &d.FileSize, &d.Status, &d.ProgressBytes, &d.SpeedBPS,
		&d.ErrorMessage, &d.RetryCount, &d.Priority, &createdAt, &startedAt, &completedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning download: %w", err)
	}
	if createdAt.Valid {
		d.CreatedAt = createdAt.Time
	}
	if startedAt.Valid {
		d.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		d.CompletedAt = &completedAt.Time
	}
	return &d, nil
}

func (s *SQLiteStore) scanDownloads(rows *sql.Rows) ([]DownloadRecord, error) {
	var downloads []DownloadRecord
	for rows.Next() {
		d, err := s.scanDownloadFromRows(rows)
		if err != nil {
			return nil, err
		}
		if d != nil {
			downloads = append(downloads, *d)
		}
	}
	if downloads == nil {
		downloads = []DownloadRecord{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return downloads, nil
}

func (s *SQLiteStore) scanDownloadFromRows(rows *sql.Rows) (*DownloadRecord, error) {
	var d DownloadRecord
	var startedAt, completedAt, createdAt nullTime
	if err := rows.Scan(&d.ID, &d.PackMessage, &d.Bot, &d.ServerAddress, &d.Channel,
		&d.Filename, &d.FileSize, &d.Status, &d.ProgressBytes, &d.SpeedBPS,
		&d.ErrorMessage, &d.RetryCount, &d.Priority, &createdAt, &startedAt, &completedAt); err != nil {
		// Defensive: if progress_bytes contains a string (corrupted data from old bug),
		// skip this row instead of failing the entire history query.
		if s.log != nil {
			s.log.Warnf("skipping corrupted download row (scan error: %v)", err)
		}
		return nil, nil // Returning nil, nil signals the caller to skip this row
	}
	if createdAt.Valid {
		d.CreatedAt = createdAt.Time
	}
	if startedAt.Valid {
		d.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		d.CompletedAt = &completedAt.Time
	}
	return &d, nil
}

// =========================================================================
// Scan helpers for SearchPreset
// =========================================================================

func scanSearchPreset(row interface{ Scan(...any) error }) (*SearchPreset, error) {
	var p SearchPreset
	var createdAt, updatedAt sql.NullString
	err := row.Scan(&p.ID, &p.Name, &p.Query, &p.FiltersJSON, &p.IsDefault, &createdAt, &updatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning search preset: %w", err)
	}
	if createdAt.Valid {
		p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt.String)
	}
	if updatedAt.Valid {
		p.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt.String)
	}
	return &p, nil
}

func scanSearchPresetFromRows(rows *sql.Rows) (*SearchPreset, error) {
	var p SearchPreset
	var createdAt, updatedAt sql.NullString
	if err := rows.Scan(&p.ID, &p.Name, &p.Query, &p.FiltersJSON, &p.IsDefault, &createdAt, &updatedAt); err != nil {
		return nil, fmt.Errorf("scanning search preset: %w", err)
	}
	if createdAt.Valid {
		p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt.String)
	}
	if updatedAt.Valid {
		p.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAt.String)
	}
	return &p, nil
}

func scanWatchlist(row interface{ Scan(...any) error }) (*Watchlist, error) {
	var w Watchlist
	var lastChecked, lastNotified, createdAtStr, updatedAtStr sql.NullString
	var lastResultsJSON sql.NullString
	var intervalMinutes int
	var notifyEnabled int
	err := row.Scan(&w.ID, &w.Name, &w.Query, &intervalMinutes, &w.FiltersJSON, &notifyEnabled,
		&w.Enabled, &w.AutoEnqueue, &lastChecked, &w.LastMatchFingerprint, &lastNotified,
		&lastResultsJSON, &createdAtStr, &updatedAtStr)
	w.NotifyEnabled = notifyEnabled != 0
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning watchlist: %w", err)
	}
	w.IntervalMinutes = intervalMinutes
	if w.IntervalMinutes < 5 {
		w.IntervalMinutes = 60
	}
	if lastResultsJSON.Valid {
		w.LastResultsJSON = json.RawMessage(lastResultsJSON.String)
	}
	if createdAtStr.Valid {
		w.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr.String)
	}
	if updatedAtStr.Valid {
		w.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAtStr.String)
	}
	if lastChecked.Valid {
		t, err := time.Parse("2006-01-02 15:04:05", lastChecked.String)
		if err == nil {
			w.LastCheckedAt = &t
		}
	}
	if lastNotified.Valid {
		t, err := time.Parse("2006-01-02 15:04:05", lastNotified.String)
		if err == nil {
			w.LastNotifiedAt = &t
		}
	}
	return &w, nil
}

func scanWatchlistFromRows(rows *sql.Rows) (*Watchlist, error) {
	var w Watchlist
	var lastChecked, lastNotified, createdAtStr, updatedAtStr sql.NullString
	var lastResultsJSON sql.NullString
	var intervalMinutes int
	var notifyEnabled int
	if err := rows.Scan(&w.ID, &w.Name, &w.Query, &intervalMinutes, &w.FiltersJSON, &notifyEnabled,
		&w.Enabled, &w.AutoEnqueue, &lastChecked, &w.LastMatchFingerprint, &lastNotified,
		&lastResultsJSON, &createdAtStr, &updatedAtStr); err != nil {
		return nil, fmt.Errorf("scanning watchlist: %w", err)
	}
	w.NotifyEnabled = notifyEnabled != 0
	w.IntervalMinutes = intervalMinutes
	if w.IntervalMinutes < 5 {
		w.IntervalMinutes = 60
	}
	if lastResultsJSON.Valid {
		w.LastResultsJSON = json.RawMessage(lastResultsJSON.String)
	}
	if lastChecked.Valid {
		t, err := time.Parse("2006-01-02 15:04:05", lastChecked.String)
		if err == nil {
			w.LastCheckedAt = &t
		}
	}
	if lastNotified.Valid {
		t, err := time.Parse("2006-01-02 15:04:05", lastNotified.String)
		if err == nil {
			w.LastNotifiedAt = &t
		}
	}
	if createdAtStr.Valid {
		w.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr.String)
	}
	if updatedAtStr.Valid {
		w.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updatedAtStr.String)
	}
	return &w, nil
}

// =========================================================================
// Helpers
// =========================================================================

// boolToInt converts a bool to 0 or 1 for SQLite integer storage.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
