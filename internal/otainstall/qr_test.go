package otainstall

import (
	"strings"
	"testing"
	"time"
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

// TestBucketQRSizes pins the code size of each bucket backend's link, as
// documented: a SigV4 presigned manifest URL is ~345 characters, so S3 needs
// version 13 (69 modules, 73 columns with the quiet zone) where a gist needs
// version 6; R2's longer host is version 14 (73); Azure's SAS is short,
// version 10 (57). Temporary AWS credentials put their session token in the
// URL (~105 modules), which the session flags as too wide for 80 columns.
func TestBucketQRSizes(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	aws := AWSCredentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "secret"}
	s3, _ := NewS3(&S3Options{Bucket: "my-app-builds", Region: "eu-central-1", Credentials: aws})
	r2, _ := NewS3(&S3Options{Bucket: "builds", Region: "auto", Endpoint: "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com",
		Credentials: AWSCredentials{AccessKeyID: "0123456789abcdef0123456789abcdef", SecretAccessKey: "secret"}})
	sts, _ := NewS3(&S3Options{Bucket: "my-app-builds", Region: "us-east-1",
		Credentials: AWSCredentials{AccessKeyID: "ASIAIOSFODNN7EXAMPLE", SecretAccessKey: "secret", SessionToken: strings.Repeat("A", 800)}})
	azure, _ := NewAzure(&AzureOptions{Account: "myappbuilds", Container: "builds", Key: "c2VjcmV0"})
	for _, tc := range []struct {
		name string
		b    *Bucket
		max  int
		fits bool
	}{
		{"s3", s3, 69, true}, {"r2", r2, 73, true}, {"azure", azure, 57, true}, {"s3 session token", sts, 105, false},
	} {
		u, err := tc.b.store.presign(BucketDir+"0123abcd/"+manifestObject, time.Hour, now)
		if err != nil {
			t.Fatal(err)
		}
		modules, err := qrModules(Link(u))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(modules); n > tc.max {
			t.Errorf("%s: QR code is %d modules wide, documented as %d", tc.name, n, tc.max)
		}
		if fits := len(modules)+2*quietZone <= wideQR; fits != tc.fits {
			t.Errorf("%s: %d modules, fits 80 columns = %v", tc.name, len(modules), fits)
		}
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
