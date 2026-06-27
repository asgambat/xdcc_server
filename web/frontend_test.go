package web

import (
	"io/fs"
	"testing"
)

func TestDistIsAccessible(t *testing.T) {
	// Verify the embedded filesystem is not empty
	entries, err := fs.ReadDir(Dist, "dist")
	if err != nil {
		t.Fatalf("cannot read dist directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected dist directory to contain files, got 0 entries")
	}

	// Check that index.html exists (required for SPA serving)
	foundIndex := false
	for _, e := range entries {
		if e.Name() == "index.html" {
			foundIndex = true
			break
		}
	}
	if !foundIndex {
		t.Error("expected index.html in dist directory")
	}
}

func TestDistIndexHTMLContent(t *testing.T) {
	data, err := Dist.ReadFile("dist/index.html")
	if err != nil {
		t.Fatalf("cannot read dist/index.html: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("index.html is empty")
	}
	content := string(data)
	if content == "" {
		t.Fatal("index.html has no content")
	}
	// Minimal check: should be an HTML document
	if len(content) >= 15 && content[:15] != "<!DOCTYPE html>" {
		preview := content
		if len(preview) > 50 {
			preview = preview[:50]
		}
		t.Logf("index.html starts with: %q", preview)
	}
}

func TestDistReadNonExistentFile(t *testing.T) {
	_, err := Dist.ReadFile("dist/nonexistent-file.xyz")
	if err == nil {
		t.Error("expected error reading non-existent file")
	}
}

func TestDistReadDirNonExistent(t *testing.T) {
	_, err := fs.ReadDir(Dist, "dist/nonexistent")
	if err == nil {
		t.Error("expected error reading non-existent directory")
	}
}
