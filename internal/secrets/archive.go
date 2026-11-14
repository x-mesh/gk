package secrets

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

// ArchiveSeparator joins an archive path and the path of a file inside it,
// as in app.jar!BOOT-INF/classes/application.yml. Gitleaks reports archive
// entries the same way, so a path allowlist written for it matches here too.
const ArchiveSeparator = "!"

// binarySniff is how far git looks for a NUL byte to call a file binary.
const binarySniff = 8000

// LooksBinary reports whether data is binary by the test git uses: a NUL
// byte in the first 8000 bytes.
func LooksBinary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), binarySniff)], 0) >= 0
}

// IsZipArchive reports whether path names an archive in the zip format that
// the secret scan opens. Gitleaks identifies archives by name and does not
// treat .jar, .war, or .ear as zip files, so it never reads the
// configuration packed into a Java build.
func IsZipArchive(p string) bool {
	return slices.Contains(ZipArchiveExts, strings.ToLower(path.Ext(p)))
}

// ZipArchiveExts are the lowercase file extensions that IsZipArchive accepts.
var ZipArchiveExts = []string{".jar", ".war", ".ear", ".zip"}

// lfsPointer starts the text Git LFS commits in place of a file it stores.
const lfsPointer = "version https://git-lfs.github.com/spec/v1\n"

// IsLFSPointer reports whether data is a Git LFS pointer, which names a file
// kept outside the repository.
func IsLFSPointer(data []byte) bool { return bytes.HasPrefix(data, []byte(lfsPointer)) }

// ZipLimits bound what WalkZip reads from one archive. A repository can hold
// any blob, including one built to expand without end.
type ZipLimits struct {
	// Entries is the most entries an archive may list.
	Entries int
	// EntryBytes is the largest decompressed size of one entry.
	EntryBytes int64
	// TotalBytes is the most that the entries of one archive may
	// decompress to together.
	TotalBytes int64
}

// WalkZip calls fn for each regular file in the zip archive data, in archive
// order, and returns how many files it could not read. Content is "" for a
// binary file, so only path rules apply to it. A file that is larger than
// limits.EntryBytes or than what is left of limits.TotalBytes, that uses a
// compression method Go cannot read, or whose data is corrupt is not passed
// to fn. Archives inside the archive are not
// opened: content scanned there is the archive's own configuration, and a
// nested jar is a dependency.
func WalkZip(data []byte, limits ZipLimits, fn func(name, content string)) (unread int, err error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	// ErrInsecurePath comes with a usable reader. The names are only
	// reported, never written to disk.
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return 0, err
	}
	if len(zr.File) > limits.Entries {
		return 0, fmt.Errorf("%d entries, more than the limit of %d", len(zr.File), limits.Entries)
	}
	left := limits.TotalBytes
	for _, f := range zr.File {
		if !f.Mode().IsRegular() {
			continue
		}
		content, used, ok := readZipFile(f, min(limits.EntryBytes, left))
		left -= used
		if !ok {
			unread++
			continue
		}
		fn(f.Name, content)
	}
	return unread, nil
}

// readZipFile returns the content of f, "" for a binary file, and how many
// bytes it decompressed.
func readZipFile(f *zip.File, maxBytes int64) (string, int64, bool) {
	// archive/zip fails a read that passes the size in the header, so the
	// header bounds how much a file can expand.
	if f.UncompressedSize64 > uint64(maxBytes) {
		return "", 0, false
	}
	rc, err := f.Open()
	if err != nil {
		return "", 0, false
	}
	defer rc.Close()
	head := make([]byte, binarySniff)
	n, err := io.ReadFull(rc, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", int64(n), false
	}
	if LooksBinary(head[:n]) {
		return "", int64(n), true
	}
	rest, err := io.ReadAll(rc)
	used := int64(n + len(rest))
	if err != nil {
		return "", used, false
	}
	return string(head[:n]) + string(rest), used, true
}
