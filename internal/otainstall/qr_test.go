package otainstall

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// representativeLink is what a real session prints: a gist's short raw URL
// inside the itms-services wrapper.
const representativeLink = "itms-services://?action=download-manifest&url=https://gist.githubusercontent.com/Interlap01/0123456789abcdef0123456789abcdef/raw"

func TestQRFitsATerminal(t *testing.T) {
	modules, err := qrModules(representativeLink)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(modules); n > 41 {
		t.Errorf("QR code is %d modules wide; too wide for a terminal", n)
	}
}

func TestQRRendersHalfBlocks(t *testing.T) {
	modules, err := qrModules("hello")
	if err != nil {
		t.Fatal(err)
	}
	out, err := QR("hello", false)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	size := len(modules) + 2*quietZone
	if want := (size + 1) / 2; len(lines) != want {
		t.Errorf("%d lines, want %d", len(lines), want)
	}
	for i, line := range lines {
		if utf8.RuneCountInString(line) != size {
			t.Errorf("line %d is %d cells wide, want %d", i, utf8.RuneCountInString(line), size)
		}
	}
	// The quiet zone is light: full blocks on the first line and at both ends.
	if !strings.HasPrefix(lines[0], "██") || !strings.HasSuffix(lines[0], "██") || strings.Trim(lines[0], "█") != "" {
		t.Errorf("first line is not a light quiet zone: %q", lines[0])
	}
	// The finder pattern's top-left module is dark, two modules in.
	if r := []rune(lines[1])[quietZone]; r != ' ' && r != '▀' && r != '▄' {
		t.Errorf("finder pattern not dark: %q", r)
	}
	inverted, err := QR("hello", true)
	if err != nil {
		t.Fatal(err)
	}
	if inverted == out || !strings.HasPrefix(inverted, "  ") {
		t.Error("inverted rendering should swap light and dark")
	}
}
