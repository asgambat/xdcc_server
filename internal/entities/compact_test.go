package entities

import "testing"

func TestBotFamily(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		// len >= 13 → first 10
		{"Bot1234567890", "Bot1234567"},
		{"WONDERFULBOT!", "WONDERFULB"},
		{"1234567890ABC", "1234567890"},
		// len < 13 and > 3 → first len-3
		{"Bot123456789", "Bot123456"}, // len=12 → 9
		{"BotABCD", "BotA"},           // len=7 → 4
		{"ABCD", "A"},                 // len=4 → 1
		// len <= 3 → full name
		{"Bot", "Bot"},
		{"AB", "AB"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BotFamily(tt.name)
			if got != tt.want {
				t.Errorf("BotFamily(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestCompactPacks(t *testing.T) {
	srv := IrcServer{Address: "irc.rizon.net"}

	mkPack := func(filename string, size int64, bot string, packNum int) *XDCCPack {
		p := NewXDCCPack(srv, bot, packNum)
		p.Filename = filename
		p.Size = size
		return p
	}

	packs := []*XDCCPack{
		mkPack("file.mkv", 1000, "Bot1234567890", 1),  // family "Bot1234567"
		mkPack("file.mkv", 1000, "Bot1234567XXX", 2),  // same family → duplicate
		mkPack("file.mkv", 1000, "Bot1234567YYY", 3),  // same family → duplicate
		mkPack("file.mkv", 2000, "Bot1234567890", 4),  // different size → kept
		mkPack("other.mkv", 1000, "Bot1234567890", 5), // different filename → kept
		mkPack("file.mkv", 1000, "DiffBot123456", 6),  // different family → kept
	}

	result := CompactPacks(packs)

	if len(result) != 4 {
		t.Fatalf("expected 4 results, got %d", len(result))
	}

	expectedPackNums := []int{1, 4, 5, 6}
	for i, p := range result {
		if p.PackNumber != expectedPackNums[i] {
			t.Errorf("result[%d].PackNumber = %d, want %d", i, p.PackNumber, expectedPackNums[i])
		}
	}
}

func TestCompactPacks_NoDuplicates(t *testing.T) {
	srv := IrcServer{Address: "irc.rizon.net"}

	mkPack := func(filename string, size int64, bot string, packNum int) *XDCCPack {
		p := NewXDCCPack(srv, bot, packNum)
		p.Filename = filename
		p.Size = size
		return p
	}

	packs := []*XDCCPack{
		mkPack("a.mkv", 100, "BotA1234567890", 1),
		mkPack("b.mkv", 200, "BotB1234567890", 2),
	}

	result := CompactPacks(packs)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
}

func TestCompactPacks_Empty(t *testing.T) {
	result := CompactPacks(nil)
	if len(result) != 0 {
		t.Fatalf("expected 0 results, got %d", len(result))
	}
}

func TestCompactPacks_ZeroSize(t *testing.T) {
	srv := IrcServer{Address: "irc.rizon.net"}
	p1 := NewXDCCPack(srv, "Bot1234567890", 1)
	p1.Filename = "file.mkv"
	p1.Size = 0
	p2 := NewXDCCPack(srv, "Bot1234567XXX", 2)
	p2.Filename = "file.mkv"
	p2.Size = 0
	result := CompactPacks([]*XDCCPack{p1, p2})
	if len(result) != 1 {
		t.Errorf("expected 1 (compacted), got %d", len(result))
	}
}

func TestCompactPacks_EmptyFilename(t *testing.T) {
	srv := IrcServer{Address: "irc.rizon.net"}
	p1 := NewXDCCPack(srv, "BotA", 1)
	p1.Filename = ""
	p1.Size = 100
	p2 := NewXDCCPack(srv, "BotB", 2)
	p2.Filename = ""
	p2.Size = 200
	result := CompactPacks([]*XDCCPack{p1, p2})
	// Different sizes → both kept
	if len(result) != 2 {
		t.Errorf("expected 2, got %d", len(result))
	}
}

func TestCompactPacks_AllIdentical(t *testing.T) {
	srv := IrcServer{Address: "irc.rizon.net"}
	var packs []*XDCCPack
	for i := 0; i < 5; i++ {
		p := NewXDCCPack(srv, "Bot1234567890", i+1)
		p.Filename = "same.mkv"
		p.Size = 1000
		packs = append(packs, p)
	}
	result := CompactPacks(packs)
	if len(result) != 1 {
		t.Errorf("expected 1 (all identical), got %d", len(result))
	}
	if result[0].PackNumber != 1 {
		t.Errorf("expected first pack (1), got %d", result[0].PackNumber)
	}
}

func TestBotFamily_ExactBoundaries(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		// Exactly 3 chars → full name
		{"ABC", "ABC"},
		// Exactly 4 chars → first 1
		{"ABCD", "A"},
		// Exactly 12 chars → first 9
		{"123456789012", "123456789"},
		// Exactly 13 chars → first 10
		{"1234567890123", "1234567890"},
	}
	for _, tt := range tests {
		got := BotFamily(tt.name)
		if got != tt.want {
			t.Errorf("BotFamily(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}
