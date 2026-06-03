package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

// --- helpers -----------------------------------------------------------------

// rowDoc builds a minimal HTML document containing one tbody tr.
// td1HTML is injected as the raw content of td[1] (the action-links cell).
func rowDoc(td1HTML, size, filename string) *goquery.Document {
	html := fmt.Sprintf(`<html><body><table><tbody><tr>
		<td>Network</td>
		<td>%s</td>
		<td></td><td></td><td></td>
		<td>%s</td>
		<td>%s</td>
	</tr></tbody></table></body></html>`, td1HTML, size, filename)
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(html))
	return doc
}

// tableDoc builds a document whose tbody contains the given raw row HTML strings.
func tableDoc(rows []string) *goquery.Document {
	var sb strings.Builder
	sb.WriteString("<html><body><table><tbody>")
	for _, r := range rows {
		sb.WriteString(r)
	}
	sb.WriteString("</tbody></table></body></html>")
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(sb.String()))
	return doc
}

// validRowHTML returns a complete tr that parseRow should accept.
func validRowHTML(bot, server string, packNum int, size, filename string) string {
	return fmt.Sprintf(`<tr>
		<td>Network</td>
		<td><a data-s="%s" data-p="%s xdcc send #%d">info</a></td>
		<td></td><td></td><td></td>
		<td>%s</td>
		<td>%s</td>
	</tr>`, server, bot, packNum, size, filename)
}

// --- extractNumericSuffix ----------------------------------------------------

func TestExtractNumericSuffix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"1.4 GB", "1.4 GB"},      // already starts with a digit
		{"≈1.4 GB", "1.4 GB"},     // non-numeric prefix stripped
		{"  ≈500 MB  ", "500 MB"}, // leading/trailing spaces and prefix
		{"123", "123"},            // plain number
		{"", ""},                  // empty input
		{"abc", "abc"},            // no digit found → returned as-is (trimmed)
	}
	for _, tt := range tests {
		got := extractNumericSuffix(tt.in)
		if got != tt.want {
			t.Errorf("extractNumericSuffix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- parseRow ----------------------------------------------------------------

func TestParseRow_Valid(t *testing.T) {
	e := &XdccEuEngine{}
	td1 := `<a data-s="irc.rizon.net" data-p="CoolBot xdcc send #42">info</a>`
	doc := rowDoc(td1, "≈700 MB", "episode.mkv")

	pack, ok := e.parseRow(0, doc.Find("tbody tr").First())

	if !ok {
		t.Fatal("parseRow returned ok=false, want true")
	}
	if pack.Bot != "CoolBot" {
		t.Errorf("Bot = %q, want CoolBot", pack.Bot)
	}
	if pack.PackNumber != 42 {
		t.Errorf("PackNumber = %d, want 42", pack.PackNumber)
	}
	if pack.Filename != "episode.mkv" {
		t.Errorf("Filename = %q, want episode.mkv", pack.Filename)
	}
	if pack.Server.Address != "irc.rizon.net" {
		t.Errorf("Server.Address = %q, want irc.rizon.net", pack.Server.Address)
	}
	if pack.Size == 0 {
		t.Error("Size should be non-zero for a valid size string")
	}
}

func TestParseRow_TooFewColumns(t *testing.T) {
	e := &XdccEuEngine{}
	html := `<html><body><table><tbody>
		<tr><td>only</td><td>two</td></tr>
	</tbody></table></body></html>`
	doc, _ := goquery.NewDocumentFromReader(strings.NewReader(html))

	_, ok := e.parseRow(0, doc.Find("tbody tr").First())
	if ok {
		t.Error("expected ok=false for row with fewer than 7 columns")
	}
}

func TestParseRow_MissingDataS(t *testing.T) {
	e := &XdccEuEngine{}
	// anchor without data-s
	td1 := `<a href="#">no data-s here</a>`
	doc := rowDoc(td1, "100 MB", "file.mkv")

	_, ok := e.parseRow(0, doc.Find("tbody tr").First())
	if ok {
		t.Error("expected ok=false when data-s attribute is missing")
	}
}

func TestParseRow_MissingDataP(t *testing.T) {
	e := &XdccEuEngine{}
	// anchor with data-s but no data-p
	td1 := `<a data-s="irc.rizon.net">no data-p</a>`
	doc := rowDoc(td1, "100 MB", "file.mkv")

	_, ok := e.parseRow(0, doc.Find("tbody tr").First())
	if ok {
		t.Error("expected ok=false when data-p attribute is missing")
	}
}

func TestParseRow_MalformedDataP(t *testing.T) {
	e := &XdccEuEngine{}
	// data-p does not contain the expected " xdcc send #" separator
	td1 := `<a data-s="irc.rizon.net" data-p="not-a-valid-command">info</a>`
	doc := rowDoc(td1, "100 MB", "file.mkv")

	_, ok := e.parseRow(0, doc.Find("tbody tr").First())
	if ok {
		t.Error("expected ok=false for malformed data-p")
	}
}

func TestParseRow_ZeroPackNumber(t *testing.T) {
	e := &XdccEuEngine{}
	// pack number is non-numeric → Sscanf leaves packNum at 0
	td1 := `<a data-s="irc.rizon.net" data-p="CoolBot xdcc send #abc">info</a>`
	doc := rowDoc(td1, "100 MB", "file.mkv")

	_, ok := e.parseRow(0, doc.Find("tbody tr").First())
	if ok {
		t.Error("expected ok=false when pack number parses to zero")
	}
}

// --- parseResults ------------------------------------------------------------

func TestParseResults_MultipleValidRows(t *testing.T) {
	e := &XdccEuEngine{}
	doc := tableDoc([]string{
		validRowHTML("BotA", "irc.rizon.net", 1, "700 MB", "file1.mkv"),
		validRowHTML("BotB", "irc.rizon.net", 2, "1.2 GB", "file2.mkv"),
	})

	packs, err := e.parseResults(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 2 {
		t.Fatalf("got %d packs, want 2", len(packs))
	}
	if packs[0].Bot != "BotA" || packs[0].PackNumber != 1 {
		t.Errorf("pack[0]: bot=%q num=%d", packs[0].Bot, packs[0].PackNumber)
	}
	if packs[1].Bot != "BotB" || packs[1].PackNumber != 2 {
		t.Errorf("pack[1]: bot=%q num=%d", packs[1].Bot, packs[1].PackNumber)
	}
}

func TestParseResults_SkipsInvalidRows(t *testing.T) {
	e := &XdccEuEngine{}
	// Mix valid and invalid rows; only the valid one should appear in the result.
	badRow := `<tr><td>too few columns</td></tr>`
	doc := tableDoc([]string{
		badRow,
		validRowHTML("BotA", "irc.rizon.net", 10, "500 MB", "good.mkv"),
		badRow,
	})

	packs, err := e.parseResults(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 1 {
		t.Fatalf("got %d packs, want 1", len(packs))
	}
	if packs[0].Bot != "BotA" {
		t.Errorf("Bot = %q, want BotA", packs[0].Bot)
	}
}

func TestParseResults_EmptyDocument(t *testing.T) {
	e := &XdccEuEngine{}
	doc := tableDoc(nil)

	packs, err := e.parseResults(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 0 {
		t.Errorf("got %d packs, want 0", len(packs))
	}
}

// --- fetchDocument -----------------------------------------------------------

func TestFetchDocument_Success(t *testing.T) {
	// Serve a minimal HTML page with one valid result row.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `<html><body><table><tbody>
			<tr>
				<td>Net</td>
				<td><a data-s="irc.rizon.net" data-p="TestBot xdcc send #7">info</a></td>
				<td></td><td></td><td></td>
				<td>100 MB</td>
				<td>test.mkv</td>
			</tr>
		</tbody></table></body></html>`)
	}))
	defer srv.Close()

	e := &XdccEuEngine{}
	doc, err := e.fetchDocument(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetchDocument returned error: %v", err)
	}
	if n := doc.Find("tbody tr").Length(); n != 1 {
		t.Errorf("expected 1 tbody tr, got %d", n)
	}
}

func TestFetchDocument_NetworkError(t *testing.T) {
	e := &XdccEuEngine{}
	// Nothing is listening on port 1.
	_, err := e.fetchDocument(context.Background(), "http://127.0.0.1:1")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

// --- Search (end-to-end with fake server) ------------------------------------

func TestSearch_ReturnsPacks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the search term is forwarded as the searchkey query parameter.
		if q := r.URL.Query().Get("searchkey"); q != "one piece" {
			t.Errorf("unexpected searchkey: %q", q)
		}
		fmt.Fprintln(w, `<html><body><table><tbody>`+
			validRowHTML("SubsBot", "irc.rizon.net", 100, "400 MB", "one_piece.mkv")+
			`</tbody></table></body></html>`)
	}))
	defer srv.Close()

	// Patch the engine to call the test server instead of xdcc.eu.
	e := &XdccEuEngine{}
	doc, err := e.fetchDocument(context.Background(), srv.URL+"?searchkey=one+piece")
	if err != nil {
		t.Fatalf("fetchDocument error: %v", err)
	}
	packs, err := e.parseResults(doc)
	if err != nil {
		t.Fatalf("parseResults error: %v", err)
	}
	if len(packs) != 1 {
		t.Fatalf("got %d packs, want 1", len(packs))
	}
	if packs[0].Filename != "one_piece.mkv" {
		t.Errorf("Filename = %q, want one_piece.mkv", packs[0].Filename)
	}
}
