package search

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"xdcc-go/internal/entities"
)

// NiblEngine searches for XDCC packs on nibl.co.uk.
type NiblEngine struct {
	baseURL string // override for testing; empty = "https://nibl.co.uk"
}

func (e *NiblEngine) Name() string { return "nibl" }

func (e *NiblEngine) Search(ctx context.Context, term string) ([]*entities.XDCCPack, error) {
	base := resolveBaseURL(e.baseURL, "https://nibl.co.uk")
	query := url.QueryEscape(term)
	searchURL := fmt.Sprintf("%s/search?query=%s", base, query)

	resp, err := httpGet(ctx, searchURL)
	if err != nil {
		return nil, fmt.Errorf("nibl search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("nibl returned HTTP %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("nibl HTML parse failed: %w", err)
	}

	server := entities.NewIrcServer("irc.rizon.net")
	var packs []*entities.XDCCPack

	// Parse table rows; skip the header row
	doc.Find("table tr").Each(func(i int, row *goquery.Selection) {
		if i == 0 {
			return // skip header
		}
		cols := row.Find("td")
		if cols.Length() < 4 {
			return
		}
		bot := strings.TrimSpace(cols.Eq(0).Text())
		packNumStr := strings.TrimSpace(cols.Eq(1).Text())
		sizeStr := strings.TrimSpace(cols.Eq(2).Text())
		filename := strings.TrimSpace(cols.Eq(3).Text())

		var num int
		_, _ = fmt.Sscanf(packNumStr, "%d", &num)
		if num == 0 {
			return
		}

		pack := entities.NewXDCCPack(server, bot, num)
		pack.SetSize(entities.ByteStringToByteCount(sizeStr))
		pack.SetFilename(filename, true)
		packs = append(packs, pack)
	})

	return packs, nil
}
