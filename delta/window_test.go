package delta

import (
	"bytes"
	"errors"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestApplyWindow(t *testing.T) {
	window := Window{
		SourceLength: 6,
		TargetLength: 16,
		Ops: []Op{
			{Kind: OpSource, Offset: 0, Length: 4},
			{Kind: OpSource, Offset: 4, Length: 2},
			{Kind: OpNew, Offset: 0, Length: 4},
			{Kind: OpTarget, Offset: 6, Length: 6},
		},
		NewData: []byte("wxyz"),
	}
	got, err := ApplyWindow([]byte("abcdef"), window)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte("abcdefwxyzwxyzwx"); !bytes.Equal(got, want) {
		t.Fatalf("ApplyWindow() = %q, want %q", got, want)
	}
}

func TestApplyWindowOverlappingTargetCopy(t *testing.T) {
	window := Window{
		TargetLength: 12,
		Ops: []Op{
			{Kind: OpNew, Offset: 0, Length: 5},
			{Kind: OpTarget, Offset: 0, Length: 7},
		},
		NewData: []byte("world"),
	}
	got, err := ApplyWindow(nil, window)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "worldworldwo" {
		t.Fatalf("ApplyWindow() = %q", got)
	}
}

func TestApplyWindowRejectsInvalidOperations(t *testing.T) {
	tests := []Window{
		{SourceLength: 1, TargetLength: 2, Ops: []Op{{Kind: OpSource, Length: 2}}},
		{TargetLength: 1, Ops: []Op{{Kind: OpTarget, Length: 1}}},
		{TargetLength: 2, Ops: []Op{{Kind: OpNew, Length: 1}}, NewData: []byte("a")},
		{TargetLength: 1, Ops: []Op{{Kind: 99, Length: 1}}},
	}
	for _, window := range tests {
		if _, err := ApplyWindow(make([]byte, window.SourceLength), window); !errors.Is(err, svn.ErrSvndiffInvalidOps) {
			t.Errorf("ApplyWindow(%+v) error = %v", window, err)
		}
	}
}

func TestApplyStreamsWindowsAndChecksMD5(t *testing.T) {
	source := bytes.NewReader([]byte("0123456789"))
	windows := Windows(
		Window{SourceOffset: 2, SourceLength: 3, TargetLength: 3, Ops: []Op{{Kind: OpSource, Length: 3}}},
		Window{TargetLength: 3, Ops: []Op{{Kind: OpNew, Length: 3}}, NewData: []byte("abc")},
	)
	var target bytes.Buffer
	expected := svn.Sum(svn.ChecksumMD5, []byte("234abc"))
	actual, err := Apply(source, &target, windows, &expected)
	if err != nil {
		t.Fatal(err)
	}
	if target.String() != "234abc" || !actual.Equal(expected) {
		t.Fatalf("Apply() wrote %q with checksum %s", target.String(), actual.Hex())
	}
}
