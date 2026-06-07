// Package config provides configuration loading and validation for the xdcc-server.
// Configuration is loaded from three sources, in increasing priority:
//  1. config.yaml (lowest priority)
//  2. Environment variables
//  3. CLI flags (highest priority)
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Duration is a time.Duration that serializes as a short string (e.g., "30m")
// in both JSON and YAML, but also accepts integer nanoseconds during unmarshal.
// This solves the impedance mismatch when the frontend sends JSON with
// integer durations (from JS Date.now() math) but the backend expects
// human-readable duration strings in YAML.
// ---------------------------------------------------------------------------

type Duration time.Duration

func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON serializes as a human-readable duration string (e.g., "30m0s").
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts both string ("30m", "1h30m") and integer (nanoseconds).
func (d *Duration) UnmarshalJSON(data []byte) error {
	// Try string format first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration string %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	// Fall back to integer (nanoseconds)
	var ns int64
	if err := json.Unmarshal(data, &ns); err != nil {
		return fmt.Errorf("duration must be a string or nanoseconds integer, got %s", string(data))
	}
	*d = Duration(ns)
	return nil
}

// MarshalYAML serializes as a human-readable duration string.
func (d Duration) MarshalYAML() (interface{}, error) {
	return time.Duration(d).String(), nil
}

// UnmarshalYAML accepts both string ("30m") and integer (nanoseconds).
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar, got %v", node.Kind)
	}
	// Try as string first ("30m", "1h30m", etc.)
	var s string
	if err := node.Decode(&s); err == nil {
		if parsed, err := time.ParseDuration(s); err == nil {
			*d = Duration(parsed)
			return nil
		}
	}
	// Try as integer nanoseconds
	var ns int64
	if err := node.Decode(&ns); err == nil {
		*d = Duration(ns)
		return nil
	}
	return fmt.Errorf("duration must be a string like \"30m\" or nanoseconds, got %q", node.Value)
}

// ---------------------------------------------------------------------------
// Config struct
// ---------------------------------------------------------------------------

// Config holds the complete server configuration.
type Config struct {
	IRC           IRCConfig            `yaml:"irc"            json:"irc"`
	HTTP          HTTPConfig           `yaml:"http"           json:"http"`
	Security      SecurityConfig       `yaml:"security"       json:"security"`
	Download      DownloadConfig       `yaml:"download"       json:"download"`
	Search        SearchConfig         `yaml:"search"         json:"search"`
	Storage       StorageConfig        `yaml:"storage"        json:"storage"`
	Logging       LoggingConfig        `yaml:"logging"        json:"logging"`
	UI            UIConfig             `yaml:"ui"             json:"ui"`
	Profiling     ProfilingConfig      `yaml:"profiling"      json:"profiling"`
	Notifications []NotificationConfig `yaml:"notifications"  json:"notifications"`
}

type IRCConfig struct {
	Nickname          string         `yaml:"nickname"           env:"XDCC_IRC_NICKNAME"           json:"nickname"`
	DefaultServers    []ServerConfig `yaml:"default_servers"                               json:"default_servers"`
	ChannelBlacklist  []string       `yaml:"channel_blacklist"                               json:"channel_blacklist"`
}

type ServerConfig struct {
	Address     string          `yaml:"address"     json:"address"`
	Port        int             `yaml:"port"        json:"port"`
	AutoConnect bool            `yaml:"auto_connect" json:"auto_connect"`
	Channels    []ChannelConfig `yaml:"channels"    json:"channels"`
}

type ChannelConfig struct {
	Name     string `yaml:"name"      json:"name"`
	AutoJoin bool   `yaml:"auto_join" json:"auto_join"`
}

type HTTPConfig struct {
	Port        int      `yaml:"port"          env:"XDCC_HTTP_PORT"          json:"port"`
	BindAddress string   `yaml:"bind_address"  env:"XDCC_HTTP_BIND_ADDRESS"  json:"bind_address"`
	CORSOrigins []string `yaml:"cors_origins"                            json:"cors_origins"`
}

type DownloadConfig struct {
	TempDir             string `yaml:"temp_dir"               env:"XDCC_DOWNLOAD_TEMP_DIR"              json:"temp_dir"`
	DestDir             string `yaml:"dest_dir"               env:"XDCC_DOWNLOAD_DEST_DIR"              json:"dest_dir"`
	ConflictPolicy      string `yaml:"conflict_policy"        env:"XDCC_DOWNLOAD_CONFLICT_POLICY"       json:"conflict_policy"`
	FailFallback        string `yaml:"fail_fallback"          env:"XDCC_DOWNLOAD_FAIL_FALLBACK"         json:"fail_fallback"`
	MaxParallelTotal    int    `yaml:"max_parallel_total"     env:"XDCC_DOWNLOAD_MAX_PARALLEL"          json:"max_parallel_total"`
	MaxRateBPS          int64  `yaml:"max_rate_bps"           env:"XDCC_DOWNLOAD_MAX_RATE_BPS"          json:"max_rate_bps"`
	MinDiskSpace        int64  `yaml:"min_disk_space_bytes"   env:"XDCC_DOWNLOAD_MIN_DISK_SPACE"        json:"min_disk_space_bytes"`
	MaxRetryAttempts    int    `yaml:"max_retry_attempts"     env:"XDCC_DOWNLOAD_MAX_RETRY"             json:"max_retry_attempts"`
	StartupDelayMinutes int    `yaml:"startup_delay_minutes"  env:"XDCC_DOWNLOAD_STARTUP_DELAY_MINUTES" json:"startup_delay_minutes"`
	ChannelJoinDelay    int    `yaml:"channel_join_delay"     env:"XDCC_DOWNLOAD_CHANNEL_JOIN_DELAY"    json:"channel_join_delay"`
}

type SearchConfig struct {
	ProviderTimeout  int               `yaml:"provider_timeout"  env:"XDCC_SEARCH_PROVIDER_TIMEOUT"  json:"provider_timeout"`
	PageSize         int               `yaml:"page_size"         env:"XDCC_SEARCH_PAGE_SIZE"          json:"page_size"`
	EnabledProviders []string          `yaml:"enabled_providers"                        json:"enabled_providers"`
	Cache            SearchCacheConfig `yaml:"cache"             json:"cache"`
}

type SearchCacheConfig struct {
	Enabled  bool     `yaml:"enabled"   env:"XDCC_SEARCH_CACHE_ENABLED" json:"enabled"`
	FreshTTL Duration `yaml:"fresh_ttl" json:"fresh_ttl"`
	StaleTTL Duration `yaml:"stale_ttl" json:"stale_ttl"`
}

type StorageConfig struct {
	DBPath             string `yaml:"db_path"              env:"XDCC_STORAGE_DB_PATH"              json:"db_path"`
	DownloadsRetention string `yaml:"downloads_retention"  env:"XDCC_STORAGE_DOWNLOADS_RETENTION" json:"downloads_retention"`
	CleanupInterval    string `yaml:"cleanup_interval"     env:"XDCC_STORAGE_CLEANUP_INTERVAL"    json:"cleanup_interval"`
}

type LoggingConfig struct {
	Level    string `yaml:"level"     env:"XDCC_LOGGING_LEVEL"     json:"level"`
	FilePath string `yaml:"file_path" env:"XDCC_LOGGING_FILE_PATH" json:"file_path"`
}

type UIConfig struct {
	SetupCompleted bool   `yaml:"setup_completed" env:"XDCC_UI_SETUP_COMPLETED" json:"setup_completed"`
	Theme          string `yaml:"theme"          env:"XDCC_UI_THEME"          json:"theme"`
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Security
// ---------------------------------------------------------------------------

// SecurityConfig holds authentication and security settings.
type SecurityConfig struct {
	AdminToken      string `yaml:"admin_token"       env:"XDCC_SECURITY_ADMIN_TOKEN"       json:"admin_token"`
	TokenTTLMinutes int    `yaml:"token_ttl_minutes" env:"XDCC_SECURITY_TOKEN_TTL_MINUTES" json:"token_ttl_minutes"`
}

// NotificationConfig configures a single external notification target.
type NotificationConfig struct {
	Type string `yaml:"type" json:"type"`
	// Webhook
	WebhookEndpoint string `yaml:"webhook_endpoint" json:"webhook_endpoint"`
	WebhookToken    string `yaml:"webhook_token"    json:"webhook_token"`
	// Ntfy (https://ntfy.sh)
	NtfyToken    string `yaml:"ntfy_token"    json:"ntfy_token"`
	NtfyEndpoint string `yaml:"ntfy_endpoint" json:"ntfy_endpoint"`
	// Pushover (https://pushover.net)
	PushoverToken    string `yaml:"pushover_token"    json:"pushover_token"`
	PushoverUser     string `yaml:"pushover_user"     json:"pushover_user"`
	PushoverEndpoint string `yaml:"pushover_endpoint" json:"pushover_endpoint"`
	// Event filter (empty = all events)
	Events []string `yaml:"events" json:"events"`
}

// ProfilingConfig controls runtime profiling rates exposed via /debug/pprof.
// Set both to 0 (default) to disable profiling overhead in production.
type ProfilingConfig struct {
	BlockProfileRate     int `yaml:"block_profile_rate"    env:"XDCC_PROFILING_BLOCK_RATE"    json:"block_profile_rate"`
	MutexProfileFraction int `yaml:"mutex_profile_fraction" env:"XDCC_PROFILING_MUTEX_FRACTION" json:"mutex_profile_fraction"`
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

func DefaultConfig() *Config {
	return &Config{
		IRC: IRCConfig{
			Nickname: "xdcc-user",
			DefaultServers: []ServerConfig{
				{
					Address:     "irc.rizon.net",
					Port:        6667,
					AutoConnect: true,
					Channels: []ChannelConfig{
						{Name: "#news", AutoJoin: true},
					},
				},
				{
					Address:     "irc.williamgattone.it",
					Port:        6667,
					AutoConnect: false,
					Channels: []ChannelConfig{
						{Name: "#xdcc", AutoJoin: true},
					},
				},
			},
		},
		HTTP: HTTPConfig{
			Port:        8080,
			BindAddress: "127.0.0.1",
			CORSOrigins: []string{},
		},
		Download: DownloadConfig{
			TempDir:             "./downloads/tmp",
			DestDir:             "./downloads/complete",
			ConflictPolicy:      "skip",
			FailFallback:        "suggest_only",
			MaxParallelTotal:    5,
			MaxRateBPS:          0,
			MinDiskSpace:        1 * 1024 * 1024 * 1024, // 1 GB default
			MaxRetryAttempts:    3,
			StartupDelayMinutes: 0,
			ChannelJoinDelay:    -1, // -1 = random 5-10s, 0 = no delay, >0 = fixed seconds
		},
		Search: SearchConfig{
			ProviderTimeout:  5,
			PageSize:         50,
			EnabledProviders: []string{},
			Cache: SearchCacheConfig{
				Enabled:  true,
				FreshTTL: Duration(30 * time.Minute),
				StaleTTL: Duration(24 * time.Hour),
			},
		},
		Storage: StorageConfig{
			DBPath:             "./db",
			DownloadsRetention: "30d",
			CleanupInterval:    "12h",
		},
		Logging: LoggingConfig{
			Level:    "info",
			FilePath: "",
		},
		UI: UIConfig{
			SetupCompleted: false,
			Theme:          "dark",
		},
		Security: SecurityConfig{
			AdminToken:      "",
			TokenTTLMinutes: 15,
		},
		Profiling: ProfilingConfig{
			BlockProfileRate:     0, // 0 = disabled (no overhead in production)
			MutexProfileFraction: 0, // 0 = disabled
		},
	}
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

// Load reads configuration from config.yaml, overlays environment variables,
// then applies flag overrides. Returns the merged Config.
//
// Parameters:
//   - configPath: path to the YAML config file (empty = skip file loading)
//   - flagOverrides: optional overrides from CLI flags
func Load(configPath string, flagOverrides *FlagOverrides) (*Config, error) {
	cfg := DefaultConfig()

	// 1. Load from YAML file
	if configPath != "" {
		if err := cfg.loadFile(configPath); err != nil {
			return nil, fmt.Errorf("loading config file: %w", err)
		}
	}

	// 2. Overlay environment variables
	cfg.applyEnvOverrides()

	// 3. Apply CLI flag overrides
	if flagOverrides != nil {
		flagOverrides.apply(cfg)
	}

	// 4. Expand relative paths to absolute
	cfg.expandPaths()

	// 5. Validate
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

// ---------------------------------------------------------------------------
// File loading
// ---------------------------------------------------------------------------

func (c *Config) loadFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("config file not found: %s", path)
		}
		return fmt.Errorf("reading config file: %w", err)
	}

	if err := yaml.Unmarshal(data, c); err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Environment variable overlays
// ---------------------------------------------------------------------------

// applyEnvOverrides reads environment variables for fields tagged with `env:`
// and overrides the corresponding Config fields.
//
// Supported env vars:
//
//	XDCC_IRC_NICKNAME
//	XDCC_HTTP_PORT
//	XDCC_HTTP_BIND_ADDRESS
//	XDCC_SECURITY_ADMIN_TOKEN
//	XDCC_SECURITY_TOKEN_TTL_MINUTES
//	XDCC_DOWNLOAD_TEMP_DIR
//	XDCC_DOWNLOAD_DEST_DIR
//	XDCC_DOWNLOAD_CONFLICT_POLICY
//	XDCC_DOWNLOAD_FAIL_FALLBACK
//	XDCC_DOWNLOAD_MAX_PARALLEL
//	XDCC_DOWNLOAD_MAX_RATE_BPS
//	XDCC_DOWNLOAD_MIN_DISK_SPACE
//	XDCC_DOWNLOAD_MAX_RETRY
//	XDCC_DOWNLOAD_STARTUP_DELAY_MINUTES
//	XDCC_SEARCH_PROVIDER_TIMEOUT
//	XDCC_SEARCH_PAGE_SIZE
//	XDCC_SEARCH_CACHE_ENABLED
//	XDCC_STORAGE_DOWNLOADS_RETENTION
//	XDCC_STORAGE_CLEANUP_INTERVAL
//	XDCC_LOGGING_LEVEL
//	XDCC_LOGGING_FILE_PATH
//	XDCC_UI_SETUP_COMPLETED
//	XDCC_PROFILING_BLOCK_RATE
//	XDCC_PROFILING_MUTEX_FRACTION
func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("XDCC_IRC_NICKNAME"); v != "" {
		c.IRC.Nickname = v
	}
	if v := os.Getenv("XDCC_HTTP_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			c.HTTP.Port = port
		}
	}
	if v := os.Getenv("XDCC_HTTP_BIND_ADDRESS"); v != "" {
		c.HTTP.BindAddress = v
	}
	if v := os.Getenv("XDCC_SECURITY_ADMIN_TOKEN"); v != "" {
		c.Security.AdminToken = v
	}
	if v := os.Getenv("XDCC_SECURITY_TOKEN_TTL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Security.TokenTTLMinutes = n
		}
	}
	if v := os.Getenv("XDCC_DOWNLOAD_TEMP_DIR"); v != "" {
		c.Download.TempDir = v
	}
	if v := os.Getenv("XDCC_DOWNLOAD_DEST_DIR"); v != "" {
		c.Download.DestDir = v
	}
	if v := os.Getenv("XDCC_DOWNLOAD_CONFLICT_POLICY"); v != "" {
		c.Download.ConflictPolicy = v
	}
	if v := os.Getenv("XDCC_DOWNLOAD_FAIL_FALLBACK"); v != "" {
		c.Download.FailFallback = v
	}
	if v := os.Getenv("XDCC_DOWNLOAD_MAX_PARALLEL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Download.MaxParallelTotal = n
		}
	}
	if v := os.Getenv("XDCC_DOWNLOAD_MAX_RATE_BPS"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.Download.MaxRateBPS = n
		}
	}
	if v := os.Getenv("XDCC_DOWNLOAD_MIN_DISK_SPACE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.Download.MinDiskSpace = n
		}
	}
	if v := os.Getenv("XDCC_DOWNLOAD_MAX_RETRY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Download.MaxRetryAttempts = n
		}
	}
	if v := os.Getenv("XDCC_DOWNLOAD_STARTUP_DELAY_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Download.StartupDelayMinutes = n
		}
	}
	if v := os.Getenv("XDCC_DOWNLOAD_CHANNEL_JOIN_DELAY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Download.ChannelJoinDelay = n
		}
	}
	if v := os.Getenv("XDCC_SEARCH_PROVIDER_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Search.ProviderTimeout = n
		}
	}
	if v := os.Getenv("XDCC_SEARCH_PAGE_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Search.PageSize = n
		}
	}
	if v := os.Getenv("XDCC_SEARCH_CACHE_ENABLED"); v != "" {
		c.Search.Cache.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("XDCC_STORAGE_DB_PATH"); v != "" {
		c.Storage.DBPath = v
	}
	if v := os.Getenv("XDCC_STORAGE_DOWNLOADS_RETENTION"); v != "" {
		c.Storage.DownloadsRetention = v
	}
	if v := os.Getenv("XDCC_STORAGE_CLEANUP_INTERVAL"); v != "" {
		c.Storage.CleanupInterval = v
	}
	if v := os.Getenv("XDCC_LOGGING_LEVEL"); v != "" {
		c.Logging.Level = v
	}
	if v := os.Getenv("XDCC_LOGGING_FILE_PATH"); v != "" {
		c.Logging.FilePath = v
	}
	if v := os.Getenv("XDCC_UI_SETUP_COMPLETED"); v != "" {
		c.UI.SetupCompleted = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("XDCC_UI_THEME"); v != "" {
		c.UI.Theme = v
	}
	if v := os.Getenv("XDCC_PROFILING_BLOCK_RATE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Profiling.BlockProfileRate = n
		}
	}
	if v := os.Getenv("XDCC_PROFILING_MUTEX_FRACTION"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Profiling.MutexProfileFraction = n
		}
	}
}

// ---------------------------------------------------------------------------
// FlagOverrides — values that can be set via CLI flags
// ---------------------------------------------------------------------------

// FlagOverrides holds optional CLI flag overrides that take highest priority.
type FlagOverrides struct {
	Port        *int
	DownloadDir *string
	TempDir     *string
	ConfigPath  *string
}

func (f *FlagOverrides) apply(c *Config) {
	if f == nil {
		return
	}
	if f.Port != nil {
		c.HTTP.Port = *f.Port
	}
	if f.DownloadDir != nil {
		c.Download.DestDir = *f.DownloadDir
	}
	if f.TempDir != nil {
		c.Download.TempDir = *f.TempDir
	}
}

// ---------------------------------------------------------------------------
// Path expansion
// ---------------------------------------------------------------------------

func (c *Config) expandPaths() {
	c.Storage.DBPath = expandPath(c.Storage.DBPath)
	c.Download.TempDir = expandPath(c.Download.TempDir)
	c.Download.DestDir = expandPath(c.Download.DestDir)
	if c.Logging.FilePath != "" {
		c.Logging.FilePath = expandPath(c.Logging.FilePath)
	}
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		// Resolve relative to CWD
		wd, err := os.Getwd()
		if err == nil {
			return filepath.Join(wd, p)
		}
	}
	return p
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// Validate checks that all configuration fields have acceptable values.
// It is called during Load() and can be called after runtime updates before
// persisting with Save().
func (c *Config) Validate() error {
	// IRC nickname
	if c.IRC.Nickname == "" {
		return fmt.Errorf("irc.nickname must not be empty")
	}

	// HTTP port
	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		return fmt.Errorf("http.port must be between 1 and 65535, got %d", c.HTTP.Port)
	}

	// Security token TTL
	if c.Security.TokenTTLMinutes < 1 {
		c.Security.TokenTTLMinutes = 15
	}

	// Database path
	if c.Storage.DBPath == "" {
		return fmt.Errorf("storage.db_path must not be empty")
	}

	// Download directories
	if c.Download.TempDir == "" {
		return fmt.Errorf("download.temp_dir must not be empty")
	}
	if c.Download.DestDir == "" {
		return fmt.Errorf("download.dest_dir must not be empty")
	}

	// Conflict policy
	switch c.Download.ConflictPolicy {
	case "skip", "overwrite", "rename":
		// valid
	default:
		return fmt.Errorf("download.conflict_policy must be one of: skip, overwrite, rename (got %q)", c.Download.ConflictPolicy)
	}

	// Fail fallback
	switch c.Download.FailFallback {
	case "suggest_only", "auto_retry_best":
		// valid
	default:
		return fmt.Errorf("download.fail_fallback must be one of: suggest_only, auto_retry_best (got %q)", c.Download.FailFallback)
	}

	// Max parallel
	if c.Download.MaxParallelTotal < 1 {
		return fmt.Errorf("download.max_parallel_total must be at least 1, got %d", c.Download.MaxParallelTotal)
	}

	// Startup delay
	if c.Download.StartupDelayMinutes < 0 {
		return fmt.Errorf("download.startup_delay_minutes must be >= 0, got %d", c.Download.StartupDelayMinutes)
	}

	// Channel join delay: -1 = random, 0 = no delay, >0 = fixed
	if c.Download.ChannelJoinDelay < -1 {
		return fmt.Errorf("download.channel_join_delay must be >= -1 (random), got %d", c.Download.ChannelJoinDelay)
	}

	// Search
	if c.Search.ProviderTimeout < 1 {
		return fmt.Errorf("search.provider_timeout must be at least 1 second, got %d", c.Search.ProviderTimeout)
	}
	if c.Search.PageSize < 1 {
		return fmt.Errorf("search.page_size must be at least 1, got %d", c.Search.PageSize)
	}

	// Log level
	switch c.Logging.Level {
	case "debug", "info", "warn", "error", "":
		// valid
	default:
		return fmt.Errorf("logging.level must be one of: debug, info, warn, error (got %q)", c.Logging.Level)
	}

	// Duration validation
	if _, err := parseDurationString(c.Storage.DownloadsRetention); err != nil {
		return fmt.Errorf("storage.downloads_retention: %w", err)
	}
	if _, err := parseDurationString(c.Storage.CleanupInterval); err != nil {
		return fmt.Errorf("storage.cleanup_interval: %w", err)
	}

	// Server configs
	for i, s := range c.IRC.DefaultServers {
		if s.Address == "" {
			return fmt.Errorf("irc.default_servers[%d].address must not be empty", i)
		}
		if s.Port < 1 || s.Port > 65535 {
			return fmt.Errorf("irc.default_servers[%d].port must be between 1 and 65535", i)
		}
		for j, ch := range s.Channels {
			if ch.Name == "" {
				return fmt.Errorf("irc.default_servers[%d].channels[%d].name must not be empty", i, j)
			}
		}
	}

	return nil
}

// ValidatePartial checks only critical fields that could cause immediate
// crashes or undefined behavior if invalid. Fields with sane defaults are
// skipped. This is used for validating raw YAML edits where the user may
// have intentionally left optional fields empty.
func (c *Config) ValidatePartial() error {
	// IRC nickname — empty nickname will crash IRC connection
	if c.IRC.Nickname == "" {
		return fmt.Errorf("irc.nickname must not be empty (required for IRC connection)")
	}

	// HTTP port — invalid port will cause server startup failure
	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		return fmt.Errorf("http.port must be between 1 and 65535, got %d", c.HTTP.Port)
	}

	// Database path — empty path will crash on first DB access
	if c.Storage.DBPath == "" {
		return fmt.Errorf("storage.db_path must not be empty (required for data persistence)")
	}

	// Server configs — addresses and ports are critical for IRC functionality
	for i, s := range c.IRC.DefaultServers {
		if s.Address == "" {
			return fmt.Errorf("irc.default_servers[%d].address must not be empty", i)
		}
		if s.Port < 1 || s.Port > 65535 {
			return fmt.Errorf("irc.default_servers[%d].port must be between 1 and 65535", i)
		}
		// Channel names must also be non-empty to avoid runtime join failures
		for j, ch := range s.Channels {
			if ch.Name == "" {
				return fmt.Errorf("irc.default_servers[%d].channels[%d].name must not be empty", i, j)
			}
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// parseDurationString parses a duration string like "30d", "12h", "45m".
func parseDurationString(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("duration string must not be empty")
	}

	// Handle "d" (days) suffix manually since Go's time.ParseDuration doesn't support it
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q: %w", s, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	return time.ParseDuration(s)
}

// ParseDownloadsRetention parses the downloads retention string and returns
// the duration as a number of days.
func (c *Config) ParseDownloadsRetention() (int, error) {
	s := c.Storage.DownloadsRetention
	if strings.HasSuffix(s, "d") {
		daysStr := strings.TrimSuffix(s, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil {
			return 0, fmt.Errorf("invalid downloads_retention %q: %w", s, err)
		}
		return days, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid downloads_retention %q: %w", s, err)
	}
	return int(d.Hours() / 24), nil
}

// IsChannelBlacklisted returns true if the given channel name is in the
// channel blacklist. The check is case-insensitive and the channel name
// is normalized (lowercase, # prefix). Blacklisted channels are never
// joined, not even manually or via WHOIS discovery.
func (c *Config) IsChannelBlacklisted(channel string) bool {
	ch := strings.ToLower(strings.TrimSpace(channel))
	if ch != "" && !strings.HasPrefix(ch, "#") {
		ch = "#" + ch
	}
	for _, bl := range c.IRC.ChannelBlacklist {
		if strings.ToLower(strings.TrimSpace(bl)) == ch {
			return true
		}
	}
	return false
}

// ParseCleanupInterval parses the cleanup interval string into a time.Duration.
func (c *Config) ParseCleanupInterval() (time.Duration, error) {
	return parseDurationString(c.Storage.CleanupInterval)
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// SaveRaw writes raw YAML bytes directly to the config file. This preserves
// comments and formatting from the Advanced YAML editor. A .bak backup of the
// existing file is created before overwriting. The bytes are validated by the
// caller before calling this method. If path is empty, returns an error.
func (c *Config) SaveRaw(path string, rawYAML []byte) error {
	if path == "" {
		return fmt.Errorf("config path is empty, cannot save")
	}

	// Stat the file once: if it exists, create a backup and preserve its mode.
	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode()
		bakPath := path + ".bak"
		if copyErr := copyFile(path, bakPath); copyErr != nil {
			return fmt.Errorf("creating backup %s: %w", bakPath, copyErr)
		}
	}

	// Atomic write: write to temp file first, then rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, rawYAML, mode); err != nil {
		return fmt.Errorf("writing temp config %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, path); err == nil {
		return nil
	}

	// Rename failed (e.g., Docker bind mount → EBUSY). Clean up temp file
	// and fall back to writing directly to the target path.
	os.Remove(tmp)
	if err := os.WriteFile(path, rawYAML, mode); err == nil {
		return nil
	}

	// Last-resort fallback: write to /data then copy (Docker volume workaround).
	if di, e := os.Stat("/data"); e == nil && di.IsDir() {
		tmpData := "/data/config.yaml.tmp"
		if err := os.WriteFile(tmpData, rawYAML, 0o600); err == nil {
			defer os.Remove(tmpData)
			if copyErr := copyFile(tmpData, path); copyErr == nil {
				return nil
			}
		}
	}

	return fmt.Errorf("all write methods failed for %s", path)
}

// Save marshals the current configuration to YAML and writes it atomically
// to the given path. For raw byte writes (preserving comments/formatting),
// use SaveRaw instead. If path is empty, returns an error.
func (c *Config) Save(path string) error {
	if path == "" {
		return fmt.Errorf("config path is empty, cannot save")
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshaling config to YAML: %w", err)
	}

	// Stat the file once: if it exists, create a backup and preserve its mode.
	mode := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil {
		mode = fi.Mode()
		bakPath := path + ".bak"
		if copyErr := copyFile(path, bakPath); copyErr != nil {
			return fmt.Errorf("creating backup %s: %w", bakPath, copyErr)
		}
	}

	// Atomic write: write to temp file first, then rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return fmt.Errorf("writing temp config %s: %w", tmp, err)
	}

	if err := os.Rename(tmp, path); err == nil {
		return nil
	}

	// Rename failed (e.g., Docker bind mount → EBUSY). Clean up temp file
	// and fall back to writing directly to the target path.
	os.Remove(tmp)
	if err := os.WriteFile(path, data, mode); err == nil {
		return nil
	}

	// Last-resort fallback: write to /data then copy (Docker volume workaround).
	if di, e := os.Stat("/data"); e == nil && di.IsDir() {
		tmpData := "/data/config.yaml.tmp"
		if err := os.WriteFile(tmpData, data, 0o600); err == nil {
			defer os.Remove(tmpData)
			if copyErr := copyFile(tmpData, path); copyErr == nil {
				return nil
			}
		}
	}

	return fmt.Errorf("all write methods failed for %s", path)
}

// copyFile copies the contents of src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// Preserve existing permissions when possible; default to 0o644.
	mode := os.FileMode(0o644)
	if fi, e := os.Stat(dst); e == nil {
		mode = fi.Mode()
	}
	return os.WriteFile(dst, in, mode)
}

// ---------------------------------------------------------------------------
// Partial update — preserves comments & formatting in the YAML file
// ---------------------------------------------------------------------------

// configChange represents a single field change between old and new config.
type configChange struct {
	yamlPath []string    // YAML key path, e.g. ["ui", "theme"]
	newValue interface{} // new value (string, int, int64, bool, float64)
}

// ApplyPartial reads the existing config YAML file, computes a diff between
// oldCfg and c (the new config), and applies only the changed scalar values
// to the YAML node tree. This preserves all comments, formatting, and field
// ordering in the file. For non-scalar changes (slices, nested structs) it
// falls back to a full Save().
func (c *Config) ApplyPartial(path string, oldCfg *Config) error {
	if path == "" {
		return fmt.Errorf("config path is empty, cannot save")
	}

	// Compute the changes between old and new config.
	changes := diffConfigs(oldCfg, c)
	if len(changes) == 0 {
		return nil // nothing to do
	}

	// If any change involves a non-scalar type (slice, array, struct), fall
	// back to a full rewrite since we can't surgically update those.
	for _, ch := range changes {
		if !isScalarValue(ch.newValue) {
			return c.Save(path)
		}
	}

	// Read existing YAML file as node tree to preserve comments & formatting.
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config file for partial update: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// If we can't parse as a node tree, fall back to full save.
		return c.Save(path)
	}

	// Navigate to the root mapping node.
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return c.Save(path)
	}
	rootMapping := doc.Content[0]
	if rootMapping.Kind != yaml.MappingNode {
		return c.Save(path)
	}

	// Create a backup before modifying.
	bakPath := path + ".bak"
	if err := copyFile(path, bakPath); err != nil {
		return fmt.Errorf("creating backup before partial update: %w", err)
	}

	// Apply each scalar change to the YAML node tree.
	for _, ch := range changes {
		if err := applyScalarChange(rootMapping, ch.yamlPath, ch.newValue); err != nil {
			// If we can't apply a change (e.g. key missing in YAML), fall
			// back to full save.
			return c.Save(path)
		}
	}

	// Determine the file mode to preserve for the write.
	mode := os.FileMode(0o644)
	if fi, e := os.Stat(path); e == nil {
		mode = fi.Mode()
	}

	// Write the modified node tree back to disk (atomic write).
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshaling updated config: %w", err)
	}

	// Atomic write: write to temp file first, then rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		return fmt.Errorf("writing temp config %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err == nil {
		return nil
	}
	os.Remove(tmp)

	// Rename failed — fall back to direct write.
	if err := os.WriteFile(path, out, mode); err == nil {
		return nil
	}

	// Last-resort /data fallback.
	if di, e := os.Stat("/data"); e == nil && di.IsDir() {
		tmpData := "/data/config.yaml.tmp"
		if err := os.WriteFile(tmpData, out, 0o600); err == nil {
			defer os.Remove(tmpData)
			if copyErr := copyFile(tmpData, path); copyErr == nil {
				return nil
			}
		}
	}

	return fmt.Errorf("all write methods failed for %s", path)
}

// diffConfigs compares two Config structs and returns a list of field changes.
// Only scalar fields (string, int, int64, bool, float64) and struct fields
// are traversed. Slices/arrays are recorded as single changes for the caller
// to decide how to handle them.
func diffConfigs(oldCfg, newCfg *Config) []configChange {
	var changes []configChange
	diffStruct(reflect.ValueOf(oldCfg).Elem(), reflect.ValueOf(newCfg).Elem(), nil, &changes)
	return changes
}

// diffStruct recursively compares two struct values and records changes.
func diffStruct(oldVal, newVal reflect.Value, prefix []string, changes *[]configChange) {
	t := oldVal.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields.
		if !field.IsExported() {
			continue
		}

		// Get the YAML tag to determine the key name.
		yamlTag := field.Tag.Get("yaml")
		if yamlTag == "" || yamlTag == "-" {
			continue
		}
		key := strings.Split(yamlTag, ",")[0]
		if key == "" {
			continue
		}

		path := make([]string, len(prefix)+1)
		copy(path, prefix)
		path[len(prefix)] = key

		oldField := oldVal.Field(i)
		newField := newVal.Field(i)

		if !oldField.IsValid() || !newField.IsValid() {
			continue
		}

		switch oldField.Kind() {
		case reflect.String:
			if oldField.String() != newField.String() {
				*changes = append(*changes, configChange{path, newField.String()})
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if oldField.Int() != newField.Int() {
				// time.Duration and custom Duration are int64 but represented as
				// strings in YAML (e.g., "30m0s"). Convert to string for compatibility.
				durationType := reflect.TypeOf(time.Duration(0))
				durationAliasType := reflect.TypeOf(Duration(0))
				if field.Type == durationType || field.Type == durationAliasType {
					d := time.Duration(newField.Int()).String()
					*changes = append(*changes, configChange{path, d})
				} else {
					*changes = append(*changes, configChange{path, newField.Int()})
				}
			}
		case reflect.Float32, reflect.Float64:
			if oldField.Float() != newField.Float() {
				*changes = append(*changes, configChange{path, newField.Float()})
			}
		case reflect.Bool:
			if oldField.Bool() != newField.Bool() {
				*changes = append(*changes, configChange{path, newField.Bool()})
			}
		case reflect.Struct:
			// Recurse into nested structs.
			diffStruct(oldField, newField, path, changes)
		case reflect.Slice, reflect.Array:
			// Record the change — the caller will decide how to handle it
			// (fall back to full save if non-scalar).
			if !reflect.DeepEqual(oldField.Interface(), newField.Interface()) {
				*changes = append(*changes, configChange{path, newField.Interface()})
			}
		default:
			// For other kinds (maps, pointers, interfaces), record the change
			// if different.
			if !reflect.DeepEqual(oldField.Interface(), newField.Interface()) {
				*changes = append(*changes, configChange{path, newField.Interface()})
			}
		}
	}
}

// isScalarValue returns true if v is a scalar type (string, int, bool, float)
// that can be surgically updated in a YAML node tree.
func isScalarValue(v interface{}) bool {
	if v == nil {
		return true
	}
	switch reflect.TypeOf(v).Kind() {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16,
		reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8,
		reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64, reflect.Bool:
		return true
	default:
		return false
	}
}

// applyScalarChange navigates the yaml.Node mapping tree following yamlPath
// and updates the value at the leaf with newValue.
func applyScalarChange(mapping *yaml.Node, yamlPath []string, newValue interface{}) error {
	current := mapping
	for i, key := range yamlPath {
		if current.Kind != yaml.MappingNode || len(current.Content) < 2 {
			return fmt.Errorf("expected mapping node at path level %d, got kind %d", i, current.Kind)
		}

		found := false
		for j := 0; j < len(current.Content); j += 2 {
			if current.Content[j].Value == key {
				if i == len(yamlPath)-1 {
					// Last key — update the value node in-place.
					setScalarNodeValue(current.Content[j+1], newValue)
					return nil
				}
				// Not the last key — recurse into the sub-mapping.
				current = current.Content[j+1]
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("key %q not found in YAML mapping", key)
		}
	}
	return nil
}

// setScalarNodeValue updates a yaml.Node's Value and Tag to reflect the
// given Go scalar value.
func setScalarNodeValue(node *yaml.Node, val interface{}) {
	v := reflect.ValueOf(val)
	switch v.Kind() {
	case reflect.String:
		node.Value = v.String()
		node.Tag = "!!str"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		node.Value = strconv.FormatInt(v.Int(), 10)
		node.Tag = "!!int"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		node.Value = strconv.FormatUint(v.Uint(), 10)
		node.Tag = "!!int"
	case reflect.Float32, reflect.Float64:
		node.Value = strconv.FormatFloat(v.Float(), 'g', -1, 64)
		node.Tag = "!!float"
	case reflect.Bool:
		node.Value = strconv.FormatBool(v.Bool())
		node.Tag = "!!bool"
	}
}
