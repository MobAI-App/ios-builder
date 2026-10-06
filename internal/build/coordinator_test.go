package build

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func ipaArtifact(t *testing.T, corrupt bool) ([]byte, []byte) {
	t.Helper()
	payload := []byte("IPA bytes from the remote build")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateHeader(&zip.FileHeader{Name: "App.ipa", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if corrupt {
		r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		offset, err := r.File[0].DataOffset()
		if err != nil {
			t.Fatal(err)
		}
		data[offset] ^= 0xff // Keep the ZIP readable but fail the entry's CRC check.
	}
	return data, payload
}

func TestExtractIPAFromZipChecksumFailure(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new destination"
		if existing {
			name = "existing destination"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "App.ipa")
			original := []byte("previous successful build")
			if existing {
				if err := os.WriteFile(dest, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, _ := ipaArtifact(t, true)
			if _, err := extractIPAFromZip(data, dest); !errors.Is(err, zip.ErrChecksum) {
				t.Fatalf("expected checksum failure, got %v", err)
			}
			got, err := os.ReadFile(dest)
			if existing {
				if err != nil || !bytes.Equal(got, original) {
					t.Fatalf("failed extraction replaced the previous IPA: %q, %v", got, err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed extraction left an IPA at the destination: %q, %v", got, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if existing {
				want = 1
			}
			if len(entries) != want {
				t.Fatalf("temporary files left after failed extraction: %v", entries)
			}
		})
	}
}

func TestExtractIPAFromZipSuccess(t *testing.T) {
	data, payload := ipaArtifact(t, false)
	dir := t.TempDir()
	dest := filepath.Join(dir, "App.ipa")
	if err := os.WriteFile(dest, []byte("old build"), 0600); err != nil {
		t.Fatal(err)
	}
	size, err := extractIPAFromZip(data, dest)
	if err != nil || size != int64(len(payload)) {
		t.Fatalf("extraction: size %d, error %v", size, err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("extracted bytes: %q, %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected output files: %v, %v", entries, err)
	}
}

func TestExtractIPAFromZipRenameFailure(t *testing.T) {
	data, _ := ipaArtifact(t, false)
	dir := t.TempDir()
	dest := filepath.Join(dir, "App.ipa")
	// An existing directory cannot be replaced by the completed temporary file.
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := extractIPAFromZip(data, dest); err == nil {
		t.Fatal("expected saving to a directory to fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "App.ipa" || !entries[0].IsDir() {
		t.Fatalf("destination changed or temporary file leaked: %v, %v", entries, err)
	}
}
