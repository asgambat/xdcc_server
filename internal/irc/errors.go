package irc

import (
	"fmt"

	"github.com/lrstanley/girc"
)

// XDCCDownloadError represents a typed error from the XDCC download process.
type XDCCDownloadError struct {
	Kind    string
	Message string
}

func (e *XDCCDownloadError) Error() string {
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

// Is allows errors.Is() to match by Kind, even when wrapped.
func (e *XDCCDownloadError) Is(target error) bool {
	t, ok := target.(*XDCCDownloadError)
	if !ok {
		return false
	}
	return e.Kind == t.Kind
}

var (
	ErrTimeout           = &XDCCDownloadError{Kind: "timeout", Message: "download timed out"}
	ErrBotNotFound       = &XDCCDownloadError{Kind: "bot_not_found", Message: "bot does not exist on server"}
	ErrPackAlreadyReq    = &XDCCDownloadError{Kind: "pack_already_requested", Message: "pack already requested, try again later"}
	ErrAlreadyDownloaded = &XDCCDownloadError{Kind: "already_downloaded", Message: "file already downloaded"}
	ErrBotDenied         = &XDCCDownloadError{Kind: "bot_denied", Message: "bot denied the XDCC request"}
	ErrServerUnreachable = &XDCCDownloadError{Kind: "server_unreachable", Message: "IRC server is unreachable"}
	ErrUnrecoverable     = &XDCCDownloadError{Kind: "unrecoverable", Message: "unrecoverable error (IP banned?)"}
	ErrDownloadFailed    = &XDCCDownloadError{Kind: "download_failed", Message: "download did not complete"}
	ErrCancelled         = &XDCCDownloadError{Kind: "cancelled", Message: "download cancelled by user"}
)

// DownloadOptions configures a download session.
type DownloadOptions struct {
	ConnectTimeout   int    // seconds to wait for bot to initiate DCC (default 120)
	StallTimeout     int    // seconds of no transfer progress before aborting (0 = disabled, default 60)
	FallbackChannel  string // used if WHOIS finds no channels
	ThrottleBytes    int64  // bytes/sec limit, -1 = unlimited
	WaitTime         int    // seconds to wait before sending XDCC request
	Username         string // IRC nick to use; empty = random
	ChannelJoinDelay int    // seconds to wait after connecting before WHOIS; -1 = random 5-10
	// DNSServer is the fallback DNS resolver used when the system DNS returns a
	// blocked address (0.0.0.0 / ::). Format: "host:port". Default: "8.8.8.8:53".
	DNSServer string
	Logger    Logger // custom logger; nil = default (log.New with "[xdcc] " prefix)

	// ProgressCallback is called periodically during a download with the current
	// progress. bytesReceived and totalBytes are in bytes; speedBPS is the
	// transfer speed in bytes/second averaged over the last interval.
	// If nil, no progress reporting occurs (default CLI progress printing is
	// is unaffected — it still prints to stdout based on verbosity).
	ProgressCallback func(bytesReceived, totalBytes int64, speedBPS float64)

	// ReconnectCallback returns an up-to-date *girc.Client when using a
	// persistent connection managed externally (e.g. by ircmanager).
	// When the IRC connection drops and the external manager recreates it,
	// this callback provides the new client so handlers can be rebound.
	// Return nil if the connection has not been re-established yet.
	ReconnectCallback func() *girc.Client
}

// PackResult holds the outcome of a single pack download.
type PackResult struct {
	FilePath      string // non-empty on success
	Filename      string // discovered filename (may be empty until DCC SEND)
	FileSize      int64  // discovered file size in bytes (0 until known)
	Error         error
	LastBotNotice string // last NOTICE from bot (useful when Error != nil)
}
