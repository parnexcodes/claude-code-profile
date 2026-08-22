//nolint:errcheck,staticcheck,unused
package proxy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractBinary_TarGz(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive.tar.gz")
	// create tar.gz with cli-proxy-api binary
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "cli-proxy-api", Mode: 0o755, Size: int64(len("binarycontent"))}
	tw.WriteHeader(hdr)
	tw.Write([]byte("binarycontent"))
	tw.Close()
	gz.Close()
	os.WriteFile(archive, buf.Bytes(), 0o600)

	dest := t.TempDir()
	got, err := ExtractBinary(archive, dest)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	data, _ := os.ReadFile(got)
	if string(data) != "binarycontent" {
		t.Fatalf("content %q", string(data))
	}
}

func TestExtractBinary_Zip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive.zip")
	f, _ := os.Create(archive)
	zw := zip.NewWriter(f)
	w, _ := zw.Create("cli-proxy-api")
	w.Write([]byte("zipcontent"))
	zw.Close()
	f.Close()

	dest := t.TempDir()
	got, err := ExtractBinary(archive, dest)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	data, _ := os.ReadFile(got)
	if string(data) != "zipcontent" {
		t.Fatalf("content %q", string(data))
	}
}

func TestExtractBinary_Raw(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "rawbinary")
	os.WriteFile(archive, []byte("raw"), 0o600)
	dest := t.TempDir()
	got, err := ExtractBinary(archive, dest)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	data, _ := os.ReadFile(got)
	if string(data) != "raw" {
		t.Fatalf("content %q", string(data))
	}
	if got != filepath.Join(dest, "cli-proxy-api") {
		t.Fatalf("dest %q", got)
	}
}

func TestExtractBinary_NotFound(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "bad.tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "other-file", Mode: 0o644, Size: int64(len("x"))}
	tw.WriteHeader(hdr)
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	os.WriteFile(archive, buf.Bytes(), 0o600)
	dest := t.TempDir()
	if _, err := ExtractBinary(archive, dest); err == nil {
		t.Fatalf("expected error")
	}
}
