package search

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"xdcc-go/internal/entities"
)

// XdccEuEngine searches for XDCC packs on xdcc.eu.
type XdccEuEngine struct {
	Verbose bool
	baseURL string // override for testing; empty = "https://www.xdcc.eu"
}

func (e *XdccEuEngine) Name() string { return "xdcc-eu" }

func (e *XdccEuEngine) Search(ctx context.Context, term string) ([]*entities.XDCCPack, error) {
	base := resolveBaseURL(e.baseURL, "https://www.xdcc.eu")
	searchURL := fmt.Sprintf("%s/search.php?searchkey=%s", base, url.QueryEscape(term))

	doc, err := e.fetchDocument(ctx, searchURL)
	if err != nil {
		return nil, err
	}

	return e.parseResults(doc)
}

// fetchDocument performs the HTTP GET and parses the response body as an HTML document.
func (e *XdccEuEngine) fetchDocument(ctx context.Context, rawURL string) (*goquery.Document, error) {
	if e.Verbose {
		fmt.Fprintf(os.Stderr, "[DEBUG] GET %s\n", rawURL)
	}

	resp, err := httpGet(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("xdcc.eu request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("xdcc.eu returned HTTP %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("xdcc.eu HTML parse failed: %w", err)
	}

	if e.Verbose {
		if rescount := strings.TrimSpace(doc.Find(".rescount").Text()); rescount != "" {
			fmt.Fprintf(os.Stderr, "[DEBUG] Page says: %s\n", rescount)
		}
		fmt.Fprintf(os.Stderr, "[DEBUG] Table rows found: %d\n", doc.Find("tbody tr").Length())
	}

	return doc, nil
}

// parseResults iterates over all result table rows and builds the pack list.
func (e *XdccEuEngine) parseResults(doc *goquery.Document) ([]*entities.XDCCPack, error) {
	var packs []*entities.XDCCPack
	skipped := 0

	doc.Find("tbody tr").Each(func(i int, row *goquery.Selection) {
		pack, ok := e.parseRow(i, row)
		if !ok {
			skipped++
			return
		}
		packs = append(packs, pack)
	})

	if e.Verbose && skipped > 0 {
		fmt.Printf("[DEBUG] Skipped %d rows\n", skipped)
	}

	return packs, nil
}

// parseRow extracts a single XDCCPack from a result table row.
// The xdcc.eu result table has at least 7 columns per row:
//
//	td[0]: network name
//	td[1]: action links — the "info" anchor carries data-s (server address)
//	        and data-p (the raw XDCC send command, e.g. "BotName xdcc send #42")
//	td[5]: file size (may be prefixed with non-numeric characters, e.g. "≈1.4 GB")
//	td[6]: filename
func (e *XdccEuEngine) parseRow(i int, row *goquery.Selection) (*entities.XDCCPack, bool) {
	parts := row.Find("td")
	if parts.Length() < 7 {
		return nil, false
	}

	// The info anchor is uniquely identified by the presence of the data-s attribute.
	link := parts.Eq(1).Find("a[data-s]")

	// data-s holds the IRC server address (host or host:port).
	serverAddr, exists := link.Attr("data-s")
	if !exists {
		if e.Verbose {
			fmt.Printf("[DEBUG] Row %d: no data-s, skipping\n", i)
		}
		return nil, false
	}

	// data-p holds the raw XDCC send command: "<bot> xdcc send #<num>"
	packMsg, exists := link.Attr("data-p")
	if !exists {
		return nil, false
	}

	// Split on the fixed " xdcc send #" separator to isolate bot name and pack number.
	msgParts := strings.SplitN(packMsg, " xdcc send #", 2)
	if len(msgParts) != 2 {
		if e.Verbose {
			fmt.Printf("[DEBUG] Row %d: unexpected data-p format: %q\n", i, packMsg)
		}
		return nil, false
	}

	bot := strings.TrimSpace(msgParts[0])
	var packNum int
	_, _ = fmt.Sscanf(msgParts[1], "%d", &packNum)
	if packNum == 0 {
		return nil, false
	}

	sizeRaw := strings.TrimSpace(parts.Eq(5).Text())
	filename := strings.TrimSpace(parts.Eq(6).Text())
	size := entities.ByteStringToByteCount(extractNumericSuffix(sizeRaw))

	if e.Verbose {
		fmt.Printf("[DEBUG] Row %d: bot=%s pack=#%d size=%s file=%s\n", i, bot, packNum, sizeRaw, filename)
	}

	server := entities.NewIrcServer(serverAddr)
	pack := entities.NewXDCCPack(server, bot, packNum)
	pack.SetSize(size)
	pack.SetFilename(filename, true)
	return pack, true
}

// extractNumericSuffix extracts the numeric+unit part of a size string
// by stripping non-numeric leading characters.
func extractNumericSuffix(s string) string {
	s = strings.TrimSpace(s)
	for i, ch := range s {
		if ch >= '0' && ch <= '9' {
			return s[i:]
		}
	}
	return s
}
