package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxArchiveSize caps the bytes we accept from GitHub. The release archives
// are ~5–10 MB; 64 MB is a generous ceiling that still defends against a
// hostile mirror returning a multi-gigabyte body.
const maxArchiveSize = 64 << 20

// maxChecksumsSize bounds the checksums.txt download. The published file is
// a few hundred bytes — 64 KiB is plenty for a SHA-256 manifest.
const maxChecksumsSize = 64 << 10

// DownloadVerified fetches the release archive for `tag` and `asset`, verifies
// its sha256 against the published checksums.txt, extracts the `gk` binary
// into `dir`, and returns the absolute path to the extracted file.
//
// `dir` is intentionally caller-controlled. For self-update we point it at
// the parent dir of the running binary so the eventual atomic rename does
// not cross filesystem boundaries.
func (c *Client) DownloadVerified(ctx context.Context, tag, asset, dir string) (string, error) {
	expected, err := c.fetchExpectedSum(ctx, tag, asset)
	if err != nil {
		return "", err
	}

	archive, err := c.downloadArchive(ctx, tag, asset, dir)
	if err != nil {
		return "", err
	}
	// downloadArchive's tempfile is removed unconditionally — extraction
	// produces the persistent artefact, the archive itself is throwaway.
	defer os.Remove(archive)

	if err := verifyFileSum(archive, expected); err != nil {
		return "", err
	}

	binPath, err := extractBinary(archive, dir)
	if err != nil {
		return "", err
	}
	return binPath, nil
}

// fetchExpectedSum downloads checksums.txt and pulls out the line for `asset`.
// Format is the standard `<sha256>  <filename>` produced by goreleaser.
func (c *Client) fetchExpectedSum(ctx context.Context, tag, asset string) (string, error) {
	url := c.AssetURL(tag, "checksums.txt")
	body, err := c.getLimited(ctx, url, maxChecksumsSize)
	if err != nil {
		return "", fmt.Errorf("fetch checksums.txt: %w", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", asset)
}

// downloadArchive streams the release archive into a tempfile in `dir` and
// returns its path. Same-directory placement keeps the later os.Rename
// atomic.
func (c *Client) downloadArchive(ctx context.Context, tag, asset, dir string) (string, error) {
	url := c.AssetURL(tag, asset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.doer().Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", asset, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s returned %s", asset, resp.Status)
	}

	f, err := os.CreateTemp(dir, "gk-update-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create temp archive: %w", err)
	}
	tmpPath := f.Name()
	// Best-effort cleanup if anything below fails before we hand the path
	// back to the caller. Successful path also cleans up via defer in
	// DownloadVerified.
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxArchiveSize)); err != nil {
		f.Close()
		cleanup()
		return "", fmt.Errorf("write archive: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", err
	}
	return tmpPath, nil
}

// getLimited issues a GET, errors on non-2xx, and reads at most `maxBytes`.
func (c *Client) getLimited(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.doer().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBytes))
}

// verifyFileSum hashes `path` and compares against the lower-case hex sum
// `expected`. Mismatch returns an error that includes both digests so users
// can copy them into a bug report.
func verifyFileSum(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if actual != strings.ToLower(expected) {
		return fmt.Errorf("checksum mismatch (expected %s, got %s)", expected, actual)
	}
	return nil
}

// extractBinary pulls the `gk` entry out of a goreleaser-shaped tar.gz and
// writes it into `dir/gk.new` with mode 0755. Returns the absolute path.
//
// Refuses to write entries whose name is not exactly "gk" — protects against
// a malicious tar that tries to drop ../../../etc/passwd next to the real
// binary.
func extractBinary(archivePath, dir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("gzip open: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	target := filepath.Join(dir, "gk.new")
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar read: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Reject anything that is not the binary itself. Defends against
		// tarballs that contain extra payloads or path traversal.
		if filepath.Base(hdr.Name) != "gk" || strings.Contains(hdr.Name, "..") {
			continue
		}

		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return "", fmt.Errorf("create %s: %w", target, err)
		}
		if _, err := io.Copy(out, io.LimitReader(tr, maxArchiveSize)); err != nil {
			out.Close()
			_ = os.Remove(target)
			return "", fmt.Errorf("write binary: %w", err)
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(target)
			return "", err
		}
		return target, nil
	}
	return "", fmt.Errorf("archive did not contain a gk binary")
}
