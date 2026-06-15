package searchagg

import (
	"path/filepath"
	"sort"
	"strings"

	"xdcc_server/internal/entities"
)

// ---------------------------------------------------------------------------
// Filter functions
// ---------------------------------------------------------------------------

// filterPacks applies the search options filters (prefix, bot, ext, compact, min/max size)
// to a list of packs and returns the filtered list.
func filterPacks(packs []*entities.XDCCPack, opts SearchOptions) []*entities.XDCCPack {
	result := packs

	// Filter by query terms - all terms must be present in filename
	if opts.Query != "" {
		result = filterByQuery(result, opts.Query)
	}
	if opts.Prefix != "" {
		result = filterByPrefix(result, opts.Prefix)
	}
	if opts.Bot != "" {
		result = filterByBot(result, opts.Bot)
	}
	if len(opts.Ext) > 0 {
		result = filterByExt(result, opts.Ext)
	}
	if opts.MinSize != "" {
		result = filterByMinSize(result, opts.MinSize)
	}
	if opts.MaxSize != "" {
		result = filterByMaxSize(result, opts.MaxSize)
	}
	if opts.VideoOnly {
		result = filterByExt(result, []string{".avi", ".mpeg", ".mkv", ".mp4", ".mpg", ".mov"})
	}
	if opts.AudioOnly {
		result = filterByExt(result, []string{".mp3", ".m4a", ".ogg", ".flac", ".aac"})
	}
	if opts.BooksOnly {
		result = filterByExt(result, []string{".epub", ".mobi", ".pdf"})
	}
	if opts.ZipOnly {
		result = filterByExt(result, []string{".zip", ".rar", ".7z", ".tar", ".gz", ".bz2", ".xz"})
	}
	if opts.Compact {
		result = compactPacks(result)
	}

	return result
}

// filterByQuery keeps only packs whose filename contains all positive query terms
// and none of the negative (exclusion) terms prefixed with "-".
// For example, "ubuntu -old" keeps packs that contain "ubuntu" but not "old".
func filterByQuery(packs []*entities.XDCCPack, query string) []*entities.XDCCPack {
	// Split query into terms and separate into positive and negative.
	rawTerms := strings.Fields(strings.ToLower(query))
	if len(rawTerms) == 0 {
		return packs
	}

	var posTerms []string
	var negTerms []string
	for _, t := range rawTerms {
		if strings.HasPrefix(t, "-") && len(t) > 1 {
			negTerms = append(negTerms, t[1:]) // strip the "-" prefix
		} else {
			posTerms = append(posTerms, t)
		}
	}

	// If there are no positive terms, include all packs and only apply exclusions.
	// If there are no negative terms, this behaves identically to the old logic.

	var out []*entities.XDCCPack
	for _, p := range packs {
		filenameLower := strings.ToLower(p.GetFilename())

		// All positive terms must be present in the filename.
		allFound := true
		for _, term := range posTerms {
			if !strings.Contains(filenameLower, term) {
				allFound = false
				break
			}
		}
		if !allFound {
			continue
		}

		// No negative term must be present in the filename.
		excluded := false
		for _, term := range negTerms {
			if strings.Contains(filenameLower, term) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}

		out = append(out, p)
	}
	return out
}

// wordSepReplacer maps common filename word separators to spaces for prefix matching.
var wordSepReplacer = strings.NewReplacer(".", " ", "_", " ", "-", " ")

// normalizeWords lowers case, replaces common word separators with spaces,
// and collapses multiple spaces into single spaces.
func normalizeWords(s string) string {
	s = wordSepReplacer.Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// filterByPrefix keeps only packs whose filename starts with the given prefix.
// Word separators (., _, -) are normalized to spaces before comparison, so
// a prefix "paperino nano" matches "paperino.nano.1080p.mkv" and vice versa.
func filterByPrefix(packs []*entities.XDCCPack, prefix string) []*entities.XDCCPack {
	prefixNorm := normalizeWords(prefix)
	var out []*entities.XDCCPack
	for _, p := range packs {
		fnNorm := normalizeWords(p.GetFilename())
		if strings.HasPrefix(fnNorm, prefixNorm) {
			out = append(out, p)
		}
	}
	return out
}

// filterByBot keeps only packs whose bot name contains the substring (case-insensitive).
func filterByBot(packs []*entities.XDCCPack, bot string) []*entities.XDCCPack {
	botLower := strings.ToLower(bot)
	var out []*entities.XDCCPack
	for _, p := range packs {
		if strings.Contains(strings.ToLower(p.Bot), botLower) {
			out = append(out, p)
		}
	}
	return out
}

// filterByMinSize keeps only packs whose file size meets the minimum.
// minSize is a human-readable size string like "100MB" or "1.5GB".
func filterByMinSize(packs []*entities.XDCCPack, minSize string) []*entities.XDCCPack {
	minBytes := entities.ByteStringToByteCount(minSize)
	if minBytes <= 0 {
		return packs
	}
	var out []*entities.XDCCPack
	for _, p := range packs {
		if p.GetSize() <= 0 || p.GetSize() >= minBytes {
			out = append(out, p)
		}
	}
	return out
}

// filterByMaxSize keeps only packs whose file size does not exceed the maximum.
// maxSize is a human-readable size string like "4GB" or "500MB".
func filterByMaxSize(packs []*entities.XDCCPack, maxSize string) []*entities.XDCCPack {
	maxBytes := entities.ByteStringToByteCount(maxSize)
	if maxBytes <= 0 {
		return packs
	}
	var out []*entities.XDCCPack
	for _, p := range packs {
		if p.GetSize() <= 0 || p.GetSize() <= maxBytes {
			out = append(out, p)
		}
	}
	return out
}

// filterByExt keeps only packs whose filename has one of the given extensions.
func filterByExt(packs []*entities.XDCCPack, exts []string) []*entities.XDCCPack {
	extSet := make(map[string]bool, len(exts))
	for _, e := range exts {
		e = strings.TrimSpace(e)
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		extSet[strings.ToLower(e)] = true
	}
	var out []*entities.XDCCPack
	for _, p := range packs {
		ext := strings.ToLower(filepath.Ext(p.GetFilename()))
		if extSet[ext] {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Deduplication / compact
// ---------------------------------------------------------------------------

// compactPacks removes duplicates by (filename, size, bot family) using
// the same algorithm as the --compact flag in xdcc-browse.
// The bot family is derived via entities.BotFamily (name prefix grouping)
// so that e.g. "SubsPlease01" and "SubsPlease99" are collapsed.
func compactPacks(packs []*entities.XDCCPack) []*entities.XDCCPack {
	return entities.CompactPacks(packs)
}

// ---------------------------------------------------------------------------
// Sorting
// ---------------------------------------------------------------------------

// sortPacks sorts packs alphabetically by filename.
func sortPacks(packs []*entities.XDCCPack, query string) {
	sort.Slice(packs, func(i, j int) bool {
		a, b := packs[i], packs[j]

		// Sort alphabetically by filename
		return strings.ToLower(a.GetFilename()) < strings.ToLower(b.GetFilename())
	})
}

// ---------------------------------------------------------------------------
// Pagination
// ---------------------------------------------------------------------------

// paginatePacks returns a slice of packs for the given page.
// page is 1-based. Returns the slice and total count.
func paginatePacks(packs []*entities.XDCCPack, page, pageSize int) (filtered []*entities.XDCCPack, total int) {
	total = len(packs)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}

	start := (page - 1) * pageSize
	if start >= total {
		return []*entities.XDCCPack{}, total
	}

	end := start + pageSize
	if end > total {
		end = total
	}

	return packs[start:end], total
}
