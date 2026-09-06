package fsfs

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	fsapi "github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/hashfile"
)

func TestOpenMetadataAndConfig(t *testing.T) {
	repository := extractFixture(t, 8)
	configuration := []byte("[caches]\nfail-stop = yes\n[deltification]\ncompression = zlib-9\n[io]\nblock-size = 32\nl2p-page-size = 4096\np2l-page-size = 512\n")
	if err := os.WriteFile(filepath.Join(repository, "db", "fsfs.conf"), configuration, 0o644); err != nil {
		t.Fatal(err)
	}
	filesystem, err := Open(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if !filesystem.Config().FailStop || filesystem.Config().Compression != CompressionZlib || filesystem.Config().CompressionLevel != 9 {
		t.Fatalf("config = %+v", filesystem.Config())
	}
	if filesystem.Config().BlockSize != 32*1024 || filesystem.Config().L2PPageSize != 4096 || filesystem.Config().P2LPageSize != 512*1024 {
		t.Fatalf("I/O config = %+v", filesystem.Config())
	}
	if filesystem.MinUnpackedRevision() != 4 {
		t.Fatalf("min unpacked revision = %d", filesystem.MinUnpackedRevision())
	}
	opened, err := fsapi.Open(context.Background(), repository)
	if err != nil || opened.Path() != repository {
		t.Fatalf("fs.Open path = %v, error = %v", opened, err)
	}
}

func TestOpenRejectsMalformedMetadata(t *testing.T) {
	tests := []struct {
		name string
		file string
		data string
		code svn.Code
	}{
		{name: "config syntax", file: "fsfs.conf", data: "[io\n", code: svn.ErrFSCorrupt},
		{name: "config value", file: "fsfs.conf", data: "[io]\nblock-size = 3\n", code: svn.ErrFSCorrupt},
		{name: "minimum", file: "min-unpacked-rev", data: "-1\n", code: svn.ErrFSCorrupt},
		{name: "format", file: "format", data: "9\n", code: svn.ErrFSUnsupportedFormat},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := extractFixture(t, 8)
			name := filepath.Join(repository, "db", test.file)
			if err := os.Chmod(name, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, []byte(test.data), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Open(context.Background(), repository)
			if !errors.Is(err, test.code) {
				t.Fatalf("error = %v, want %v", err, test.code)
			}
		})
	}
}

func TestLogicalIndexCorruption(t *testing.T) {
	filesystem, err := Open(context.Background(), extractFixture(t, 8))
	if err != nil {
		t.Fatal(err)
	}
	name, _, _, err := filesystem.revisionPath(4)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	footer, err := parseLogicalFooter(data)
	if err != nil {
		t.Fatal(err)
	}
	data[int(footer.p2lOffset)+len("P2L-INDEX\n")] ^= 1
	if _, _, err := parseLogicalIndexes(data); !errors.Is(err, svn.ErrFSCorrupt) {
		t.Fatalf("error = %v", err)
	}
}

func TestLogicalIndexesForEveryFixtureRevision(t *testing.T) {
	for _, format := range []int{7, 8} {
		t.Run(fmt.Sprint(format), func(t *testing.T) {
			filesystem, err := Open(context.Background(), extractFixture(t, format))
			if err != nil {
				t.Fatal(err)
			}
			youngest, err := filesystem.YoungestRevision(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for revision := svn.Revnum(0); revision <= youngest; revision++ {
				file, err := filesystem.openRevision(revision)
				if err != nil {
					t.Fatalf("r%d: %v", revision, err)
				}
				if len(file.locations[revision]) == 0 {
					t.Fatalf("r%d has no logical items", revision)
				}
			}
		})
	}
}

func TestMissingFSFSFormatMeansFormatOne(t *testing.T) {
	repository := extractFixture(t, 1)
	name := filepath.Join(repository, "db", "format")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	filesystem, err := Open(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if filesystem.Format().Number != 1 {
		t.Fatalf("format = %d", filesystem.Format().Number)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("read created format file: %v", err)
	}
}

func TestNodeOriginMatchesOldestHistory(t *testing.T) {
	filesystem, err := Open(context.Background(), extractFixture(t, 8))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filesystem.RevisionRoot(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	history, err := root.NodeHistory(context.Background(), "/trunk/README.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := root.NodeOrigin(context.Background(), "/trunk/README.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || origin != history[len(history)-1].Revision {
		t.Fatalf("origin = %d, history = %v", origin, history)
	}
}

func TestReadLocks(t *testing.T) {
	repository := extractFixture(t, 8)
	filesystem, err := Open(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	writeTestLock(t, repository, svn.Lock{Path: "/trunk/README.txt", Token: "opaquelocktoken:test", Owner: "alice", Comment: "editing", IsDAVComment: true, CreationDate: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})
	lock, err := filesystem.GetLock(context.Background(), "trunk/README.txt")
	if err != nil || lock == nil || lock.Owner != "alice" || !lock.IsDAVComment {
		t.Fatalf("lock = %+v, error = %v", lock, err)
	}
	locks, err := filesystem.GetLocks(context.Background(), "/trunk", svn.DepthFiles)
	if err != nil || len(locks) != 1 || locks["/trunk/README.txt"] == nil {
		t.Fatalf("locks = %v, error = %v", locks, err)
	}
}

func writeTestLock(t *testing.T, repository string, lock svn.Lock) {
	t.Helper()
	digest := md5.Sum([]byte(lock.Path))
	name := hex.EncodeToString(digest[:])
	directory := filepath.Join(repository, "db", "locks", name[:3])
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	properties := svn.Props{
		"path":           []byte(lock.Path),
		"token":          []byte(lock.Token),
		"owner":          []byte(lock.Owner),
		"comment":        []byte(lock.Comment),
		"is_dav_comment": []byte("1"),
		"creation_date":  []byte(svn.FormatDate(lock.CreationDate)),
	}
	var data bytes.Buffer
	if err := hashfile.Write(&data, properties); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), data.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLowLevelParserGoldens(t *testing.T) {
	if id, err := ParseID("1.0.r2/3"); err != nil || id.String() != "1.0.r2/3" {
		t.Fatalf("ID = %v, error = %v", id, err)
	}
	if header, err := parseRepresentationHeader([]byte("DELTA 1 2 3")); err != nil || header.base == nil || header.base.Revision != 1 || header.base.Item != 2 || header.base.Length != 3 {
		t.Fatalf("header = %+v, error = %v", header, err)
	}
	directory := []byte("K 4\nname\nV 13\nfile 1.0.r2/3\nEND\nPROPS-END\n")
	if entries, err := parseDirectoryEntries(directory); err != nil || entries["name"].kind != svn.NodeFile {
		t.Fatalf("entries = %v, error = %v", entries, err)
	}
}

func TestFNV1a32x4Reader(t *testing.T) {
	for length := 0; length < 200000; length = length*2 + 1 {
		data := bytes.Repeat([]byte{0, 1, 2, 3, 4, 5, 6}, (length+6)/7)[:length]
		got, err := fnv1a32x4Reader(bytes.NewReader(data), 0, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		if want := fnv1a32x4(data); got != want {
			t.Fatalf("length %d: checksum %08x, want %08x", length, got, want)
		}
	}
}

func TestLogicalIndexCacheIsBounded(t *testing.T) {
	cache := newLogicalIndexCache(2)
	cache.put("one", nil, 1)
	cache.put("two", nil, 2)
	if _, _, found := cache.get("one"); !found {
		t.Fatal("first cache entry is missing")
	}
	cache.put("three", nil, 3)
	if _, _, found := cache.get("two"); found {
		t.Fatal("least recently used entry was retained")
	}
	if cache.list.Len() != 2 {
		t.Fatalf("cache size = %d", cache.list.Len())
	}
}

func FuzzLowLevelParsers(f *testing.F) {
	f.Add([]byte("1.0.r2/3"))
	f.Add([]byte("DELTA 1 2 3"))
	f.Add([]byte("id: 0.0.r0/2\ntype: dir\ncount: 0\ncpath: /\n\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseID(string(data))
		_, _ = parseFormat(data)
		_, _ = parseNodeRevision(data)
		_, _ = parseRepresentation(string(data))
		_, _ = parseRepresentationHeader(data)
		_, _ = parseChanges(data)
		_, _ = parseDirectoryEntries(data)
		_, _ = parseLogicalFooter(data)
		_, _, _ = parseLogicalIndexes(data)
	})
}
