package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// =========================================================================
// Duration type
// =========================================================================

func TestDuration_MarshalJSON(t *testing.T) {
	d := Duration(5 * time.Minute)
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}
	if string(data) != `"5m0s"` {
		t.Errorf("expected %q, got %q", `"5m0s"`, string(data))
	}
}

func TestDuration_UnmarshalJSON_String(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`"30m"`), &d); err != nil {
		t.Fatalf("UnmarshalJSON string failed: %v", err)
	}
	if time.Duration(d) != 30*time.Minute {
		t.Errorf("expected 30m, got %v", time.Duration(d))
	}
}

func TestDuration_UnmarshalJSON_StringInvalid(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`"banana"`), &d); err == nil {
		t.Error("expected error for invalid duration string")
	}
}

func TestDuration_UnmarshalJSON_Int(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`60000000000`), &d); err != nil {
		t.Fatalf("UnmarshalJSON int failed: %v", err)
	}
	if time.Duration(d) != time.Minute {
		t.Errorf("expected 1m (60e9 ns), got %v", time.Duration(d))
	}
}

func TestDuration_UnmarshalJSON_Invalid(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`true`), &d); err == nil {
		t.Error("expected error for boolean value")
	}
}

func TestDuration_UnmarshalYAML_String(t *testing.T) {
	var d Duration
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: "1h30m"}
	if err := d.UnmarshalYAML(node); err != nil {
		t.Fatalf("UnmarshalYAML failed: %v", err)
	}
	if time.Duration(d) != 90*time.Minute {
		t.Errorf("expected 1h30m, got %v", time.Duration(d))
	}
}

func TestDuration_UnmarshalYAML_Int(t *testing.T) {
	var d Duration
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: "120000000000"}
	if err := d.UnmarshalYAML(node); err != nil {
		t.Fatalf("UnmarshalYAML int failed: %v", err)
	}
	if time.Duration(d) != 2*time.Minute {
		t.Errorf("expected 2m, got %v", time.Duration(d))
	}
}

func TestDuration_UnmarshalYAML_NotScalar(t *testing.T) {
	var d Duration
	node := &yaml.Node{Kind: yaml.SequenceNode}
	if err := d.UnmarshalYAML(node); err == nil {
		t.Error("expected error for non-scalar YAML node")
	}
}

func TestDuration_String(t *testing.T) {
	d := Duration(90 * time.Second)
	if d.String() != "1m30s" {
		t.Errorf("expected '1m30s', got %q", d.String())
	}
}

// =========================================================================
// DefaultConfig
// =========================================================================

func TestDefaultConfig_Defaults(t *testing.T) {
	c := DefaultConfig()

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"nickname", c.IRC.Nickname, "xdcc-user"},
		{"http.port", c.HTTP.Port, 8080},
		{"http.bind_address", c.HTTP.BindAddress, "127.0.0.1"},
		{"security.token_ttl", c.Security.TokenTTLMinutes, 15},
		{"download.conflict_policy", c.Download.ConflictPolicy, "skip"},
		{"download.fail_fallback", c.Download.FailFallback, "suggest_only"},
		{"download.max_parallel", c.Download.MaxParallelTotal, 5},
		{"download.max_rate", c.Download.MaxRateBPS, int64(0)},
		{"download.min_disk", c.Download.MinDiskSpace, int64(1073741824)},
		{"download.max_retry", c.Download.MaxRetryAttempts, 3},
		{"download.startup_delay", c.Download.StartupDelayMinutes, 0},
		{"download.channel_join_delay", c.Download.ChannelJoinDelay, -1},
		{"search.provider_timeout", c.Search.ProviderTimeout, 5},
		{"search.page_size", c.Search.PageSize, 50},
		{"search.cache.enabled", c.Search.Cache.Enabled, true},
		{"storage.db_path", c.Storage.DBPath, "./db"},
		{"storage.downloads_retention", c.Storage.DownloadsRetention, "30d"},
		{"storage.cleanup_interval", c.Storage.CleanupInterval, "12h"},
		{"storage.busy_timeout_ms", c.Storage.BusyTimeoutMs, 2000},
		{"logging.level", c.Logging.Level, "info"},
		{"ui.setup_completed", c.UI.SetupCompleted, false},
		{"ui.theme", c.UI.Theme, "dark"},
		{"security.admin_token", c.Security.AdminToken, ""},
		{"profiling.block_rate", c.Profiling.BlockProfileRate, 0},
		{"profiling.mutex_fraction", c.Profiling.MutexProfileFraction, 0},
		{"irc.enable_message_send", c.IRC.EnableMessageSend, false},
		{"irc.log_private_messages", c.IRC.LogPrivateMessages, false},
		{"irc.message_rate_limit", c.IRC.MessageRateLimit, 5},
		{"irc.message_rate_window", c.IRC.MessageRateWindowSec, 60},
	}
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestDefaultConfig_DefaultServers(t *testing.T) {
	c := DefaultConfig()
	if len(c.IRC.DefaultServers) != 2 {
		t.Fatalf("expected 2 default servers, got %d", len(c.IRC.DefaultServers))
	}
	if c.IRC.DefaultServers[0].Address != "irc.rizon.net" {
		t.Errorf("first server: %q", c.IRC.DefaultServers[0].Address)
	}
	if c.IRC.DefaultServers[1].Address != "irc.williamgattone.it" {
		t.Errorf("second server: %q", c.IRC.DefaultServers[1].Address)
	}
}

// =========================================================================
// Validate
// =========================================================================

func TestValidate_Valid(t *testing.T) {
	c := DefaultConfig()
	if err := c.Validate(); err != nil {
		t.Errorf("DefaultConfig should be valid: %v", err)
	}
}

func TestValidate_EmptyNickname(t *testing.T) {
	c := DefaultConfig()
	c.IRC.Nickname = ""
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "nickname") {
		t.Errorf("expected error about nickname, got %v", err)
	}
}

func TestValidate_BadPort(t *testing.T) {
	c := DefaultConfig()
	c.HTTP.Port = 99999
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("expected error about port, got %v", err)
	}
}

func TestValidate_ZeroPort(t *testing.T) {
	c := DefaultConfig()
	c.HTTP.Port = 0
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Errorf("expected error about port, got %v", err)
	}
}

func TestValidate_TokenTTLClamps(t *testing.T) {
	c := DefaultConfig()
	c.Security.TokenTTLMinutes = 0
	if err := c.Validate(); err != nil {
		t.Errorf("Validate with TTL=0 should clamp to 15: %v", err)
	}
	if c.Security.TokenTTLMinutes != 15 {
		t.Errorf("TTL should be clamped to 15, got %d", c.Security.TokenTTLMinutes)
	}
}

func TestValidate_EmptyDBPath(t *testing.T) {
	c := DefaultConfig()
	c.Storage.DBPath = ""
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "db_path") {
		t.Errorf("expected error about db_path, got %v", err)
	}
}

func TestValidate_EmptyTempDir(t *testing.T) {
	c := DefaultConfig()
	c.Download.TempDir = ""
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "temp_dir") {
		t.Errorf("expected error about temp_dir, got %v", err)
	}
}

func TestValidate_EmptyDestDir(t *testing.T) {
	c := DefaultConfig()
	c.Download.DestDir = ""
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "dest_dir") {
		t.Errorf("expected error about dest_dir, got %v", err)
	}
}

func TestValidate_BadConflictPolicy(t *testing.T) {
	c := DefaultConfig()
	c.Download.ConflictPolicy = "delete"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "conflict_policy") {
		t.Errorf("expected error about conflict_policy, got %v", err)
	}
}

func TestValidate_ValidConflictPolicies(t *testing.T) {
	for _, p := range []string{"skip", "overwrite", "rename"} {
		c := DefaultConfig()
		c.Download.ConflictPolicy = p
		if err := c.Validate(); err != nil {
			t.Errorf("policy %q should be valid: %v", p, err)
		}
	}
}

func TestValidate_BadFailFallback(t *testing.T) {
	c := DefaultConfig()
	c.Download.FailFallback = "panic"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "fail_fallback") {
		t.Errorf("expected error about fail_fallback, got %v", err)
	}
}

func TestValidate_ValidFailFallbacks(t *testing.T) {
	for _, f := range []string{"suggest_only", "auto_retry_best"} {
		c := DefaultConfig()
		c.Download.FailFallback = f
		if err := c.Validate(); err != nil {
			t.Errorf("fail_fallback %q should be valid: %v", f, err)
		}
	}
}

func TestValidate_MaxParallelZero(t *testing.T) {
	c := DefaultConfig()
	c.Download.MaxParallelTotal = 0
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "max_parallel") {
		t.Errorf("expected error about max_parallel, got %v", err)
	}
}

func TestValidate_StartupDelayNegative(t *testing.T) {
	c := DefaultConfig()
	c.Download.StartupDelayMinutes = -5
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "startup_delay") {
		t.Errorf("expected error about startup_delay, got %v", err)
	}
}

func TestValidate_ChannelJoinDelayInvalid(t *testing.T) {
	c := DefaultConfig()
	c.Download.ChannelJoinDelay = -2
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "channel_join_delay") {
		t.Errorf("expected error about channel_join_delay, got %v", err)
	}
}

func TestValidate_ChannelJoinDelayValid(t *testing.T) {
	for _, d := range []int{-1, 0, 5, 60} {
		c := DefaultConfig()
		c.Download.ChannelJoinDelay = d
		if err := c.Validate(); err != nil {
			t.Errorf("channel_join_delay %d should be valid: %v", d, err)
		}
	}
}

func TestValidate_ProviderTimeoutZero(t *testing.T) {
	c := DefaultConfig()
	c.Search.ProviderTimeout = 0
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "provider_timeout") {
		t.Errorf("expected error about provider_timeout, got %v", err)
	}
}

func TestValidate_PageSizeZero(t *testing.T) {
	c := DefaultConfig()
	c.Search.PageSize = 0
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "page_size") {
		t.Errorf("expected error about page_size, got %v", err)
	}
}

func TestValidate_BadLogLevel(t *testing.T) {
	c := DefaultConfig()
	c.Logging.Level = "verbose"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "logging.level") {
		t.Errorf("expected error about logging.level, got %v", err)
	}
}

func TestValidate_ValidLogLevels(t *testing.T) {
	for _, l := range []string{"debug", "info", "warn", "error", ""} {
		c := DefaultConfig()
		c.Logging.Level = l
		if err := c.Validate(); err != nil {
			t.Errorf("level %q should be valid: %v", l, err)
		}
	}
}

func TestValidate_BadRetention(t *testing.T) {
	c := DefaultConfig()
	c.Storage.DownloadsRetention = "forever"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "downloads_retention") {
		t.Errorf("expected error about downloads_retention, got %v", err)
	}
}

func TestValidate_BadCleanupInterval(t *testing.T) {
	c := DefaultConfig()
	c.Storage.CleanupInterval = "whenever"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "cleanup_interval") {
		t.Errorf("expected error about cleanup_interval, got %v", err)
	}
}

func TestValidate_NegativeBusyTimeout(t *testing.T) {
	c := DefaultConfig()
	c.Storage.BusyTimeoutMs = -1
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "busy_timeout_ms") {
		t.Errorf("expected error about busy_timeout_ms, got %v", err)
	}
}

func TestValidate_EmptyServerAddress(t *testing.T) {
	c := DefaultConfig()
	c.IRC.DefaultServers = []ServerConfig{{Address: "", Port: 6667}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "address must not be empty") {
		t.Errorf("expected error about server address, got %v", err)
	}
}

func TestValidate_InvalidServerPort(t *testing.T) {
	c := DefaultConfig()
	c.IRC.DefaultServers = []ServerConfig{{Address: "irc.test.com", Port: 0}}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "port must be between") {
		t.Errorf("expected error about server port, got %v", err)
	}
}

func TestValidate_EmptyChannelName(t *testing.T) {
	c := DefaultConfig()
	c.IRC.DefaultServers = []ServerConfig{
		{
			Address:  "irc.test.com",
			Port:     6667,
			Channels: []ChannelConfig{{Name: "", AutoJoin: true}},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "channels[0].name") {
		t.Errorf("expected error about channel name, got %v", err)
	}
}

// =========================================================================
// ValidatePartial
// =========================================================================

func TestValidatePartial_Valid(t *testing.T) {
	c := DefaultConfig()
	if err := c.ValidatePartial(); err != nil {
		t.Errorf("DefaultConfig should pass ValidatePartial: %v", err)
	}
}

func TestValidatePartial_EmptyNickname(t *testing.T) {
	c := DefaultConfig()
	c.IRC.Nickname = ""
	if err := c.ValidatePartial(); err == nil {
		t.Error("expected error for empty nickname")
	}
}

func TestValidatePartial_BadPort(t *testing.T) {
	c := DefaultConfig()
	c.HTTP.Port = 99999
	if err := c.ValidatePartial(); err == nil {
		t.Error("expected error for bad port")
	}
}

func TestValidatePartial_EmptyDBPath(t *testing.T) {
	c := DefaultConfig()
	c.Storage.DBPath = ""
	if err := c.ValidatePartial(); err == nil {
		t.Error("expected error for empty db_path")
	}
}

func TestValidatePartial_SkipsOptionalFields(t *testing.T) {
	// ValidatePartial should skip non-critical fields like conflict_policy
	c := DefaultConfig()
	c.Download.ConflictPolicy = "bad"
	if err := c.ValidatePartial(); err != nil {
		t.Errorf("ValidatePartial should skip conflict_policy: %v", err)
	}
}

// =========================================================================
// Clone / Replace / SnapshotAndApply
// =========================================================================

func TestClone_Independent(t *testing.T) {
	original := DefaultConfig()
	clone := original.Clone()

	// Mutate clone
	clone.IRC.Nickname = "mutated"

	// Original should be unchanged
	if original.IRC.Nickname != "xdcc-user" {
		t.Errorf("original nickname mutated: %q", original.IRC.Nickname)
	}
	if clone.IRC.Nickname != "mutated" {
		t.Errorf("clone nickname not set: %q", clone.IRC.Nickname)
	}
}

func TestReplace(t *testing.T) {
	c := DefaultConfig()
	other := DefaultConfig()
	other.IRC.Nickname = "replaced"
	other.HTTP.Port = 3000

	c.Replace(other)

	if c.IRC.Nickname != "replaced" {
		t.Errorf("nickname not replaced: %q", c.IRC.Nickname)
	}
	if c.HTTP.Port != 3000 {
		t.Errorf("port not replaced: %d", c.HTTP.Port)
	}
}

func TestSnapshotAndApply(t *testing.T) {
	c := DefaultConfig()

	old := c.SnapshotAndApply(func(snap *Config) {
		snap.IRC.Nickname = "snapshot_test"
		snap.UI.Theme = "light"
	})

	// Current config should reflect changes
	if c.IRC.Nickname != "snapshot_test" {
		t.Errorf("expected snapshot_test, got %q", c.IRC.Nickname)
	}
	if c.UI.Theme != "light" {
		t.Errorf("expected light theme, got %q", c.UI.Theme)
	}

	// Old should reflect pre-mutation state
	if old.IRC.Nickname != "xdcc-user" {
		t.Errorf("old nickname should be xdcc-user, got %q", old.IRC.Nickname)
	}
	if old.UI.Theme != "dark" {
		t.Errorf("old theme should be dark, got %q", old.UI.Theme)
	}
}

func TestSnapshotAndApply_NilFn(t *testing.T) {
	c := DefaultConfig()
	old := c.SnapshotAndApply(nil)
	if old.UI.Theme != "dark" {
		t.Errorf("old theme should be dark: %q", old.UI.Theme)
	}
	// No mutation → current should still be default
	if c.IRC.Nickname != "xdcc-user" {
		t.Errorf("nickname should be unchanged: %q", c.IRC.Nickname)
	}
}

// =========================================================================
// FlagOverrides
// =========================================================================

func TestFlagOverrides_Apply(t *testing.T) {
	c := DefaultConfig()

	port := 9999
	destDir := "/downloads"
	tempDir := "/tmp"

	f := &FlagOverrides{
		Port:        &port,
		DownloadDir: &destDir,
		TempDir:     &tempDir,
	}
	f.apply(c)

	if c.HTTP.Port != 9999 {
		t.Errorf("port: %d", c.HTTP.Port)
	}
	if c.Download.DestDir != "/downloads" {
		t.Errorf("dest_dir: %q", c.Download.DestDir)
	}
	if c.Download.TempDir != "/tmp" {
		t.Errorf("temp_dir: %q", c.Download.TempDir)
	}
}

func TestFlagOverrides_ApplyNil(t *testing.T) {
	c := DefaultConfig()
	var f *FlagOverrides
	f.apply(c) // should not panic
}

func TestFlagOverrides_ApplyPartial(t *testing.T) {
	c := DefaultConfig()
	port := 7777
	f := &FlagOverrides{Port: &port}
	f.apply(c)

	if c.HTTP.Port != 7777 {
		t.Errorf("port: %d", c.HTTP.Port)
	}
	// Other fields unchanged
	if c.Download.DestDir == "" {
		t.Error("dest_dir should still be default")
	}
}

// =========================================================================
// parseDurationString / ParseDownloadsRetention / ParseCleanupInterval
// =========================================================================

func TestParseDurationString_StandardSuffixes(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
	}{
		{"30d", 30 * 24 * time.Hour},
		{"1d", 24 * time.Hour},
		{"12h", 12 * time.Hour},
		{"45m", 45 * time.Minute},
		{"90s", 90 * time.Second},
		{"1h30m", 90 * time.Minute},
	}
	for _, tt := range tests {
		got, err := parseDurationString(tt.in)
		if err != nil {
			t.Errorf("parseDurationString(%q) error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseDurationString(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseDurationString_Empty(t *testing.T) {
	_, err := parseDurationString("")
	if err == nil {
		t.Error("expected error for empty string")
	}
}

func TestParseDurationString_InvalidDays(t *testing.T) {
	_, err := parseDurationString("abcd")
	if err == nil {
		t.Error("expected error for 'abcd'")
	}
}

func TestParseDownloadsRetention(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"30d", 30},
		{"7d", 7},
		{"48h", 2},
		{"1440m", 1},
	}
	for _, tt := range tests {
		c := DefaultConfig()
		c.Storage.DownloadsRetention = tt.in
		days, err := c.ParseDownloadsRetention()
		if err != nil {
			t.Errorf("ParseDownloadsRetention(%q) error: %v", tt.in, err)
			continue
		}
		if days != tt.want {
			t.Errorf("ParseDownloadsRetention(%q) = %d days, want %d", tt.in, days, tt.want)
		}
	}
}

func TestParseDownloadsRetention_Invalid(t *testing.T) {
	c := DefaultConfig()
	c.Storage.DownloadsRetention = "banana"
	_, err := c.ParseDownloadsRetention()
	if err == nil {
		t.Error("expected error for invalid retention")
	}
}

func TestParseCleanupInterval(t *testing.T) {
	c := DefaultConfig()
	c.Storage.CleanupInterval = "12h"
	d, err := c.ParseCleanupInterval()
	if err != nil {
		t.Fatalf("ParseCleanupInterval: %v", err)
	}
	if d != 12*time.Hour {
		t.Errorf("expected 12h, got %v", d)
	}
}

// =========================================================================
// normalizeChannelName / IsChannelBlacklisted
// =========================================================================

func TestNormalizeChannelName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"#test", "#test"},
		{"TEST", "#test"},
		{"#CAPS", "#caps"},
		{"  #space  ", "#space"},
		{"&local", "&local"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		got := normalizeChannelName(tt.in)
		if got != tt.want {
			t.Errorf("normalizeChannelName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsChannelBlacklisted(t *testing.T) {
	c := &Config{IRC: IRCConfig{ChannelBlacklist: []string{"#spam", "#bad"}}}

	tests := []struct {
		channel string
		want    bool
	}{
		{"#spam", true},
		{"#SPAM", true},
		{"spam", true},
		{"#good", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := c.IsChannelBlacklisted(tt.channel); got != tt.want {
			t.Errorf("IsChannelBlacklisted(%q) = %v, want %v", tt.channel, got, tt.want)
		}
	}
}

func TestIsChannelBlacklisted_EmptyList(t *testing.T) {
	c := &Config{}
	if c.IsChannelBlacklisted("#anything") {
		t.Error("expected false for empty blacklist")
	}
}

// =========================================================================
// Thread-safe accessors
// =========================================================================

func TestAccessors(t *testing.T) {
	c := DefaultConfig()

	if got := c.GetNickname(); got != "xdcc-user" {
		t.Errorf("GetNickname: %q", got)
	}
	if got := c.GetEnableMessageSend(); got != false {
		t.Errorf("GetEnableMessageSend: %v", got)
	}
	if got := c.GetTrustProxy(); got != false {
		t.Errorf("GetTrustProxy: %v", got)
	}
	if got := c.GetTokenTTLMinutes(); got != 15 {
		t.Errorf("GetTokenTTLMinutes: %d", got)
	}
	if got := c.GetUITheme(); got != "dark" {
		t.Errorf("GetUITheme: %q", got)
	}
	if got := c.GetSetupCompleted(); got != false {
		t.Errorf("GetSetupCompleted: %v", got)
	}
	if got := c.GetMaxRateBPS(); got != 0 {
		t.Errorf("GetMaxRateBPS: %d", got)
	}
	if got := c.GetMaxParallelTotal(); got != 5 {
		t.Errorf("GetMaxParallelTotal: %d", got)
	}
	if got := c.GetMinDiskSpace(); got != 1073741824 {
		t.Errorf("GetMinDiskSpace: %d", got)
	}
	if got := c.GetFailFallback(); got != "suggest_only" {
		t.Errorf("GetFailFallback: %q", got)
	}
	if got := c.GetMaxRetryAttempts(); got != 3 {
		t.Errorf("GetMaxRetryAttempts: %d", got)
	}
	if got := c.GetTempDir(); got == "" {
		t.Error("GetTempDir empty")
	}
	if got := c.GetDestDir(); got == "" {
		t.Error("GetDestDir empty")
	}
	if got := c.GetStartupDelayMinutes(); got != 0 {
		t.Errorf("GetStartupDelayMinutes: %d", got)
	}
	if got := c.GetConflictPolicy(); got != "skip" {
		t.Errorf("GetConflictPolicy: %q", got)
	}
	if got := c.GetChannelJoinDelay(); got != -1 {
		t.Errorf("GetChannelJoinDelay: %d", got)
	}
	if got := c.GetLogPrivateMessages(); got != false {
		t.Errorf("GetLogPrivateMessages: %v", got)
	}

	limit, window := c.GetMessageRateLimit()
	if limit != 5 || window != 60 {
		t.Errorf("GetMessageRateLimit: limit=%d window=%d", limit, window)
	}

	dc := c.GetDownloadConfig()
	if dc.MaxParallelTotal != 5 {
		t.Errorf("GetDownloadConfig.MaxParallelTotal: %d", dc.MaxParallelTotal)
	}
}

// =========================================================================
// More env overrides
// =========================================================================

func TestApplyEnvOverrides_HTTPPort(t *testing.T) {
	t.Setenv("XDCC_HTTP_PORT", "3000")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.HTTP.Port != 3000 {
		t.Errorf("expected port 3000, got %d", c.HTTP.Port)
	}
}

func TestApplyEnvOverrides_HTTPPortInvalid(t *testing.T) {
	t.Setenv("XDCC_HTTP_PORT", "abc")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.HTTP.Port != 8080 {
		t.Errorf("port should remain default 8080, got %d", c.HTTP.Port)
	}
}

func TestApplyEnvOverrides_BindAddress(t *testing.T) {
	t.Setenv("XDCC_HTTP_BIND_ADDRESS", "0.0.0.0")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.HTTP.BindAddress != "0.0.0.0" {
		t.Errorf("expected 0.0.0.0, got %q", c.HTTP.BindAddress)
	}
}

func TestApplyEnvOverrides_AdminToken(t *testing.T) {
	t.Setenv("XDCC_SECURITY_ADMIN_TOKEN", "secret123")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Security.AdminToken != "secret123" {
		t.Errorf("expected secret123, got %q", c.Security.AdminToken)
	}
}

func TestApplyEnvOverrides_TokenTTL(t *testing.T) {
	t.Setenv("XDCC_SECURITY_TOKEN_TTL_MINUTES", "60")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Security.TokenTTLMinutes != 60 {
		t.Errorf("expected 60, got %d", c.Security.TokenTTLMinutes)
	}
}

func TestApplyEnvOverrides_DownloadDirs(t *testing.T) {
	t.Setenv("XDCC_DOWNLOAD_TEMP_DIR", "/custom/tmp")
	t.Setenv("XDCC_DOWNLOAD_DEST_DIR", "/custom/complete")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Download.TempDir != "/custom/tmp" {
		t.Errorf("temp: %q", c.Download.TempDir)
	}
	if c.Download.DestDir != "/custom/complete" {
		t.Errorf("dest: %q", c.Download.DestDir)
	}
}

func TestApplyEnvOverrides_ConflictPolicy(t *testing.T) {
	t.Setenv("XDCC_DOWNLOAD_CONFLICT_POLICY", "rename")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Download.ConflictPolicy != "rename" {
		t.Errorf("expected rename, got %q", c.Download.ConflictPolicy)
	}
}

func TestApplyEnvOverrides_FailFallback(t *testing.T) {
	t.Setenv("XDCC_DOWNLOAD_FAIL_FALLBACK", "auto_retry_best")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Download.FailFallback != "auto_retry_best" {
		t.Errorf("expected auto_retry_best, got %q", c.Download.FailFallback)
	}
}

func TestApplyEnvOverrides_MaxParallel(t *testing.T) {
	t.Setenv("XDCC_DOWNLOAD_MAX_PARALLEL", "10")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Download.MaxParallelTotal != 10 {
		t.Errorf("expected 10, got %d", c.Download.MaxParallelTotal)
	}
}

func TestApplyEnvOverrides_MaxRateBPS(t *testing.T) {
	t.Setenv("XDCC_DOWNLOAD_MAX_RATE_BPS", "1048576")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Download.MaxRateBPS != 1048576 {
		t.Errorf("expected 1048576, got %d", c.Download.MaxRateBPS)
	}
}

func TestApplyEnvOverrides_MinDiskSpace(t *testing.T) {
	t.Setenv("XDCC_DOWNLOAD_MIN_DISK_SPACE", "536870912")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Download.MinDiskSpace != 536870912 {
		t.Errorf("expected 536870912, got %d", c.Download.MinDiskSpace)
	}
}

func TestApplyEnvOverrides_MaxRetry(t *testing.T) {
	t.Setenv("XDCC_DOWNLOAD_MAX_RETRY", "5")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Download.MaxRetryAttempts != 5 {
		t.Errorf("expected 5, got %d", c.Download.MaxRetryAttempts)
	}
}

func TestApplyEnvOverrides_StartupDelay(t *testing.T) {
	t.Setenv("XDCC_DOWNLOAD_STARTUP_DELAY_MINUTES", "2")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Download.StartupDelayMinutes != 2 {
		t.Errorf("expected 2, got %d", c.Download.StartupDelayMinutes)
	}
}

func TestApplyEnvOverrides_ChannelJoinDelay(t *testing.T) {
	t.Setenv("XDCC_DOWNLOAD_CHANNEL_JOIN_DELAY", "3")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Download.ChannelJoinDelay != 3 {
		t.Errorf("expected 3, got %d", c.Download.ChannelJoinDelay)
	}
}

func TestApplyEnvOverrides_SearchTimeout(t *testing.T) {
	t.Setenv("XDCC_SEARCH_PROVIDER_TIMEOUT", "10")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Search.ProviderTimeout != 10 {
		t.Errorf("expected 10, got %d", c.Search.ProviderTimeout)
	}
}

func TestApplyEnvOverrides_SearchPageSize(t *testing.T) {
	t.Setenv("XDCC_SEARCH_PAGE_SIZE", "25")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Search.PageSize != 25 {
		t.Errorf("expected 25, got %d", c.Search.PageSize)
	}
}

func TestApplyEnvOverrides_SearchCacheEnabled(t *testing.T) {
	tests := []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"1", true},
		{"false", false},
		{"0", false},
	}
	for _, tt := range tests {
		t.Setenv("XDCC_SEARCH_CACHE_ENABLED", tt.val)
		c := DefaultConfig()
		c.applyEnvOverrides()
		if c.Search.Cache.Enabled != tt.want {
			t.Errorf("search cache enabled with env=%q: got %v, want %v", tt.val, c.Search.Cache.Enabled, tt.want)
		}
	}
}

func TestApplyEnvOverrides_StorageDBPath(t *testing.T) {
	t.Setenv("XDCC_STORAGE_DB_PATH", "/data/db")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Storage.DBPath != "/data/db" {
		t.Errorf("expected /data/db, got %q", c.Storage.DBPath)
	}
}

func TestApplyEnvOverrides_StorageRetention(t *testing.T) {
	t.Setenv("XDCC_STORAGE_DOWNLOADS_RETENTION", "60d")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Storage.DownloadsRetention != "60d" {
		t.Errorf("expected 60d, got %q", c.Storage.DownloadsRetention)
	}
}

func TestApplyEnvOverrides_StorageCleanupInterval(t *testing.T) {
	t.Setenv("XDCC_STORAGE_CLEANUP_INTERVAL", "6h")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Storage.CleanupInterval != "6h" {
		t.Errorf("expected 6h, got %q", c.Storage.CleanupInterval)
	}
}

func TestApplyEnvOverrides_StorageBusyTimeout(t *testing.T) {
	t.Setenv("XDCC_STORAGE_BUSY_TIMEOUT_MS", "5000")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Storage.BusyTimeoutMs != 5000 {
		t.Errorf("expected 5000, got %d", c.Storage.BusyTimeoutMs)
	}
}

func TestApplyEnvOverrides_LoggingLevel(t *testing.T) {
	t.Setenv("XDCC_LOGGING_LEVEL", "debug")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Logging.Level != "debug" {
		t.Errorf("expected debug, got %q", c.Logging.Level)
	}
}

func TestApplyEnvOverrides_LoggingFilePath(t *testing.T) {
	t.Setenv("XDCC_LOGGING_FILE_PATH", "/var/log/xdcc.log")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Logging.FilePath != "/var/log/xdcc.log" {
		t.Errorf("expected /var/log/xdcc.log, got %q", c.Logging.FilePath)
	}
}

func TestApplyEnvOverrides_UISetupCompleted(t *testing.T) {
	t.Setenv("XDCC_UI_SETUP_COMPLETED", "true")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if !c.UI.SetupCompleted {
		t.Error("expected setup_completed=true")
	}
}

func TestApplyEnvOverrides_UITheme(t *testing.T) {
	t.Setenv("XDCC_UI_THEME", "light")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.UI.Theme != "light" {
		t.Errorf("expected light, got %q", c.UI.Theme)
	}
}

func TestApplyEnvOverrides_Profiling(t *testing.T) {
	t.Setenv("XDCC_PROFILING_BLOCK_RATE", "1")
	t.Setenv("XDCC_PROFILING_MUTEX_FRACTION", "10")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.Profiling.BlockProfileRate != 1 {
		t.Errorf("block rate: %d", c.Profiling.BlockProfileRate)
	}
	if c.Profiling.MutexProfileFraction != 10 {
		t.Errorf("mutex fraction: %d", c.Profiling.MutexProfileFraction)
	}
}

func TestApplyEnvOverrides_Nickname(t *testing.T) {
	t.Setenv("XDCC_IRC_NICKNAME", "custom-bot")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if c.IRC.Nickname != "custom-bot" {
		t.Errorf("expected custom-bot, got %q", c.IRC.Nickname)
	}
}

func TestApplyEnvOverrides_GreetingsWithBlanks(t *testing.T) {
	t.Setenv("XDCC_IRC_GREETINGS", "hello,,world")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if len(c.IRC.Greetings) != 2 {
		t.Fatalf("expected 2 greetings (blank filtered), got %d: %v", len(c.IRC.Greetings), c.IRC.Greetings)
	}
	if c.IRC.Greetings[0] != "hello" || c.IRC.Greetings[1] != "world" {
		t.Errorf("unexpected: %v", c.IRC.Greetings)
	}
}

// =========================================================================
// Save / SaveRaw persistence
// =========================================================================

func TestSave_CreatesFile(t *testing.T) {
	c := DefaultConfig()
	path := t.TempDir() + "/config.yaml"

	if err := c.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	if !strings.Contains(string(data), "nickname") {
		t.Error("saved YAML should contain 'nickname'")
	}
}

func TestSave_EmptyPath(t *testing.T) {
	c := DefaultConfig()
	if err := c.Save(""); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestSaveRaw_EmptyPath(t *testing.T) {
	c := DefaultConfig()
	if err := c.SaveRaw("", []byte("test")); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestSaveRaw_WritesFile(t *testing.T) {
	c := DefaultConfig()
	path := t.TempDir() + "/config.yaml"

	yamlBytes := []byte("irc:\n  nickname: test\n")
	if err := c.SaveRaw(path, yamlBytes); err != nil {
		t.Fatalf("SaveRaw failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	if !strings.Contains(string(data), "nickname: test") {
		t.Errorf("unexpected content: %s", string(data))
	}
}

func TestSave_CreatesBackup(t *testing.T) {
	c := DefaultConfig()
	path := t.TempDir() + "/config.yaml"

	// First save
	if err := c.Save(path); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	// Second save → should create .bak
	if err := c.Save(path); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	bakPath := path + ".bak"
	if _, err := os.Stat(bakPath); os.IsNotExist(err) {
		t.Error("backup file should exist after second save")
	}
}

// =========================================================================
// Load
// =========================================================================

func TestLoad_NoFile(t *testing.T) {
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load with empty path: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.IRC.Nickname != "xdcc-user" {
		t.Errorf("expected default nickname, got %q", cfg.IRC.Nickname)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml", nil)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoad_FromYAML(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	yamlContent := "irc:\n  nickname: yaml-bot\nhttp:\n  port: 9090\n"
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.IRC.Nickname != "yaml-bot" {
		t.Errorf("expected yaml-bot, got %q", cfg.IRC.Nickname)
	}
	if cfg.HTTP.Port != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.HTTP.Port)
	}
}

func TestLoad_WithFlagOverrides(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	yamlContent := "irc:\n  nickname: yaml-bot\nhttp:\n  port: 9090\n"
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	port := 9999
	flags := &FlagOverrides{Port: &port}

	cfg, err := Load(path, flags)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// YAML value
	if cfg.IRC.Nickname != "yaml-bot" {
		t.Errorf("expected yaml-bot, got %q", cfg.IRC.Nickname)
	}
	// Flag override takes priority over YAML
	if cfg.HTTP.Port != 9999 {
		t.Errorf("expected flag override port 9999, got %d", cfg.HTTP.Port)
	}
}

func TestLoad_WithEnvOverrides(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	yamlContent := "irc:\n  nickname: yaml-bot\n"
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	t.Setenv("XDCC_IRC_NICKNAME", "env-bot")

	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Env override takes priority over YAML
	if cfg.IRC.Nickname != "env-bot" {
		t.Errorf("expected env override 'env-bot', got %q", cfg.IRC.Nickname)
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte("this: is: not: valid: yaml: [[["), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	_, err := Load(path, nil)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoad_ValidationError(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	yamlContent := "irc:\n  nickname: ''\n"
	if err := os.WriteFile(path, []byte(yamlContent), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	_, err := Load(path, nil)
	if err == nil {
		t.Error("expected validation error for empty nickname")
	}
}

// =========================================================================
// Path expansion
// =========================================================================

func TestExpandPath_Absolute(t *testing.T) {
	got := expandPath("/absolute/path")
	if got != "/absolute/path" {
		t.Errorf("absolute path should be unchanged: %q", got)
	}
}

func TestExpandPath_Tilde(t *testing.T) {
	got := expandPath("~/downloads")
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(got, "~/") {
		t.Errorf("tilde should be expanded: %q", got)
	}
	if home != "" && got != home+"/downloads" {
		t.Logf("tilde expanded to %q (home=%q)", got, home)
	}
}

func TestExpandPath_Relative(t *testing.T) {
	got := expandPath("downloads")
	if filepath.IsAbs(got) {
		t.Logf("relative path expanded to absolute: %q", got)
	}
}

// TestPickGreeting_EmptyListReturnsDefault verifies the spec: when the
// configured greetings list is empty, "hello everybody" is returned.
func TestPickGreeting_EmptyListReturnsDefault(t *testing.T) {
	c := &Config{}
	got := c.PickGreeting()
	if got != DefaultGreeting {
		t.Errorf("expected default greeting %q, got %q", DefaultGreeting, got)
	}
	if DefaultGreeting != "hello everybody" {
		t.Errorf("DefaultGreeting constant changed: got %q, want %q", DefaultGreeting, "hello everybody")
	}
}

// TestPickGreeting_NilListReturnsDefault verifies the spec for a nil slice too.
func TestPickGreeting_NilListReturnsDefault(t *testing.T) {
	c := &Config{IRC: IRCConfig{Greetings: nil}}
	got := c.PickGreeting()
	if got != DefaultGreeting {
		t.Errorf("expected default greeting %q, got %q", DefaultGreeting, got)
	}
}

// TestPickGreeting_SingleEntryAlwaysReturnsIt verifies that with a single
// configured greeting, that one is always returned.
func TestPickGreeting_SingleEntryAlwaysReturnsIt(t *testing.T) {
	c := &Config{IRC: IRCConfig{Greetings: []string{"ciao a tutti"}}}
	for i := 0; i < 10; i++ {
		if got := c.PickGreeting(); got != "ciao a tutti" {
			t.Errorf("expected configured greeting, got %q", got)
		}
	}
}

// TestPickGreeting_PicksFromConfiguredList verifies that with multiple
// configured greetings, all of them can be picked (statistical check).
func TestPickGreeting_PicksFromConfiguredList(t *testing.T) {
	list := []string{"hi", "hello", "ciao", "salve", "buongiorno"}
	// Build a set for O(1) membership checks.
	listSet := make(map[string]bool, len(list))
	for _, s := range list {
		listSet[s] = true
	}
	c := &Config{IRC: IRCConfig{Greetings: list}}

	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		got := c.PickGreeting()
		// Every returned greeting must be in the configured list.
		if !listSet[got] {
			t.Fatalf("PickGreeting returned %q, which is not in the configured list", got)
		}
		seen[got] = true
	}

	// With 200 draws and 5 entries we should see at least 2 distinct
	// greetings (probability of seeing only one is essentially zero).
	if len(seen) < 2 {
		t.Errorf("expected to see multiple greetings over 200 draws, saw %d distinct: %v", len(seen), seen)
	}
}

// TestPickGreeting_IgnoresBlankEntries ensures a list of one empty string
// still falls back to the default greeting rather than sending a blank
// message. (Empty entries in the user-provided list are not filtered here
// because the spec is "if the list doesn't exist use the default"; we
// interpret that strictly: a non-empty list with only blanks still uses
// the configured list verbatim. So this test only ensures the empty list
// itself is the trigger.)
func TestPickGreeting_EmptyListExplicitReturnsDefault(t *testing.T) {
	c := &Config{IRC: IRCConfig{Greetings: []string{}}}
	if got := c.PickGreeting(); got != DefaultGreeting {
		t.Errorf("expected default greeting, got %q", got)
	}
}

// TestApplyEnvOverrides_Greetings verifies that XDCC_IRC_GREETINGS, when
// set, is parsed as a comma-separated list of greeting phrases.
func TestApplyEnvOverrides_Greetings(t *testing.T) {
	t.Setenv("XDCC_IRC_GREETINGS", "ciao,  hello ,buongiorno")
	c := DefaultConfig()
	c.applyEnvOverrides()

	if len(c.IRC.Greetings) != 3 {
		t.Fatalf("expected 3 greetings, got %d (%v)", len(c.IRC.Greetings), c.IRC.Greetings)
	}
	want := []string{"ciao", "hello", "buongiorno"}
	for i, w := range want {
		if c.IRC.Greetings[i] != w {
			t.Errorf("greeting[%d]: expected %q, got %q", i, w, c.IRC.Greetings[i])
		}
	}
}

// TestApplyEnvOverrides_GreetingsUnsetKeepsEmpty verifies that when the
// environment variable is not set the configured list is preserved as-is.
func TestApplyEnvOverrides_GreetingsUnsetKeepsEmpty(t *testing.T) {
	// Make sure the variable is not set.
	t.Setenv("XDCC_IRC_GREETINGS", "")

	c := DefaultConfig()
	c.applyEnvOverrides()

	if len(c.IRC.Greetings) != 0 {
		t.Errorf("expected greetings to remain empty, got %v", c.IRC.Greetings)
	}
}

// TestDefaultConfig_HasGreetingsField ensures the DefaultConfig initializes
// the Greetings field as an empty slice (so YAML serialization is stable and
// "no greetings configured" is the documented default).
func TestDefaultConfig_HasGreetingsField(t *testing.T) {
	c := DefaultConfig()
	if c.IRC.Greetings == nil {
		t.Error("DefaultConfig().IRC.Greetings is nil, expected empty slice")
	}
	if len(c.IRC.Greetings) != 0 {
		t.Errorf("expected empty greetings, got %v", c.IRC.Greetings)
	}
}

// TestApplyEnvOverrides_TrustProxy verifies that XDCC_HTTP_TRUST_PROXY
// sets the TrustProxy field correctly for both "true" and "false" values.
func TestApplyEnvOverrides_TrustProxy(t *testing.T) {
	// "true" → true
	t.Setenv("XDCC_HTTP_TRUST_PROXY", "true")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if !c.HTTP.TrustProxy {
		t.Error("expected TrustProxy=true when env is 'true'")
	}

	// "1" → true
	t.Setenv("XDCC_HTTP_TRUST_PROXY", "1")
	c = DefaultConfig()
	c.applyEnvOverrides()
	if !c.HTTP.TrustProxy {
		t.Error("expected TrustProxy=true when env is '1'")
	}

	// "false" → false
	t.Setenv("XDCC_HTTP_TRUST_PROXY", "false")
	c = DefaultConfig()
	c.applyEnvOverrides()
	if c.HTTP.TrustProxy {
		t.Error("expected TrustProxy=false when env is 'false'")
	}

	// unset → false (default)
	t.Setenv("XDCC_HTTP_TRUST_PROXY", "")
	c = DefaultConfig()
	c.applyEnvOverrides()
	if c.HTTP.TrustProxy {
		t.Error("expected TrustProxy=false when env is unset")
	}
}

// TestDefaultConfig_TrustProxy verifies the default is false.
func TestDefaultConfig_TrustProxy(t *testing.T) {
	c := DefaultConfig()
	if c.HTTP.TrustProxy {
		t.Error("DefaultConfig().HTTP.TrustProxy should be false")
	}
}

// TestApplyEnvOverrides_EnableMessageSend verifies the env override for enable_message_send.
func TestApplyEnvOverrides_EnableMessageSend(t *testing.T) {
	t.Setenv("XDCC_IRC_ENABLE_MESSAGE_SEND", "true")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if !c.IRC.EnableMessageSend {
		t.Error("expected EnableMessageSend=true when env is 'true'")
	}

	t.Setenv("XDCC_IRC_ENABLE_MESSAGE_SEND", "0")
	c = DefaultConfig()
	c.applyEnvOverrides()
	if c.IRC.EnableMessageSend {
		t.Error("expected EnableMessageSend=false when env is '0'")
	}
}

// TestApplyEnvOverrides_LogPrivateMessages verifies the env override.
func TestApplyEnvOverrides_LogPrivateMessages(t *testing.T) {
	t.Setenv("XDCC_IRC_LOG_PRIVATE_MESSAGES", "1")
	c := DefaultConfig()
	c.applyEnvOverrides()
	if !c.IRC.LogPrivateMessages {
		t.Error("expected LogPrivateMessages=true when env is '1'")
	}
}

// TestIsChannelLogged verifies the IsChannelLogged helper.
func TestIsChannelLogged(t *testing.T) {
	c := &Config{IRC: IRCConfig{ChannelLog: []string{"#general", "&local"}}}

	tests := []struct {
		channel string
		want    bool
	}{
		{"#general", true},
		{"#GENERAL", true}, // case-insensitive
		{"general", true},  // no '#' prefix → normalized
		{"&local", true},
		{"#other", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := c.IsChannelLogged(tt.channel); got != tt.want {
			t.Errorf("IsChannelLogged(%q) = %v, want %v", tt.channel, got, tt.want)
		}
	}
}

// TestIsChannelLogged_EmptyList verifies empty list returns false for everything.
func TestIsChannelLogged_EmptyList(t *testing.T) {
	c := &Config{}
	if c.IsChannelLogged("#general") {
		t.Error("expected false when ChannelLog is nil")
	}
}

// TestGetConfigRevision_BumpsOnSave verifies that the revision counter
// is incremented atomically each time the config is persisted to disk
// via Save, SaveRaw, or ApplyPartial.
func TestGetConfigRevision_BumpsOnSave(t *testing.T) {
	c := DefaultConfig()

	// Initial revision is 0.
	if rev := c.GetConfigRevision(); rev != 0 {
		t.Fatalf("initial revision = %d, want 0", rev)
	}

	path := t.TempDir() + "/config.yaml"

	// Save() should bump revision.
	if err := c.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if rev := c.GetConfigRevision(); rev != 1 {
		t.Errorf("after Save: revision = %d, want 1", rev)
	}

	// SaveRaw() with valid YAML should bump revision.
	yamlBytes := []byte("irc:\n  nickname: test\nui:\n  theme: dark\n")
	if err := c.SaveRaw(path, yamlBytes); err != nil {
		t.Fatalf("SaveRaw failed: %v", err)
	}
	if rev := c.GetConfigRevision(); rev != 2 {
		t.Errorf("after SaveRaw: revision = %d, want 2", rev)
	}

	// ApplyPartial with a scalar change should bump revision.
	// The YAML file already has ui.theme=dark, so the change can be
	// surgically applied without falling back to saveUnlocked.
	old := c.Clone()
	c.SnapshotAndApply(func(snap *Config) {
		snap.UI.Theme = "light"
	})
	if err := c.ApplyPartial(path, old); err != nil {
		t.Fatalf("ApplyPartial failed: %v", err)
	}
	if rev := c.GetConfigRevision(); rev != 3 {
		t.Errorf("after ApplyPartial: revision = %d, want 3", rev)
	}
}

// TestGetConfigRevision_NoBumpOnNoChange verifies that ApplyPartial does
// not bump the revision when there are no changes to persist.
func TestGetConfigRevision_NoBumpOnNoChange(t *testing.T) {
	c := DefaultConfig()
	path := t.TempDir() + "/config.yaml"

	// First save to create the file and set initial revision.
	if err := c.Save(path); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if rev := c.GetConfigRevision(); rev != 1 {
		t.Fatalf("after Save: revision = %d, want 1", rev)
	}

	// ApplyPartial with no changes (old == current) should NOT bump.
	old := c.Clone()
	if err := c.ApplyPartial(path, old); err != nil {
		t.Fatalf("ApplyPartial failed: %v", err)
	}
	if rev := c.GetConfigRevision(); rev != 1 {
		t.Errorf("after no-op ApplyPartial: revision = %d, want 1 (no bump)", rev)
	}
}

// TestGetConfigRevision_NoBumpOnError verifies that SaveRaw does not bump
// the revision when the write fails (e.g., path is empty).
func TestGetConfigRevision_NoBumpOnError(t *testing.T) {
	c := DefaultConfig()

	if rev := c.GetConfigRevision(); rev != 0 {
		t.Fatalf("initial revision = %d, want 0", rev)
	}

	// SaveRaw with empty path should return an error and NOT bump.
	if err := c.SaveRaw("", []byte("x")); err == nil {
		t.Fatal("expected error from SaveRaw with empty path, got nil")
	}
	if rev := c.GetConfigRevision(); rev != 0 {
		t.Errorf("after failed SaveRaw: revision = %d, want 0 (no bump)", rev)
	}
}

// TestGetConfigRevision_Monotonic ensures revisions increase monotonically
// across multiple write operations.
func TestGetConfigRevision_Monotonic(t *testing.T) {
	c := DefaultConfig()
	path := t.TempDir() + "/config.yaml"

	var lastRev int64
	for i := 0; i < 5; i++ {
		// Alternate Save and SaveRaw
		if i%2 == 0 {
			if err := c.Save(path); err != nil {
				t.Fatalf("Save #%d failed: %v", i, err)
			}
		} else {
			if err := c.SaveRaw(path, []byte("irc:\n  nickname: test"+fmt.Sprint(i)+"\n")); err != nil {
				t.Fatalf("SaveRaw #%d failed: %v", i, err)
			}
		}

		rev := c.GetConfigRevision()
		if rev <= lastRev {
			t.Errorf("iteration %d: revision %d <= previous %d (must be strictly increasing)", i, rev, lastRev)
		}
		lastRev = rev
	}

	if lastRev != 5 {
		t.Errorf("after 5 writes: revision = %d, want 5", lastRev)
	}
}

// TestGetConfigRevision_ConcurrentAccess verifies that GetConfigRevision
// can be called concurrently with writes without panicking.
func TestGetConfigRevision_ConcurrentAccess(t *testing.T) {
	c := DefaultConfig()
	path := t.TempDir() + "/config.yaml"

	// First save to create the file.
	if err := c.Save(path); err != nil {
		t.Fatalf("initial Save failed: %v", err)
	}

	done := make(chan struct{})

	// Writer goroutine: repeatedly calls Save.
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 20; i++ {
			_ = c.Save(path)
		}
	}()

	// Reader goroutines: repeatedly read the revision.
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				_ = c.GetConfigRevision()
			}
		}()
	}

	for i := 0; i < 5; i++ {
		<-done
	}
}

// TestApplyPartial_FallbackOnMissingKey verifies that ApplyPartial falls
// back to saveUnlocked when the key to update doesn't exist in the YAML
// file. The revision still bumps because saveUnlocked increments it.
func TestApplyPartial_FallbackOnMissingKey(t *testing.T) {
	c := DefaultConfig()
	path := t.TempDir() + "/config.yaml"

	// Write a minimal YAML file that lacks the "ui" section.
	yamlBytes := []byte("irc:\n  nickname: test\n")
	if err := c.SaveRaw(path, yamlBytes); err != nil {
		t.Fatalf("SaveRaw failed: %v", err)
	}
	if rev := c.GetConfigRevision(); rev != 1 {
		t.Fatalf("after SaveRaw: revision = %d, want 1", rev)
	}

	// Change ui.theme — this key doesn't exist in the YAML file, so
	// ApplyPartial should fall back to saveUnlocked, which bumps revision.
	old := c.Clone()
	c.SnapshotAndApply(func(snap *Config) {
		snap.UI.Theme = "light"
	})
	if err := c.ApplyPartial(path, old); err != nil {
		t.Fatalf("ApplyPartial failed: %v", err)
	}
	if rev := c.GetConfigRevision(); rev != 2 {
		t.Errorf("after ApplyPartial (missing key fallback): revision = %d, want 2", rev)
	}

	// The file should now contain all fields (from the saveUnlocked fallback).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "light") {
		t.Error("expected 'light' theme in config after saveUnlocked fallback")
	}
}

// TestApplyPartial_FallbackOnNonScalar verifies that ApplyPartial falls
// back to saveUnlocked when a change involves a non-scalar type (e.g., a
// slice like greetings). The revision still bumps.
func TestApplyPartial_FallbackOnNonScalar(t *testing.T) {
	c := DefaultConfig()
	path := t.TempDir() + "/config.yaml"

	// Write a YAML file that includes the greetings field.
	yamlBytes := []byte("irc:\n  nickname: test\n  greetings:\n    - hi\n    - hello\n")
	if err := c.SaveRaw(path, yamlBytes); err != nil {
		t.Fatalf("SaveRaw failed: %v", err)
	}
	if rev := c.GetConfigRevision(); rev != 1 {
		t.Fatalf("after SaveRaw: revision = %d, want 1", rev)
	}

	// Change greetings (a slice — non-scalar) — ApplyPartial should detect
	// this and fall back to saveUnlocked, which bumps revision.
	old := c.Clone()
	c.SnapshotAndApply(func(snap *Config) {
		snap.IRC.Greetings = []string{"ciao", "buongiorno"}
	})
	if err := c.ApplyPartial(path, old); err != nil {
		t.Fatalf("ApplyPartial failed: %v", err)
	}
	if rev := c.GetConfigRevision(); rev != 2 {
		t.Errorf("after ApplyPartial (non-scalar fallback): revision = %d, want 2", rev)
	}

	// The file should contain the new greetings from the full rewrite.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "ciao") {
		t.Error("expected 'ciao' in config after saveUnlocked fallback")
	}
}
