package ircmanager

import (
	"testing"
)

func TestStripIRCFormatting(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text unchanged",
			input: "Hello world",
			want:  "Hello world",
		},
		{
			name:  "bold stripped",
			input: "Hello \x02world\x02!",
			want:  "Hello world!",
		},
		{
			name:  "underline stripped",
			input: "Hello \x1Fworld\x1F!",
			want:  "Hello world!",
		},

		{
			name:  "italic stripped",
			input: "Hello \x1Dworld\x1D!",
			want:  "Hello world!",
		},
		{
			name:  "reverse stripped",
			input: "Hello \x16world\x16!",
			want:  "Hello world!",
		},
		{
			name:  "plain reset stripped",
			input: "Hello \x0Fworld\x0F!",
			want:  "Hello world!",
		},
		{
			name:  "color with one digit stripped",
			input: "Hello \x034world!",
			want:  "Hello world!",
		},
		{
			name:  "color with two digits stripped",
			input: "Hello \x0312world!",
			want:  "Hello world!",
		},
		{
			name:  "color with fg and bg stripped",
			input: "Hello \x034,8world!",
			want:  "Hello world!",
		},
		{
			name:  "color with two-digit fg and bg stripped",
			input: "Hello \x0312,34world!",
			want:  "Hello world!",
		},
		{
			name:  "color alone (no digits) stripped",
			input: "Hello \x03world!",
			want:  "Hello world!",
		},
		{
			name:  "color comma but no bg digits",
			input: "Hello \x034,world!",
			want:  "Hello world!",
		},
		{
			name:  "mixed formatting",
			input: "\x02Bold + \x034,12Colored\x0F plain!",
			want:  "Bold + Colored plain!",
		},
		{
			name:  "realistic topic pattern from arabafernice",
			input: "\x02\x038,8\x16\x1F ArAbAFeNiCe \x0F\x0F \x02\x030,4In.The.Grey.2026\x0F\x02\x03",
			want:  " ArAbAFeNiCe  In.The.Grey.2026",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only control codes",
			input: "\x02\x03\x0F\x16\x1D\x1F",
			want:  "",
		},
		{
			name:  "non-breaking Unicode preserved",
			input: "\x02Caffè\x02",
			want:  "Caffè",
		},
		{
			name:  "emoji preserved",
			input: "\x02Hello 🎉 world\x02",
			want:  "Hello 🎉 world",
		},
		{
			name:  "invalid UTF-8 replaced",
			input: "Hello \xff\xfeworld",
			want:  "Hello \ufffdworld",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripIRCFormatting(tt.input)
			if got != tt.want {
				t.Errorf("stripIRCFormatting(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
