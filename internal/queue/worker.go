package queue

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"xdcc-go/internal/entities"
	xdccirc "xdcc-go/internal/irc"
	"xdcc-go/internal/store"
)

// ---------------------------------------------------------------------------
// Worker configuration
// ---------------------------------------------------------------------------

// DownloadConfig holds the configuration for a single download worker.
type DownloadConfig struct {
	TempDir          string
	DestDir          string
	ConflictPolicy   string
	MaxRateBPS       int64
	Nickname         string
	ChannelJoinDelay int // seconds before WHOIS: -1=random 5-10s, 0=no delay, >0=fixed
	Logger           xdccirc.Logger

	// IRCManager for persistent connections (optional - if nil, uses temporary connections)
	IRCManager IRCManagerInterface
}

// ---------------------------------------------------------------------------
// Worker result
// ---------------------------------------------------------------------------

// workerResult holds the outcome of a single download execution.
type workerResult struct {
	DownloadID int64
	Error      error
	FilePath   string // final file path on success
	Filename   string // discovered filename (may be empty until DCC SEND)
	FileSize   int64
	BotNotice  string
	Skipped    bool // true when file was skipped due to conflict policy
}

// ---------------------------------------------------------------------------
// runDownload — executes a single pack download
// ---------------------------------------------------------------------------

// runDownload runs a single XDCC pack download in the foreground (blocking).
// It:
//   - Uses the provided entities.XDCCPack (already built by the caller)
//   - Creates an internal/irc client with a progress callback
//   - Downloads the pack to the temp directory
//   - On success, moves the file to the destination directory
//   - Handles file conflict policy (skip/overwrite/rename)
//   - Reports progress and completion via callbacks
func runDownload(
	ctx context.Context,
	rec store.DownloadRecord,
	pack *entities.XDCCPack,
	cfg DownloadConfig,
	progressFn func(bytesReceived, totalBytes int64, speedBPS float64),
	completeFn func(result workerResult),
) {
	logger := cfg.Logger

	result := workerResult{
		DownloadID: rec.ID,
		FileSize:   rec.FileSize,
	}

	var srcPath string
	var downloadErr error
	var err error

	// --- Execute download with IRCManager (persistent) or temp connection ---
	if cfg.IRCManager != nil {
		// Use persistent IRC connections via IRCManager
		logger.Printf("→ [download %d] Using persistent IRC connection for %s (bot=%s, channel=%q, file=%s)",
			rec.ID, rec.ServerAddress, rec.Bot, rec.Channel, rec.Filename)
		srcPath, downloadErr = cfg.IRCManager.DownloadPack(ctx, pack, rec.Channel, progressFn)
	} else {
		// Fallback to temporary IRC connection (for CLI tools)
		logger.Printf("→ [download %d] Using temporary IRC connection for %s (bot=%s, channel=%q, file=%s)",
			rec.ID, rec.ServerAddress, rec.Bot, rec.Channel, rec.Filename)
		srcPath, downloadErr = downloadWithTempConnection(ctx, pack, rec.Channel, cfg, progressFn)
	}

	// Capture filename and size from pack if discovered during download
	if pack.Filename != "" && result.Filename == "" {
		result.Filename = pack.Filename
	}
	if pack.Size > 0 {
		result.FileSize = pack.Size
	}

	if downloadErr != nil {
		logger.Printf("✗ [download %d] FAILED — bot=%s server=%s channel=%q file=%q error=%v",
			rec.ID, rec.Bot, rec.ServerAddress, rec.Channel, rec.Filename, downloadErr)
		result.Error = downloadErr
		result.BotNotice = "" // TODO: extract from error if available
		completeFn(result)
		return
	}

	logger.Printf("✓ [download %d] Transfer complete — moving from temp to destination (file=%s)",
		rec.ID, rec.Filename)

	// Verify the file exists
	if _, err := os.Stat(srcPath); err != nil {
		result.Error = fmt.Errorf("downloaded file not found at %s: %w", srcPath, err)
		completeFn(result)
		return
	}

	// --- Move to destination directory ---
	// Use discovered filename from pack if the record's filename is still empty
	// (happens for manual downloads where filename is unknown until bot notice).
	destFilename := rec.Filename
	if destFilename == "" && pack.Filename != "" {
		destFilename = pack.Filename
	}
	var destPath string
	if destFilename != "" {
		destPath, err = entities.SafeJoin(cfg.DestDir, destFilename)
		if err != nil {
			result.Error = fmt.Errorf("invalid destination filename %q: %w", destFilename, err)
			completeFn(result)
			return
		}
	} else {
		destPath = cfg.DestDir
	}

	// Handle conflict policy
	conflictPolicy := cfg.ConflictPolicy
	if conflictPolicy == "" {
		conflictPolicy = "skip"
	}

	if _, err := os.Stat(destPath); err == nil {
		// File already exists at destination
		switch conflictPolicy {
		case "skip":
			// Remove the temp file and report as skipped
			if err := os.Remove(srcPath); err != nil {
				logger.Printf("warning: failed to remove temp file %s: %v", srcPath, err)
			}
			result.FilePath = destPath
			result.Skipped = true
			completeFn(result)
			return
		case "overwrite":
			// Remove destination file, then move.
			// NOTE: on Windows, os.Remove fails if the file is open by another
			// process (on Unix, the inode is unlinked immediately and the file
			// remains accessible to existing handles). If the destination file
			// is locked, the error is propagated to the caller.
			if err := os.Remove(destPath); err != nil {
				result.Error = fmt.Errorf("cannot overwrite %s: %w", destPath, err)
				completeFn(result)
				return
			}
		case "rename":
			// Add timestamp suffix
			ext := filepath.Ext(destPath)
			base := destPath[:len(destPath)-len(ext)]
			destPath = fmt.Sprintf("%s_%s%s", base, time.Now().Format("20060102_150405"), ext)
		}
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		result.Error = fmt.Errorf("creating destination directory: %w", err)
		completeFn(result)
		return
	}

	// Move the file (rename works within same filesystem; fall back to copy+delete)
	if err := os.Rename(srcPath, destPath); err != nil {
		// Cross-filesystem move: copy then delete
		if err := copyFile(srcPath, destPath); err != nil {
			result.Error = fmt.Errorf("moving file to destination: %w", err)
			completeFn(result)
			return
		}
		if err := os.Remove(srcPath); err != nil {
			logger.Printf("warning: failed to remove temp file %s after copy: %v", srcPath, err)
		}
	}

	result.FilePath = destPath
	result.Error = nil

	// Update result file size from actual downloaded file
	if fi, err := os.Stat(destPath); err == nil {
		result.FileSize = fi.Size()
	}

	// If filename was discovered during download (e.g. from bot notice or DCC),
	// propagate it to the result so the queue manager can update the store.
	if pack.Filename != "" {
		result.Filename = pack.Filename
	}
	if pack.Size > 0 {
		result.FileSize = pack.Size
	}

	completeFn(result)
}

// copyFile copies a file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer srcFile.Close()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stating source: %w", err)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode())
	if err != nil {
		return fmt.Errorf("creating destination: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying data: %w", err)
	}
	return nil
}

// downloadWithTempConnection performs a download using a temporary IRC connection.
// This is used as fallback when IRCManager is not available (e.g., CLI tools).
func downloadWithTempConnection(
	ctx context.Context,
	pack *entities.XDCCPack,
	channel string,
	cfg DownloadConfig,
	progressFn func(bytesReceived, totalBytes int64, speedBPS float64),
) (string, error) {
	logger := cfg.Logger

	// Determine throttle (BPS limit)
	throttle := cfg.MaxRateBPS
	if throttle < 0 {
		throttle = 0
	}

	opts := xdccirc.DownloadOptions{
		ConnectTimeout:   120,
		StallTimeout:     60,
		FallbackChannel:  channel,
		ThrottleBytes:    throttle,
		WaitTime:         1,
		ChannelJoinDelay: cfg.ChannelJoinDelay, // from config: -1=random, 0=no delay, >0=fixed
		Username:         cfg.Nickname,
		Logger:           logger,
		ProgressCallback: progressFn,
	}

	// Execute download
	packSlice := []*entities.XDCCPack{pack}
	client := xdccirc.NewClient(ctx, packSlice, opts, -1) // -1 = quiet
	results := client.DownloadAll()

	if len(results) == 0 {
		return "", fmt.Errorf("no result from download client")
	}

	r := results[0]
	if r.Error != nil {
		return "", r.Error
	}

	// Return downloaded file path
	srcPath := pack.GetFilepath()
	if r.FilePath != "" {
		srcPath = r.FilePath
	}

	// Validate the returned temp path is contained within TempDir (defense-in-depth).
	// Use the normalized path from SafeJoin so any path-traversal artifacts in srcPath
	// are stripped before the caller uses it (e.g. os.Remove, os.Rename).
	validatedPath, err := entities.SafeJoin(cfg.TempDir, srcPath)
	if err != nil {
		return "", fmt.Errorf("invalid temp file path %q (not within %s): %w", srcPath, cfg.TempDir, err)
	}
	srcPath = validatedPath

	return srcPath, nil
}
