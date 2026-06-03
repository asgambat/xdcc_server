package cli

import "testing"

func TestVerbosityLevel(t *testing.T) {
	tests := []struct {
		verbose, quiet int
		want           int
	}{
		{0, 0, 0},  // default
		{1, 0, 1},  // -v
		{2, 0, 2},  // -vv
		{0, 1, -1}, // -q
		{0, 2, -2}, // -qq
		{1, 1, -1}, // -v -q → -q wins
		{2, 1, -1}, // -vv -q → -q wins
		{1, 2, -2}, // -v -qq → -qq wins
		{2, 2, -2}, // -vv -qq → -qq wins
		{0, 3, -2}, // extra quiet still -2
	}
	for _, tt := range tests {
		got := VerbosityLevel(tt.verbose, tt.quiet)
		if got != tt.want {
			t.Errorf("VerbosityLevel(%d, %d) = %d, want %d",
				tt.verbose, tt.quiet, got, tt.want)
		}
	}
}

func TestVerbosityLevel_NegativeVerbose(t *testing.T) {
	// Negative verbose with no quiet → passed through
	got := VerbosityLevel(-5, 0)
	if got != -5 {
		t.Errorf("VerbosityLevel(-5, 0) = %d, want -5", got)
	}
}

func TestVerbosityLevel_LargeVerbose(t *testing.T) {
	got := VerbosityLevel(100, 0)
	if got != 100 {
		t.Errorf("VerbosityLevel(100, 0) = %d, want 100", got)
	}
}
