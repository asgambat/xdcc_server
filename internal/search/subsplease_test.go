package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- parseSubsPleaseResult ---

func TestParseSubsPleaseResult_Valid(t *testing.T) {
	// Format: varXXX={b: "BotName", n:42, s:700, f:"filename.mkv"}
	input := `var1={b: "SubsBot", n:42, s:700, f:"episode.mkv"}`
	data, err := parseSubsPleaseResult(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data["b"] != "SubsBot" {
		t.Errorf("b = %q, want SubsBot", data["b"])
	}
	if data["n"] != "42" {
		t.Errorf("n = %q, want 42", data["n"])
	}
	if data["s"] != "700" {
		t.Errorf("s = %q, want 700", data["s"])
	}
	if data["f"] != "episode.mkv" {
		t.Errorf("f = %q, want episode.mkv", data["f"])
	}
}

func TestParseSubsPleaseResult_NoEquals(t *testing.T) {
	_, err := parseSubsPleaseResult("no-equals-here")
	if err == nil {
		t.Error("expected error for input without '='")
	}
}

func TestParseSubsPleaseResult_EmptyInput(t *testing.T) {
	_, err := parseSubsPleaseResult("")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseSubsPleaseResult_NumericValues(t *testing.T) {
	// Numeric values (no quotes) should be stored as plain strings.
	input := `x={n:99, s:1024}`
	data, err := parseSubsPleaseResult(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if data["n"] != "99" {
		t.Errorf("n = %q, want 99", data["n"])
	}
	if data["s"] != "1024" {
		t.Errorf("s = %q, want 1024", data["s"])
	}
}

// --- SubsPleaseEngine.Search ---

func TestSubsPleaseSearch_EmptyTerm(t *testing.T) {
	e := &SubsPleaseEngine{}
	packs, err := e.Search(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if packs != nil {
		t.Error("expected nil packs for empty term")
	}
}

func TestSubsPleaseSearch_CloudflareBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	e := &SubsPleaseEngine{baseURL: srv.URL}
	_, err := e.Search(context.Background(), "test")
	if err == nil {
		t.Error("expected error for non-2xx response (CloudFlare block)")
	}
}

func TestSubsPleaseSearch_ValidResults(t *testing.T) {
	body := `var1={b: "SubsBot", n:100, s:700, f:"ep100.mkv"};var2={b: "SubsBot", n:101, s:800, f:"ep101.mkv"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	e := &SubsPleaseEngine{baseURL: srv.URL}
	packs, err := e.Search(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 2 {
		t.Fatalf("got %d packs, want 2", len(packs))
	}
	if packs[0].Bot != "SubsBot" || packs[0].PackNumber != 100 {
		t.Errorf("pack[0]: bot=%q num=%d", packs[0].Bot, packs[0].PackNumber)
	}
	if packs[1].PackNumber != 101 {
		t.Errorf("pack[1].PackNumber = %d, want 101", packs[1].PackNumber)
	}
}

func TestSubsPleaseSearch_SkipsInvalidAndMissingBot(t *testing.T) {
	// "invalid" has no '=', empty bot must be skipped.
	body := `invalid;var1={b: "", n:1, s:100, f:"file.mkv"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	e := &SubsPleaseEngine{baseURL: srv.URL}
	packs, err := e.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 0 {
		t.Errorf("got %d packs, want 0", len(packs))
	}
}

func TestSubsPleaseSearch_NetworkError(t *testing.T) {
	e := &SubsPleaseEngine{baseURL: "http://127.0.0.1:1"}
	_, err := e.Search(context.Background(), "test")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}
