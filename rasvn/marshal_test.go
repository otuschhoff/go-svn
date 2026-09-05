package rasvn

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestDecodeReferenceGreeting(t *testing.T) {
	wire := "( success ( 2 2 ( ) ( edit-pipeline svndiff1 accepts-svndiff2 ) ) ) "
	item, err := NewReader(strings.NewReader(wire)).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Word != "success" {
		t.Fatalf("unexpected greeting: %#v", item)
	}
	params := item.List[1].List
	if params[0].Number != 2 || params[1].Number != 2 || params[3].List[2].Word != "accepts-svndiff2" {
		t.Fatalf("unexpected greeting parameters: %#v", params)
	}
}

func TestItemRoundTrip(t *testing.T) {
	want := List(
		Word("command"),
		Number(^uint64(0)),
		String([]byte{'a', 0, '\n', 0xff}),
		List(Word("true"), String(nil)),
	)
	var wire bytes.Buffer
	writer := NewWriter(&wire)
	if err := writer.Encode(want); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	got, err := NewReader(&wire).Decode()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestReaderRejectsMalformedItems(t *testing.T) {
	tests := []string{
		"word)",
		"1",
		"3:ab ",
		"18446744073709551616 ",
		"67108865:x ",
		strings.Repeat("( ", MaxNestingDepth+1) + strings.Repeat(") ", MaxNestingDepth+1),
	}
	for _, wire := range tests {
		_, err := NewReader(strings.NewReader(wire)).Decode()
		if !errors.Is(err, svn.ErrRASvnMalformedData) {
			t.Errorf("Decode(%q) error = %v", wire, err)
		}
	}
}

func TestTupleOptionalAndNested(t *testing.T) {
	items := []Item{String([]byte("path")), List(), Word("true"), Number(99)}
	var path string
	var revision svn.Revnum
	var enabled bool
	if err := ParseTuple(items, "c(?r)b", &path, &revision, &enabled); err != nil {
		t.Fatal(err)
	}
	if path != "path" || revision != svn.InvalidRevnum || !enabled {
		t.Fatalf("parsed path=%q revision=%d enabled=%v", path, revision, enabled)
	}

	var wire bytes.Buffer
	writer := NewWriter(&wire)
	if err := writer.WriteTuple("c(?r)b", path, svn.InvalidRevnum, enabled); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := wire.String(), "( 4:path ( ) true ) "; got != want {
		t.Fatalf("wire = %q, want %q", got, want)
	}
}

func TestTupleBangSuppressesOuterList(t *testing.T) {
	var wire bytes.Buffer
	writer := NewWriter(&wire)
	if err := writer.WriteTuple("!wr", "get-rev", svn.Revnum(7)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := wire.String(), "get-rev 7 "; got != want {
		t.Fatalf("wire = %q, want %q", got, want)
	}
}

func TestReferenceTranscriptItemGoldens(t *testing.T) {
	paths, err := filepath.Glob("../testdata/transcripts/rasvn/*.transcript")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no ra_svn transcripts")
	}
	for _, path := range paths {
		path := path
		t.Run(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), func(t *testing.T) {
			testTranscriptGolden(t, path)
		})
	}
}

func testTranscriptGolden(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	streams := make(map[string][]byte)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, encoded, ok := strings.Cut(scanner.Text(), ": ")
		if !ok || name == "" || strings.HasPrefix(name, "#") {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		streams[name] = decoded
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(path, "read-matrix.transcript") {
		words := make(map[string]bool)
		collectTranscriptWords(t, streams["client-base64"], words)
		for _, command := range []string{
			"get-latest-rev", "get-dated-rev", "rev-proplist", "rev-prop",
			"check-path", "stat", "get-file", "get-dir", "list", "log",
			"get-locations", "get-location-segments", "get-file-revs",
			"get-mergeinfo", "get-iprops", "get-deleted-rev", "get-lock", "get-locks",
			"update", "switch", "status", "diff", "replay", "replay-range", "reparent",
		} {
			if !words[command] {
				t.Errorf("transcript does not contain command %q", command)
			}
		}
	}
	for name, wire := range streams {
		t.Run(name, func(t *testing.T) {
			reader := NewReader(bytes.NewReader(wire))
			var encoded bytes.Buffer
			writer := NewWriter(&encoded)
			items := 0
			for {
				item, err := reader.Decode()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("item %d: %v", items, err)
				}
				if err := writer.Encode(item); err != nil {
					t.Fatal(err)
				}
				items++
			}
			if err := writer.Flush(); err != nil {
				t.Fatal(err)
			}
			if items == 0 {
				t.Fatal("transcript contains no items")
			}
			if !bytes.Equal(encoded.Bytes(), wire) {
				t.Fatalf("canonical round trip changed %s transcript", name)
			}
		})
	}
}

func collectTranscriptWords(t *testing.T, wire []byte, words map[string]bool) {
	t.Helper()
	reader := NewReader(bytes.NewReader(wire))
	for {
		item, err := reader.Decode()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		walkTranscriptWords(item, words)
	}
}

func walkTranscriptWords(item Item, words map[string]bool) {
	if item.Kind == WordKind {
		words[item.Word] = true
	}
	for _, child := range item.List {
		walkTranscriptWords(child, words)
	}
}

func FuzzItemRoundTrip(fuzz *testing.F) {
	fuzz.Add([]byte("payload"))
	fuzz.Add([]byte{0, 1, '\n', 0xff})
	fuzz.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		want := List(Word("data"), String(data), Number(uint64(len(data))))
		var wire bytes.Buffer
		writer := NewWriter(&wire)
		if err := writer.Encode(want); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		got, err := NewReader(&wire).Decode()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip mismatch")
		}
	})
}

func FuzzReader(fuzz *testing.F) {
	fuzz.Add([]byte("( success ( 2 2 ( ) ( edit-pipeline ) ) ) "))
	fuzz.Add([]byte("3:abc "))
	fuzz.Add([]byte("( ( ( ) ) ) "))
	fuzz.Fuzz(func(t *testing.T, wire []byte) {
		if len(wire) > 1<<20 {
			t.Skip()
		}
		reader := NewReader(bytes.NewReader(wire))
		for count := 0; count < 1000; count++ {
			if _, err := reader.Decode(); err != nil {
				return
			}
		}
	})
}
