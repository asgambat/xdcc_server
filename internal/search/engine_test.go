package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEngineByName(t *testing.T) {
	cases := []struct {
		input   string
		want    string // expected Name(), empty if nil expected
		wantNil bool
	}{
		{"nibl", "nibl", false},
		{"NIBL", "nibl", false}, // case-insensitive
		{"xdcc-eu", "xdcc-eu", false},
		{"subsplease", "subsplease", false},
		{"SUBSPLEASE", "subsplease", false},
		{"unknown", "", true},
		{"", "", true},
	}
	for _, tt := range cases {
		e := EngineByName(tt.input, false)
		if tt.wantNil {
			if e != nil {
				t.Errorf("EngineByName(%q) = %v, want nil", tt.input, e)
			}
			continue
		}
		if e == nil {
			t.Errorf("EngineByName(%q) = nil, want engine with Name()=%q", tt.input, tt.want)
			continue
		}
		if e.Name() != tt.want {
			t.Errorf("EngineByName(%q).Name() = %q, want %q", tt.input, e.Name(), tt.want)
		}
	}
}

func TestEngineByName_VerboseFlag(t *testing.T) {
	// Verify verbose flag reaches XdccEuEngine without panic.
	e := EngineByName("xdcc-eu", true)
	if e == nil {
		t.Fatal("expected non-nil engine")
	}
	xdcc, ok := e.(*XdccEuEngine)
	if !ok {
		t.Fatal("expected *XdccEuEngine")
	}
	if !xdcc.Verbose {
		t.Error("Verbose should be true")
	}
}

func TestAvailableEngines(t *testing.T) {
	engines := AvailableEngines()
	if len(engines) == 0 {
		t.Fatal("AvailableEngines returned empty slice")
	}
	seen := make(map[string]bool)
	for _, name := range engines {
		if seen[name] {
			t.Errorf("duplicate engine name: %q", name)
		}
		seen[name] = true
		if EngineByName(name, false) == nil {
			t.Errorf("EngineByName(%q) = nil, but it is listed in AvailableEngines", name)
		}
	}
}

func TestResolveBaseURL(t *testing.T) {
	tests := []struct {
		override   string
		defaultURL string
		want       string
	}{
		{"", "https://nibl.co.uk", "https://nibl.co.uk"},
		{"http://localhost:8080", "https://nibl.co.uk", "http://localhost:8080"},
		{"http://test", "https://example.com", "http://test"},
		{"", "https://subsplease.org", "https://subsplease.org"},
	}
	for _, tt := range tests {
		if got := resolveBaseURL(tt.override, tt.defaultURL); got != tt.want {
			t.Errorf("resolveBaseURL(%q, %q) = %q, want %q",
				tt.override, tt.defaultURL, got, tt.want)
		}
	}
}

func TestEngineNames(t *testing.T) {
	cases := []struct {
		engine Engine
		want   string
	}{
		{&NiblEngine{}, "nibl"},
		{&XdccEuEngine{}, "xdcc-eu"},
		{&SubsPleaseEngine{}, "subsplease"},
	}
	for _, tt := range cases {
		if got := tt.engine.Name(); got != tt.want {
			t.Errorf("%T.Name() = %q, want %q", tt.engine, got, tt.want)
		}
	}
}

func TestHttpGet_UserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		if ua != "xdcc_server/1.0" {
			t.Errorf("User-Agent = %q, want xdcc_server/1.0", ua)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := httpGet(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
