package main

import (
	"testing"

	"xdcc-go/internal/entities"
)

func TestPackageCompiles(t *testing.T) {
	// Core logic is tested in internal/entities and internal/search packages.
	// This test ensures the cmd package compiles correctly.
}

// --- filterByPrefix ---

func TestFilterByPrefix_MatchesPrefix(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPackWithName("My Show - S01E01.mkv", 1),
		mkPackWithName("My Show - S01E02.mkv", 2),
		mkPackWithName("Other Show - S01E01.mkv", 3),
	}
	result := filterByPrefix(packs, "my show")
	if len(result) != 2 {
		t.Fatalf("got %d packs, want 2", len(result))
	}
}

func TestFilterByPrefix_CaseInsensitive(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPackWithName("My Show - S01E01.mkv", 1),
	}
	result := filterByPrefix(packs, "MY SHOW")
	if len(result) != 1 {
		t.Fatalf("got %d packs, want 1", len(result))
	}
}

func TestFilterByPrefix_NoMatch(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPackWithName("Other Show - S01E01.mkv", 1),
	}
	result := filterByPrefix(packs, "My Show")
	if len(result) != 0 {
		t.Errorf("expected 0 results, got %d", len(result))
	}
}

func TestFilterByPrefix_EmptyList(t *testing.T) {
	result := filterByPrefix(nil, "test")
	if len(result) != 0 {
		t.Errorf("expected 0 results for nil input, got %d", len(result))
	}
}

func TestFilterByPrefix_SubstringNotPrefix(t *testing.T) {
	packs := []*entities.XDCCPack{
		mkPackWithName("[SubGroup] My Show - S01E01.mkv", 1),
	}
	result := filterByPrefix(packs, "My Show")
	if len(result) != 0 {
		t.Errorf("substring match should not pass prefix filter, got %d", len(result))
	}
}

func mkPackWithName(name string, packNum int) *entities.XDCCPack {
	p := entities.NewXDCCPack(entities.NewIrcServer("irc.rizon.net"), "Bot", packNum)
	p.SetFilename(name, true)
	return p
}
