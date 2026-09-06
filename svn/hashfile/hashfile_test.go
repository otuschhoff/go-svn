package hashfile

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestReadWrite(t *testing.T) {
	fixture := "K 7\nsvn:log\nV 11\nline1\nline2\nK 10\nsvn:author\nV 3\noli\nEND\n"
	properties, err := Read(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(properties["svn:log"]); got != "line1\nline2" {
		t.Fatalf("svn:log = %q", got)
	}
	var encoded bytes.Buffer
	if err := Write(&encoded, properties); err != nil {
		t.Fatal(err)
	}
	want := "K 10\nsvn:author\nV 3\noli\nK 7\nsvn:log\nV 11\nline1\nline2\nEND\n"
	if encoded.String() != want {
		t.Fatalf("Write = %q, want %q", encoded.String(), want)
	}
	roundTrip, err := Read(&encoded)
	if err != nil || !equalProps(properties, roundTrip) {
		t.Fatalf("round trip = %#v, %v", roundTrip, err)
	}
}

func TestReadSVNAdminRevpropsFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/revprops-0")
	if err != nil {
		t.Fatal(err)
	}
	properties, err := Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(properties["svn:date"]); got != "2026-09-05T16:48:12.273626Z" {
		t.Fatalf("svn:date = %q", got)
	}
}

func TestReadIncremental(t *testing.T) {
	base := svn.Props{"delete": []byte("old"), "replace": []byte("old"), "keep": []byte("yes")}
	fixture := "D 6\ndelete\nK 7\nreplace\nV 3\nnew\nK 6\nbinary\nV 4\n\x00\n\xffx\nEND\n"
	properties, err := ReadIncremental(strings.NewReader(fixture), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := properties["delete"]; exists || string(properties["replace"]) != "new" || string(properties["keep"]) != "yes" || !bytes.Equal(properties["binary"], []byte{0, '\n', 0xff, 'x'}) {
		t.Fatalf("incremental result = %#v", properties)
	}
	if string(base["delete"]) != "old" || string(base["replace"]) != "old" {
		t.Fatalf("base was mutated: %#v", base)
	}
}

func TestReadRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		code  svn.Code
	}{
		{"empty", "", svn.ErrIncompleteData},
		{"truncated header", "K 3", svn.ErrIncompleteData},
		{"unknown record", "X 1\na\nEND\n", svn.ErrMalformedFile},
		{"signed length", "K -1\n", svn.ErrMalformedFile},
		{"nondecimal length", "K x\n", svn.ErrMalformedFile},
		{"missing payload", "K 3\nab", svn.ErrIncompleteData},
		{"missing payload newline", "K 3\nabcX", svn.ErrMalformedFile},
		{"key without value", "K 1\na\nEND\n", svn.ErrMalformedFile},
		{"value first", "V 1\na\nEND\n", svn.ErrMalformedFile},
		{"delete in full", "D 1\na\nEND\n", svn.ErrMalformedFile},
		{"oversize", "K 67108865\n", svn.ErrMalformedFile},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Read(strings.NewReader(test.input))
			if !errors.Is(err, test.code) {
				t.Fatalf("Read error = %v, want %v", err, test.code)
			}
		})
	}
}

func FuzzRead(f *testing.F) {
	f.Add([]byte("END\n"))
	f.Add([]byte("K 1\na\nV 1\nb\nEND\n"))
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = Read(bytes.NewReader(input))
	})
}

func equalProps(first, second svn.Props) bool {
	if len(first) != len(second) {
		return false
	}
	for key, value := range first {
		if !bytes.Equal(value, second[key]) {
			return false
		}
	}
	return true
}
