package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeArchive returns a tar.gz body with a single regular file named "gk"
// containing payload, mirroring the goreleaser layout.
func makeArchive(t *testing.T, payload string) []byte {
	t.Helper()
	var gzbuf bytes.Buffer
	gz := gzip.NewWriter(&gzbuf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "gk", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return gzbuf.Bytes()
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestDownloadVerifiedHappyPath(t *testing.T) {
	const tag = "v0.30.0"
	asset := "gk_linux_amd64.tar.gz"
	archive := makeArchive(t, "binary-payload")
	checksum := sha256hex(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n%s  some-other-asset.tar.gz\n", checksum, asset, sha256hex([]byte("noise")))
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{DownloadBase: srv.URL}
	dir := t.TempDir()
	out, err := c.DownloadVerified(context.Background(), tag, asset, dir)
	if err != nil {
		t.Fatalf("DownloadVerified: %v", err)
	}
	if got := filepath.Base(out); got != "gk.new" {
		t.Errorf("out filename = %q, want gk.new", got)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "binary-payload" {
		t.Errorf("extracted body = %q", body)
	}
	// extracted binary must be executable
	info, _ := os.Stat(out)
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("extracted file is not executable: %v", info.Mode())
	}
}

func TestDownloadVerifiedBadChecksum(t *testing.T) {
	const tag = "v0.30.0"
	asset := "gk_linux_amd64.tar.gz"
	archive := makeArchive(t, "real")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			// Lie about the sum.
			fmt.Fprintf(w, "%s  %s\n", sha256hex([]byte("not-the-archive")), asset)
		case strings.HasSuffix(r.URL.Path, "/"+asset):
			_, _ = w.Write(archive)
		}
	}))
	defer srv.Close()

	c := &Client{DownloadBase: srv.URL}
	_, err := c.DownloadVerified(context.Background(), tag, asset, t.TempDir())
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %q, want checksum mismatch", err)
	}
}

func TestDownloadVerifiedAssetMissingFromChecksums(t *testing.T) {
	const tag = "v0.30.0"
	asset := "gk_linux_amd64.tar.gz"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			// Asset not listed.
			fmt.Fprintf(w, "%s  some-other.tar.gz\n", sha256hex([]byte("x")))
		}
	}))
	defer srv.Close()

	c := &Client{DownloadBase: srv.URL}
	_, err := c.DownloadVerified(context.Background(), tag, asset, t.TempDir())
	if err == nil {
		t.Fatal("expected error when asset missing from checksums")
	}
	if !strings.Contains(err.Error(), "no entry") {
		t.Errorf("error = %q, want it to mention missing entry", err)
	}
}

func TestExtractBinaryRejectsTraversal(t *testing.T) {
	// tarball containing only a path-traversal entry — extraction must fail
	// rather than write outside the intended directory.
	var gzbuf bytes.Buffer
	gz := gzip.NewWriter(&gzbuf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../../../tmp/evil", Mode: 0o755, Size: 4, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("evil"))
	_ = tw.Close()
	_ = gz.Close()

	dir := t.TempDir()
	archive := filepath.Join(dir, "a.tar.gz")
	if err := os.WriteFile(archive, gzbuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := extractBinary(archive, dir); err == nil {
		t.Fatal("expected extractBinary to reject path-traversal entry")
	}
}
