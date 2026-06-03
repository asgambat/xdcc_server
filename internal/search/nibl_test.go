package search

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNiblSearch_ValidResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "" {
			t.Error("expected query parameter")
		}
		fmt.Fprintln(w, `<html><body><table>
			<tr><th>Bot</th><th>Pack</th><th>Size</th><th>File</th></tr>
			<tr><td>SubsBot</td><td>42</td><td>700 MB</td><td>episode.mkv</td></tr>
			<tr><td>SubsBot</td><td>43</td><td>800 MB</td><td>episode2.mkv</td></tr>
		</table></body></html>`)
	}))
	defer srv.Close()

	e := &NiblEngine{baseURL: srv.URL}
	packs, err := e.Search(context.Background(), "test query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 2 {
		t.Fatalf("got %d packs, want 2", len(packs))
	}
	if packs[0].Bot != "SubsBot" || packs[0].PackNumber != 42 {
		t.Errorf("pack[0]: bot=%q num=%d", packs[0].Bot, packs[0].PackNumber)
	}
	if packs[1].PackNumber != 43 {
		t.Errorf("pack[1].PackNumber = %d, want 43", packs[1].PackNumber)
	}
}

func TestNiblSearch_SkipsHeaderAndZeroPack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `<html><body><table>
			<tr><th>Bot</th><th>Pack</th><th>Size</th><th>File</th></tr>
			<tr><td>Bot</td><td>not-a-number</td><td>100 MB</td><td>file.mkv</td></tr>
		</table></body></html>`)
	}))
	defer srv.Close()

	e := &NiblEngine{baseURL: srv.URL}
	packs, err := e.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 0 {
		t.Errorf("got %d packs, want 0 (zero pack numbers must be skipped)", len(packs))
	}
}

func TestNiblSearch_SkipsRowsWithTooFewColumns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `<html><body><table>
			<tr><td>only</td><td>two</td></tr>
		</table></body></html>`)
	}))
	defer srv.Close()

	e := &NiblEngine{baseURL: srv.URL}
	packs, err := e.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 0 {
		t.Errorf("got %d packs, want 0", len(packs))
	}
}

func TestNiblSearch_NetworkError(t *testing.T) {
	e := &NiblEngine{baseURL: "http://127.0.0.1:1"}
	_, err := e.Search(context.Background(), "test")
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

func TestNiblSearch_EmptyTerm(t *testing.T) {
	// Nibl doesn't check for empty term, but the URL should still work
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `<html><body><table></table></body></html>`)
	}))
	defer srv.Close()

	e := &NiblEngine{baseURL: srv.URL}
	packs, err := e.Search(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(packs) != 0 {
		t.Errorf("expected 0 packs, got %d", len(packs))
	}
}

func TestNiblSearch_EmptyTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `<html><body><table>
			<tr><th>Bot</th><th>Pack</th><th>Size</th><th>File</th></tr>
		</table></body></html>`)
	}))
	defer srv.Close()

	e := &NiblEngine{baseURL: srv.URL}
	packs, err := e.Search(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 0 {
		t.Errorf("expected 0 packs for empty table, got %d", len(packs))
	}
}

func TestNiblSearch_HTTP400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	e := &NiblEngine{baseURL: srv.URL}
	_, err := e.Search(context.Background(), "test")
	if err == nil {
		t.Error("expected error for HTTP 400")
	}
}
