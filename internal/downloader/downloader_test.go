package downloader

import (
	"testing"

	"xdcc_server/internal/entities"
	xdccirc "xdcc_server/internal/irc"
)

// ---------------------------------------------------------------------------
// groupByServer
// ---------------------------------------------------------------------------

func makePack(bot, server string, packNum int) *entities.XDCCPack {
	return &entities.XDCCPack{
		Bot:        bot,
		PackNumber: packNum,
		Server:     entities.IrcServer{Address: server, Port: 6667},
	}
}

func TestGroupByServerEmpty(t *testing.T) {
	groups := groupByServer(nil)
	if groups != nil {
		t.Errorf("expected nil for empty packs, got %v", groups)
	}

	groups = groupByServer([]*entities.XDCCPack{})
	if groups != nil {
		t.Errorf("expected nil for empty slice, got %v", groups)
	}
}

func TestGroupByServerSingle(t *testing.T) {
	packs := []*entities.XDCCPack{
		makePack("Bot1", "irc.example.com", 1),
	}
	groups := groupByServer(packs)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0]) != 1 {
		t.Fatalf("expected 1 pack in group, got %d", len(groups[0]))
	}
}

func TestGroupByServerSameServer(t *testing.T) {
	packs := []*entities.XDCCPack{
		makePack("Bot1", "irc.example.com", 1),
		makePack("Bot2", "irc.example.com", 2),
		makePack("Bot3", "irc.example.com", 3),
	}
	groups := groupByServer(packs)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0]) != 3 {
		t.Fatalf("expected 3 packs in group, got %d", len(groups[0]))
	}
}

func TestGroupByServerDifferentServers(t *testing.T) {
	packs := []*entities.XDCCPack{
		makePack("Bot1", "irc.one.com", 1),
		makePack("Bot2", "irc.two.com", 2),
		makePack("Bot3", "irc.one.com", 3),
	}
	groups := groupByServer(packs)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	if len(groups[0]) != 1 {
		t.Errorf("group 0: expected 1 pack, got %d", len(groups[0]))
	}
	if len(groups[1]) != 1 {
		t.Errorf("group 1: expected 1 pack, got %d", len(groups[1]))
	}
	if len(groups[2]) != 1 {
		t.Errorf("group 2: expected 1 pack, got %d", len(groups[2]))
	}
}

func TestGroupByServerAlternating(t *testing.T) {
	packs := []*entities.XDCCPack{
		makePack("Bot1", "irc.one.com", 1),
		makePack("Bot1", "irc.one.com", 2), // consecutive same server → same group
		makePack("Bot2", "irc.two.com", 3),
		makePack("Bot2", "irc.two.com", 4), // consecutive same → same group
		makePack("Bot3", "irc.one.com", 5),
	}
	groups := groupByServer(packs)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Errorf("group 0: expected 2 packs, got %d", len(groups[0]))
	}
	if len(groups[1]) != 2 {
		t.Errorf("group 1: expected 2 packs, got %d", len(groups[1]))
	}
	if len(groups[2]) != 1 {
		t.Errorf("group 2: expected 1 pack, got %d", len(groups[2]))
	}

	// Verify order preserved
	if groups[0][0].PackNumber != 1 || groups[0][1].PackNumber != 2 {
		t.Errorf("group 0: unexpected pack order")
	}
	if groups[1][0].PackNumber != 3 || groups[1][1].PackNumber != 4 {
		t.Errorf("group 1: unexpected pack order")
	}
	if groups[2][0].PackNumber != 5 {
		t.Errorf("group 2: unexpected pack order")
	}
}

// ---------------------------------------------------------------------------
// Error classification via PackResult
// ---------------------------------------------------------------------------

func TestPackResultErrorTypes(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		isWrapped bool
	}{
		{"ErrAlreadyDownloaded", xdccirc.ErrAlreadyDownloaded, true},
		{"ErrBotDenied", xdccirc.ErrBotDenied, true},
		{"ErrBotNotFound", xdccirc.ErrBotNotFound, true},
		{"ErrServerUnreachable", xdccirc.ErrServerUnreachable, true},
		{"ErrUnrecoverable", xdccirc.ErrUnrecoverable, true},
		{"ErrCancelled", xdccirc.ErrCancelled, true},
		{"ErrTimeout", xdccirc.ErrTimeout, true},
		{"ErrDownloadFailed", xdccirc.ErrDownloadFailed, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := xdccirc.PackResult{Error: tt.err}
			if r.Error == nil {
				t.Errorf("expected non-nil error for %s", tt.name)
			}
		})
	}
}

func TestPackResultSuccess(t *testing.T) {
	r := xdccirc.PackResult{
		FilePath: "/tmp/test.mkv",
		Filename: "test.mkv",
		FileSize: 123456789,
	}
	if r.Error != nil {
		t.Errorf("expected nil error, got %v", r.Error)
	}
	if r.FilePath != "/tmp/test.mkv" {
		t.Errorf("unexpected FilePath: %q", r.FilePath)
	}
	if r.Filename != "test.mkv" {
		t.Errorf("unexpected Filename: %q", r.Filename)
	}
	if r.FileSize != 123456789 {
		t.Errorf("unexpected FileSize: %d", r.FileSize)
	}
}

func TestPackResultWithLastBotNotice(t *testing.T) {
	r := xdccirc.PackResult{
		Error:         xdccirc.ErrBotDenied,
		LastBotNotice: "All slots full, try again later",
	}
	if r.LastBotNotice == "" {
		t.Error("expected non-empty LastBotNotice")
	}
}
