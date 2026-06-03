package entities

import "testing"

// --- resolveServer -----------------------------------------------------------

func TestResolveServer_TLTAlwaysMapped(t *testing.T) {
	// TLT bots MUST always connect to irc.williamgattone.it, regardless of
	// what fallback server is passed. The only override is the explicit
	// --server CLI flag, which is applied at the CLI level after resolveServer.
	for _, fallback := range []string{"irc.rizon.net", "irc.custom.net", "irc.other.net", ""} {
		s := ResolveServer("TLTBot", fallback)
		if s.Address != "irc.williamgattone.it" {
			t.Errorf("ResolveServer(TLTBot, %q) = %s, want irc.williamgattone.it", fallback, s.Address)
		}
	}
}

func TestResolveServer_TLTPrefix(t *testing.T) {
	s := ResolveServer("TLTBot", "irc.rizon.net")
	if s.Address != "irc.williamgattone.it" {
		t.Errorf("expected irc.williamgattone.it, got %s", s.Address)
	}
}

func TestResolveServer_WeCPrefix(t *testing.T) {
	// WeC bots MUST always connect to irc.explosionirc.net, regardless of fallback.
	for _, fallback := range []string{"irc.rizon.net", "irc.custom.net", ""} {
		s := ResolveServer("WeCBot", fallback)
		if s.Address != "irc.explosionirc.net" {
			t.Errorf("ResolveServer(WeCBot, %q) = %s, want irc.explosionirc.net", fallback, s.Address)
		}
	}
}

func TestResolveServer_Default(t *testing.T) {
	s := ResolveServer("SomeBot", "irc.rizon.net")
	if s.Address != "irc.rizon.net" {
		t.Errorf("expected irc.rizon.net, got %s", s.Address)
	}
}

func TestResolveServer_DefaultEmptyFallback(t *testing.T) {
	// Empty fallback should default to irc.rizon.net for non-TLT/WeC bots.
	s := ResolveServer("SomeBot", "")
	if s.Address != "irc.rizon.net" {
		t.Errorf("expected irc.rizon.net, got %s", s.Address)
	}
}

func TestResolveServer_CustomFallback(t *testing.T) {
	// Non-TLT/WeC bots use whatever fallback server is given.
	s := ResolveServer("SomeBot", "irc.custom.net")
	if s.Address != "irc.custom.net" {
		t.Errorf("expected irc.custom.net, got %s", s.Address)
	}
}

func TestResolveChannel_TLT(t *testing.T) {
	ch := ResolveChannel("TLTBot")
	if ch != "#tlt@XDCC|Bots|Channel" {
		t.Errorf("expected #tlt@XDCC|Bots|Channel, got %s", ch)
	}
}

func TestResolveChannel_WeC(t *testing.T) {
	ch := ResolveChannel("WeCBot")
	if ch != "#WeC@XDCC" {
		t.Errorf("expected #WeC@XDCC, got %s", ch)
	}
}

func TestResolveChannel_Other(t *testing.T) {
	ch := ResolveChannel("SomeBot")
	if ch != "" {
		t.Errorf("expected empty string for non-TLT/WeC bot, got %s", ch)
	}
}

// --- ParseXDCCMessage --------------------------------------------------------

func TestParseXDCCMessage_Single(t *testing.T) {
	packs, err := ParseXDCCMessage("/msg SomeBot xdcc send #42", ".", "irc.rizon.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 1 {
		t.Fatalf("got %d packs, want 1", len(packs))
	}
	if packs[0].PackNumber != 42 || packs[0].Bot != "SomeBot" {
		t.Errorf("pack = %+v", packs[0])
	}
}

func TestParseXDCCMessage_CommaSeparated(t *testing.T) {
	packs, err := ParseXDCCMessage("/msg SomeBot xdcc send #1,3,5", ".", "irc.rizon.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 3 {
		t.Fatalf("got %d packs, want 3", len(packs))
	}
	for i, want := range []int{1, 3, 5} {
		if packs[i].PackNumber != want {
			t.Errorf("packs[%d].PackNumber = %d, want %d", i, packs[i].PackNumber, want)
		}
	}
}

func TestParseXDCCMessage_Range(t *testing.T) {
	packs, err := ParseXDCCMessage("/msg SomeBot xdcc send #1-4", ".", "irc.rizon.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 4 {
		t.Fatalf("got %d packs, want 4", len(packs))
	}
	for i, want := range []int{1, 2, 3, 4} {
		if packs[i].PackNumber != want {
			t.Errorf("packs[%d].PackNumber = %d, want %d", i, packs[i].PackNumber, want)
		}
	}
}

func TestParseXDCCMessage_RangeWithStep(t *testing.T) {
	packs, err := ParseXDCCMessage("/msg SomeBot xdcc send #1-9;2", ".", "irc.rizon.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 5 {
		t.Fatalf("got %d packs, want 5", len(packs))
	}
	for i, want := range []int{1, 3, 5, 7, 9} {
		if packs[i].PackNumber != want {
			t.Errorf("packs[%d].PackNumber = %d, want %d", i, packs[i].PackNumber, want)
		}
	}
}

func TestParseXDCCMessage_DefaultsApplied(t *testing.T) {
	// Empty server → irc.rizon.net; empty directory → ".".
	packs, err := ParseXDCCMessage("/msg SomeBot xdcc send #10", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if packs[0].Server.Address != "irc.rizon.net" {
		t.Errorf("Server.Address = %s, want irc.rizon.net", packs[0].Server.Address)
	}
	if packs[0].Directory != "." {
		t.Errorf("Directory = %s, want .", packs[0].Directory)
	}
}

func TestParseXDCCMessage_BotServerOverride(t *testing.T) {
	// TLT-prefixed bot should resolve to irc.williamgattone.it.
	packs, err := ParseXDCCMessage("/msg TLTBot xdcc send #1", ".", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if packs[0].Server.Address != "irc.williamgattone.it" {
		t.Errorf("Server.Address = %s, want irc.williamgattone.it", packs[0].Server.Address)
	}
}

func TestParseXDCCMessage_InvalidFormat(t *testing.T) {
	_, err := ParseXDCCMessage("not a valid message", ".", "")
	if err == nil {
		t.Error("expected error for invalid message, got nil")
	}
}

func TestParseXDCCMessage_ReversedRange(t *testing.T) {
	_, err := ParseXDCCMessage("/msg SomeBot xdcc send #5-1", ".", "irc.rizon.net")
	if err == nil {
		t.Error("expected error for reversed range #5-1, got nil")
	}
}

func TestParseXDCCMessage_DirectoryPropagated(t *testing.T) {
	packs, err := ParseXDCCMessage("/msg SomeBot xdcc send #1,2", "/downloads", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, p := range packs {
		if p.Directory != "/downloads" {
			t.Errorf("Directory = %s, want /downloads", p.Directory)
		}
	}
}

// --- PreparePacks ------------------------------------------------------------

func TestPreparePacks_SinglePackLocation(t *testing.T) {
	p := NewXDCCPack(NewIrcServer("irc.rizon.net"), "Bot", 1)
	PreparePacks([]*XDCCPack{p}, "custom_name")
	if p.Filename != "custom_name" {
		t.Errorf("Filename = %q, want custom_name", p.Filename)
	}
}

func TestPreparePacks_MultiplePacksLocation(t *testing.T) {
	packs := []*XDCCPack{
		NewXDCCPack(NewIrcServer("irc.rizon.net"), "Bot", 1),
		NewXDCCPack(NewIrcServer("irc.rizon.net"), "Bot", 2),
	}
	PreparePacks(packs, "ep")
	if packs[0].Filename != "ep-000" {
		t.Errorf("packs[0].Filename = %q, want ep-000", packs[0].Filename)
	}
	if packs[1].Filename != "ep-001" {
		t.Errorf("packs[1].Filename = %q, want ep-001", packs[1].Filename)
	}
}

func TestPreparePacks_NoLocation(t *testing.T) {
	p := NewXDCCPack(NewIrcServer("irc.rizon.net"), "Bot", 1)
	p.SetFilename("original.mkv", true)
	PreparePacks([]*XDCCPack{p}, "")
	if p.Filename != "original.mkv" {
		t.Errorf("filename should not change when location is empty")
	}
}

func TestPreparePacks_TLTBotServerOverride(t *testing.T) {
	// PreparePacks MUST always map TLT bots to irc.williamgattone.it,
	// even when the search engine returned a different server.
	for _, startServer := range []string{"irc.rizon.net", "irc.other.net", "some.random.server"} {
		p := NewXDCCPack(NewIrcServer(startServer), "TLTBot", 1)
		PreparePacks([]*XDCCPack{p}, "")
		if p.Server.Address != "irc.williamgattone.it" {
			t.Errorf("PreparePacks(TLTBot from %s): Server.Address = %s, want irc.williamgattone.it",
				startServer, p.Server.Address)
		}
	}
}

func TestPreparePacks_DirectoryLocation(t *testing.T) {
	dir := t.TempDir()
	packs := []*XDCCPack{
		NewXDCCPack(NewIrcServer("irc.rizon.net"), "Bot", 1),
		NewXDCCPack(NewIrcServer("irc.rizon.net"), "Bot", 2),
	}
	// Set original filenames to verify they are not overwritten.
	packs[0].SetFilename("file1.mkv", true)
	packs[1].SetFilename("file2.mkv", true)

	PreparePacks(packs, dir)

	for i, p := range packs {
		if p.Directory != dir {
			t.Errorf("packs[%d].Directory = %q, want %q", i, p.Directory, dir)
		}
	}
	// Filenames must remain unchanged.
	if packs[0].Filename != "file1.mkv" {
		t.Errorf("packs[0].Filename = %q, want file1.mkv", packs[0].Filename)
	}
	if packs[1].Filename != "file2.mkv" {
		t.Errorf("packs[1].Filename = %q, want file2.mkv", packs[1].Filename)
	}
}

// --- ByteStringToByteCount ---------------------------------------------------

func TestByteStringToByteCount(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1 KB", 1024},
		{"1 MB", 1024 * 1024},
		{"1 GB", 1024 * 1024 * 1024},
		{"1.5 MB", int64(1.5 * 1024 * 1024)},
		{"512 B", 512},
		{"1024", 1024},
		{"", 0},
		{"abc", 0},
	}
	for _, tt := range tests {
		got := ByteStringToByteCount(tt.in)
		if got != tt.want {
			t.Errorf("ByteStringToByteCount(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// --- ParseThrottle -----------------------------------------------------------

func TestParseThrottle(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", -1, false},
		{"0", -1, false},
		{"-1", -1, false},
		{"100K", 100 * 1024, false},
		{"2M", 2 * 1024 * 1024, false},
		{"1G", 1024 * 1024 * 1024, false},
		{"abc", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseThrottle(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseThrottle(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Errorf("ParseThrottle(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseXDCCMessage_EmptyMessage(t *testing.T) {
	_, err := ParseXDCCMessage("", ".", "")
	if err == nil {
		t.Error("expected error for empty message")
	}
}

func TestParseXDCCMessage_ExtraSpaces(t *testing.T) {
	// Extra spaces should cause regex to fail
	_, err := ParseXDCCMessage("/msg  Bot  xdcc  send  #42", ".", "")
	if err == nil {
		t.Error("expected error for message with extra spaces")
	}
}

func TestParseXDCCMessage_LargeRange(t *testing.T) {
	packs, err := ParseXDCCMessage("/msg Bot xdcc send #1-100", ".", "irc.rizon.net")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 100 {
		t.Errorf("got %d packs, want 100", len(packs))
	}
}

func TestParseXDCCMessage_StepZeroDefaultsToOne(t *testing.T) {
	// Step 0 should default to 1 (the code checks s >= 1)
	packs, err := ParseXDCCMessage("/msg Bot xdcc send #1-5;0", ".", "irc.rizon.net")
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 5 {
		t.Errorf("got %d packs, want 5 (step=0 defaults to 1)", len(packs))
	}
}

func TestByteStringToByteCount_Lowercase(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1 kb", 1024},
		{"2 mb", 2 * 1024 * 1024},
		{"1 gb", 1024 * 1024 * 1024},
		{"500 b", 500},
		{"1.5 Mb", int64(1.5 * 1024 * 1024)},
		{"100 kB", 100 * 1024},
	}
	for _, tt := range tests {
		got := ByteStringToByteCount(tt.in)
		if got != tt.want {
			t.Errorf("ByteStringToByteCount(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestByteStringToByteCount_ShortSuffixes(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"1K", 1024},
		{"2M", 2 * 1024 * 1024},
		{"1G", 1024 * 1024 * 1024},
	}
	for _, tt := range tests {
		got := ByteStringToByteCount(tt.in)
		if got != tt.want {
			t.Errorf("ByteStringToByteCount(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseThrottle_Lowercase(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"100k", 100 * 1024},
		{"2m", 2 * 1024 * 1024},
		{"1g", 1024 * 1024 * 1024},
	}
	for _, tt := range tests {
		got, err := ParseThrottle(tt.in)
		if err != nil {
			t.Errorf("ParseThrottle(%q) error = %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseThrottle(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseThrottle_Decimal(t *testing.T) {
	got, err := ParseThrottle("1.5M")
	if err != nil {
		t.Fatal(err)
	}
	want := int64(1.5 * 1024 * 1024)
	if got != want {
		t.Errorf("ParseThrottle(1.5M) = %d, want %d", got, want)
	}
}

func TestParseThrottle_InvalidFormats(t *testing.T) {
	invalids := []string{"M100", "abc", "1.2.3M"}
	for _, s := range invalids {
		_, err := ParseThrottle(s)
		if err == nil {
			t.Errorf("ParseThrottle(%q) should return error", s)
		}
	}
}
