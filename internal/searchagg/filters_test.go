package searchagg

import (
	"testing"

	"xdcc-go/internal/entities"
)

// Helper to create a pack with minimal fields.
func mkPack(filename string, size int64, bot string) *entities.XDCCPack {
	srv := entities.NewIrcServerWithPort("irc.test.net", 6667)
	p := entities.NewXDCCPack(srv, bot, 0)
	p.SetFilename(filename, true)
	p.SetSize(size)
	p.Bot = bot
	return p
}

func mkPackWithBot(filename string, size int64, bot string, packNum int) *entities.XDCCPack {
	srv := entities.NewIrcServerWithPort("irc.test.net", 6667)
	p := entities.NewXDCCPack(srv, bot, packNum)
	p.SetFilename(filename, true)
	p.SetSize(size)
	p.Bot = bot
	return p
}

// ===========================================================================
// filterByPrefix
// ===========================================================================

func TestFilterByPrefix(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("Anime.Movie.2023.mkv", 1000, "Bot1"),
		mkPack("Anime.Series.E01.mkv", 500, "Bot2"),
		mkPack("Movie.2023.mkv", 2000, "Bot3"),
	}

	// Case-insensitive
	result := filterByPrefix(packs, "anime")
	if len(result) != 2 {
		t.Errorf("expected 2 results for prefix 'anime', got %d", len(result))
	}

	// Non-matching prefix
	result = filterByPrefix(packs, "nonexistent")
	if len(result) != 0 {
		t.Errorf("expected 0 results for non-matching prefix, got %d", len(result))
	}

	// Empty prefix returns all
	result = filterByPrefix(packs, "")
	if len(result) != 3 {
		t.Errorf("expected 3 results for empty prefix, got %d", len(result))
	}

	// Exact prefix match
	result = filterByPrefix(packs, "Movie")
	if len(result) != 1 {
		t.Errorf("expected 1 result for prefix 'Movie', got %d", len(result))
	}
}

// ===========================================================================
// filterByBot
// ===========================================================================

func TestFilterByBot(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("a.mkv", 100, "SubsPlease"),
		mkPack("b.mkv", 200, "AnimeBot"),
		mkPack("c.mkv", 300, "SubsPleaseAlt"),
	}

	// Substring match (case-insensitive)
	result := filterByBot(packs, "subs")
	if len(result) != 2 {
		t.Errorf("expected 2 results for bot 'subs', got %d: %+v", len(result), result)
	}

	// Full match
	result = filterByBot(packs, "AnimeBot")
	if len(result) != 1 {
		t.Errorf("expected 1 result for bot 'AnimeBot', got %d", len(result))
	}

	// No match
	result = filterByBot(packs, "Nonexistent")
	if len(result) != 0 {
		t.Errorf("expected 0 results for non-matching bot, got %d", len(result))
	}

	// Empty bot returns all
	result = filterByBot(packs, "")
	if len(result) != 3 {
		t.Errorf("expected 3 results for empty bot, got %d", len(result))
	}
}

// ===========================================================================
// filterByExt
// ===========================================================================

func TestFilterByExt(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("video.mkv", 100, "Bot1"),
		mkPack("subtitle.srt", 10, "Bot2"),
		mkPack("audio.mp3", 50, "Bot3"),
		mkPack("image.jpg", 5, "Bot4"),
	}

	// Single extension
	result := filterByExt(packs, []string{".mkv"})
	if len(result) != 1 {
		t.Errorf("expected 1 result for .mkv, got %d", len(result))
	}

	// Multiple extensions
	result = filterByExt(packs, []string{".mkv", ".srt"})
	if len(result) != 2 {
		t.Errorf("expected 2 results for .mkv,.srt, got %d", len(result))
	}

	// Extension without dot prefix
	result = filterByExt(packs, []string{"mkv"})
	if len(result) != 1 {
		t.Errorf("expected 1 result for 'mkv' (no dot), got %d", len(result))
	}

	// Case-insensitive
	result = filterByExt(packs, []string{".MKV"})
	if len(result) != 1 {
		t.Errorf("expected 1 result for .MKV (uppercase), got %d", len(result))
	}

	// Empty exts returns none (no extensions to match against)
	result = filterByExt(packs, []string{})
	if len(result) != 0 {
		t.Errorf("expected 0 results for empty exts (no filter criteria), got %d", len(result))
	}

	// No match
	result = filterByExt(packs, []string{".exe"})
	if len(result) != 0 {
		t.Errorf("expected 0 results for .exe, got %d", len(result))
	}
}

// ===========================================================================
// compactPacks (deduplication)
// ===========================================================================

func TestCompactPacks_NoDuplicates(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("file1.mkv", 1000, "BotA"),
		mkPack("file2.mkv", 2000, "BotB"),
	}

	result := compactPacks(packs)
	if len(result) != 2 {
		t.Errorf("expected 2 results with no duplicates, got %d", len(result))
	}
}

func TestCompactPacks_ExactDuplicate(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("file.mkv", 1000, "Bot123"),
		mkPack("file.mkv", 1000, "Bot123"),
	}

	result := compactPacks(packs)
	if len(result) != 1 {
		t.Errorf("expected 1 result for exact duplicate, got %d", len(result))
	}
}

func TestCompactPacks_SameBotFamily(t *testing.T) {
	// Bot123 and Bot456 have same family "Bot"
	packs := []*entities.XDCCPack{
		mkPack("file.mkv", 1000, "Bot123"),
		mkPack("file.mkv", 1000, "Bot456"),
		mkPack("file.mkv", 1000, "Bot789"),
	}

	result := compactPacks(packs)
	if len(result) != 1 {
		t.Errorf("expected 1 result for same bot family, got %d", len(result))
	}
}

func TestCompactPacks_DifferentBotFamilies(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("file.mkv", 1000, "SubsPlease"),
		mkPack("file.mkv", 1000, "AnimeBot"),
	}

	result := compactPacks(packs)
	if len(result) != 2 {
		t.Errorf("expected 2 results for different bot families, got %d", len(result))
	}
}

func TestCompactPacks_DifferentSizes(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("file.mkv", 1000, "Bot123"),
		mkPack("file.mkv", 2000, "Bot123"),
	}

	result := compactPacks(packs)
	if len(result) != 2 {
		t.Errorf("expected 2 results for different sizes, got %d", len(result))
	}
}

// ===========================================================================
// botFamily
// ===========================================================================

func TestBotFamily(t *testing.T) {
	// Delegate to entities.BotFamily after unification of compact logic
	tests := []struct {
		bot    string
		family string
	}{
		{"SubsPlease", "SubsPle"},          // len=10: n > 3, so first 7 chars
		{"SubsPlease01", "SubsPleas"},      // len=12: n > 3, so first 9 chars
		{"AnimeBot123", "AnimeBot"},        // len=11: n > 3, so first 8 chars
		{"Bot123456", "Bot123"},            // len=9:  n > 3, so first 6 chars
		{"WOND-ZeroTwo_001", "WOND-ZeroT"}, // len=16: n >= 13, so first 10 chars
		{"Test", "T"},                      // len=4:  n > 3, so first 1 char (n-3)
		{"A", "A"},                         // len=1:  default, no truncation
		{"AB", "AB"},                       // len=2:  default
		{"ABC", "ABC"},                     // len=3:  default
		{"ABCD", "A"},                      // len=4:  n > 3, so first 1 char (n-3)
		{"123Bot", "123"},                  // len=6:  n > 3, so first 3 chars
		{"", ""},
	}

	for _, tt := range tests {
		got := entities.BotFamily(tt.bot)
		if got != tt.family {
			t.Errorf("BotFamily(%q) = %q, want %q", tt.bot, got, tt.family)
		}
	}
}

// ===========================================================================
// filterPacks (combined)
// ===========================================================================

func TestFilterPacks_All(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("Anime.Show.2023.mkv", 1000, "SubsPlease01"),
		mkPack("Anime.Show.2023.mp4", 800, "SubsPlease02"),
		mkPack("Movie.2023.mkv", 2000, "AnimeBot"),
		mkPack("Anime.Show.2023.srt", 10, "SubsPlease03"),
	}

	opts := SearchOptions{
		Prefix:  "Anime",
		Bot:     "SubsPlease",
		Ext:     []string{".mkv", ".mp4"},
		Compact: true,
	}

	result := filterPacks(packs, opts)
	// Should match: Anime.Show.2023.mkv (SubsPlease01) and Anime.Show.2023.mp4 (SubsPlease02)
	// After compact: only 1 since same bot family + filename + size → wait, sizes differ (1000 vs 800) so both remain
	// Actually: both match prefix, bot filter, ext filter, different sizes → 2 results
	if len(result) != 2 {
		t.Errorf("expected 2 results after all filters, got %d", len(result))
	}
}

func TestFilterPacks_EmptyResult(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("a.mkv", 100, "Bot"),
	}

	opts := SearchOptions{Bot: "Nonexistent"}
	result := filterPacks(packs, opts)
	if len(result) != 0 {
		t.Errorf("expected 0 results for non-matching filter, got %d", len(result))
	}
}

// ===========================================================================
// sortPacks
// ===========================================================================

func TestSortPacks_PrefixFirst(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("ZZZ_Other.mkv", 100, "Bot"),
		mkPack("Anime_Show.mkv", 200, "Bot"),
		mkPack("Anime_Movie.mkv", 300, "Bot"),
	}

	sortPacks(packs, "Anime")

	// All packs with "Anime" prefix first, then sorted by size desc, then alphabetically
	if len(packs) >= 3 {
		if !containsLower(packs[0].Filename, "anime") {
			t.Errorf("expected first result to have 'Anime' prefix, got %s", packs[0].Filename)
		}
		// Anime_Movie.mkv (300) should come before Anime_Show.mkv (200)
		if packs[0].Filename != "Anime_Movie.mkv" {
			t.Errorf("expected first to be Anime_Movie.mkv (largest size), got %s", packs[0].Filename)
		}
	}
}

func TestSortPacks_NoQuery(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPack("b.mkv", 100, "Bot"),
		mkPack("a.mkv", 100, "Bot"),
		mkPack("c.mkv", 100, "Bot"),
	}

	sortPacks(packs, "")

	// When no query, sort by size desc then alphabetically
	// All have same size (100), so alphabetical: a.mkv, b.mkv, c.mkv
	if packs[0].Filename != "a.mkv" {
		t.Errorf("expected first to be a.mkv (alphabetical when same size), got %s", packs[0].Filename)
	}
	if packs[2].Filename != "c.mkv" {
		t.Errorf("expected last to be c.mkv, got %s", packs[2].Filename)
	}
}

func TestSortPacks_LargerSizeFirst(t *testing.T) {
	allSameSize := []*entities.XDCCPack{
		mkPack("z.mkv", 100, "Bot"),
		mkPack("a.mkv", 100, "Bot"),
	}
	sortPacks(allSameSize, "")
	// Same size → alphabetical
	if allSameSize[0].Filename != "a.mkv" {
		t.Errorf("expected 'a.mkv' first when same size, got %s", allSameSize[0].Filename)
	}

	diffSize := []*entities.XDCCPack{
		mkPack("small.mkv", 100, "Bot"),
		mkPack("large.mkv", 1000, "Bot"),
	}
	sortPacks(diffSize, "")
	if diffSize[0].Filename != "large.mkv" {
		t.Errorf("expected 'large.mkv' first (larger size), got %s", diffSize[0].Filename)
	}
}

// ===========================================================================
// paginatePacks
// ===========================================================================

func TestPaginatePacks_FirstPage(t *testing.T) {
	packs := make([]*entities.XDCCPack, 100)
	for i := 0; i < 100; i++ {
		packs[i] = mkPack("file.mkv", int64(i), "Bot")
	}

	paged, total := paginatePacks(packs, 1, 10)
	if len(paged) != 10 {
		t.Errorf("expected 10 items on first page, got %d", len(paged))
	}
	if total != 100 {
		t.Errorf("expected total 100, got %d", total)
	}
}

func TestPaginatePacks_LastPage(t *testing.T) {
	packs := make([]*entities.XDCCPack, 25)
	for i := 0; i < 25; i++ {
		packs[i] = mkPack("file.mkv", int64(i), "Bot")
	}

	paged, total := paginatePacks(packs, 3, 10)
	if len(paged) != 5 {
		t.Errorf("expected 5 items on last page (page 3 of 25 items with pageSize=10), got %d", len(paged))
	}
	if total != 25 {
		t.Errorf("expected total 25, got %d", total)
	}
}

func TestPaginatePacks_Empty(t *testing.T) {
	paged, total := paginatePacks([]*entities.XDCCPack{}, 1, 10)
	if len(paged) != 0 {
		t.Errorf("expected 0 items for empty list, got %d", len(paged))
	}
	if total != 0 {
		t.Errorf("expected total 0, got %d", total)
	}
}

func TestPaginatePacks_DefaultPage(t *testing.T) {
	packs := []*entities.XDCCPack{mkPack("a.mkv", 100, "Bot")}

	// Page 0 should default to 1
	paged, _ := paginatePacks(packs, 0, 10)
	if len(paged) != 1 {
		t.Errorf("expected 1 item for page 0 (defaults to 1), got %d", len(paged))
	}
}

func TestPaginatePacks_OutOfRange(t *testing.T) {
	packs := make([]*entities.XDCCPack, 5)
	for i := 0; i < 5; i++ {
		packs[i] = mkPack("file.mkv", int64(i), "Bot")
	}

	paged, total := paginatePacks(packs, 99, 10)
	if len(paged) != 0 {
		t.Errorf("expected 0 items for out-of-range page, got %d", len(paged))
	}
	if total != 5 {
		t.Errorf("expected total 5, got %d", total)
	}
}

// ===========================================================================
// cacheKey (from cache.go, but simple enough to test here)
// ===========================================================================

func TestCacheKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Anime Show", "anime show"},
		{"  Multiple   Spaces  ", "multiple spaces"},
		{"UPPERCASE", "uppercase"},
		{"Mixed CASE", "mixed case"},
		{"  trim  ", "trim"},
		{"", ""},
	}

	for _, tt := range tests {
		got := cacheKey(tt.input)
		if got != tt.expected {
			t.Errorf("cacheKey(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

func containsLower(s, substr string) bool {
	return len(s) >= len(substr) && containerLower(s, substr)
}

func containerLower(s, substr string) bool {
	// simple ASCII case-insensitive contains
	for i := 0; i <= len(s)-len(substr); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			sc := s[i+j]
			if sc >= 'A' && sc <= 'Z' {
				sc += 'a' - 'A'
			}
			if sc != substr[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
