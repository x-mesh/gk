package secrets

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

type zipFile struct {
	name   string
	data   string
	method uint16
}

func buildZip(t *testing.T, files ...zipFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		if f.method == 0 {
			f.method = zip.Deflate
		}
		h := &zip.FileHeader{Name: f.name, Method: f.method}
		if strings.HasSuffix(f.name, "/") {
			h.Method = zip.Store
		}
		create := zw.CreateHeader
		if h.Method != zip.Store && h.Method != zip.Deflate {
			// The writer has no compressor for the method, so the data is
			// written as if already compressed.
			h.CompressedSize64, h.UncompressedSize64 = uint64(len(f.data)), uint64(len(f.data))
			create = zw.CreateRaw
		}
		w, err := create(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIsZipArchive(t *testing.T) {
	for p, want := range map[string]bool{
		"was/app-1.0.jar": true, "APP.WAR": true, "x.ear": true, "dist/site.zip": true,
		"app.jar.sha1": false, "jar": false, "a.tar.gz": false, "lib.aar": false,
	} {
		if got := IsZipArchive(p); got != want {
			t.Errorf("IsZipArchive(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestWalkZip_PassesTextAndBinaryFilesAndCountsUnread(t *testing.T) {
	data := buildZip(t,
		zipFile{name: "BOOT-INF/"},
		zipFile{name: "BOOT-INF/classes/application.yml", data: "a: 1\nb: 2\n"},
		zipFile{name: "BOOT-INF/classes/App.class", data: "\xca\xfe\xba\xbe\x00\x00"},
		zipFile{name: "big.txt", data: strings.Repeat("x", 65)},
		zipFile{name: "odd.txt", data: "z", method: 99},
		zipFile{name: "empty.txt"},
	)
	var got []string
	unread, err := WalkZip(data, ZipLimits{Entries: 10, EntryBytes: 64, TotalBytes: 1 << 20}, func(name, content string) {
		got = append(got, name+"="+content)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BOOT-INF/classes/application.yml=a: 1\nb: 2\n", "BOOT-INF/classes/App.class=", "empty.txt="}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("files = %q, want %q", got, want)
	}
	// big.txt is over the entry limit and odd.txt uses an unknown method.
	if unread != 2 {
		t.Errorf("unread = %d, want 2", unread)
	}
}

// A text file longer than the binary sniff must arrive whole.
func TestWalkZip_LongTextFileIsWhole(t *testing.T) {
	text := strings.Repeat("key: value\n", 2000)
	data := buildZip(t, zipFile{name: "a.yml", data: text})
	var got string
	if _, err := WalkZip(data, ZipLimits{Entries: 1, EntryBytes: 1 << 20, TotalBytes: 1 << 20}, func(_, content string) { got = content }); err != nil {
		t.Fatal(err)
	}
	if got != text {
		t.Errorf("got %d bytes, want %d", len(got), len(text))
	}
}

func TestWalkZip_Rejects(t *testing.T) {
	two := buildZip(t, zipFile{name: "a"}, zipFile{name: "b"})
	cases := map[string][]byte{
		"not a zip":          []byte("plain text named .jar"),
		"truncated":          two[:len(two)-10],
		"too many entries":   two,
		"empty blob":         nil,
		"zip header only":    []byte("PK\x03\x04"),
		"directory past end": append([]byte(nil), two[:len(two)/2]...),
	}
	for name, data := range cases {
		called := false
		if _, err := WalkZip(data, ZipLimits{Entries: 1, EntryBytes: 64, TotalBytes: 1 << 20}, func(string, string) { called = true }); err == nil || called {
			t.Errorf("%s: err=%v called=%v, want an error before any file", name, err, called)
		}
	}
}

// Files that fit the entry limit can still add up without end, so the files
// of one archive share a budget. A file that does not fit what is left is
// not read, and a smaller one after it still is.
func TestWalkZip_FilesShareTheTotalBudget(t *testing.T) {
	data := buildZip(t,
		zipFile{name: "a.txt", data: strings.Repeat("a", 80)},
		zipFile{name: "b.txt", data: strings.Repeat("b", 80)},
		zipFile{name: "c.txt", data: strings.Repeat("c", 30)},
	)
	var got []string
	unread, err := WalkZip(data, ZipLimits{Entries: 10, EntryBytes: 100, TotalBytes: 150}, func(name, _ string) {
		got = append(got, name)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a.txt,c.txt" || unread != 1 {
		t.Errorf("read %v with %d unread, want a.txt,c.txt with 1 unread", got, unread)
	}
}
