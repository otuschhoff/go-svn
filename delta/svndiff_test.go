package delta

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestDecodeSvndiff0Golden(t *testing.T) {
	data := []byte{
		'S', 'V', 'N', 0,
		0, 12, 16, 7, 1,
		0x04, 0x00, 0x04, 0x08, 0x81, 0x47, 0x08,
		'd',
	}
	decoder, err := NewDecoder(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	window, err := decoder.NextWindow()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ApplyWindow([]byte("aaaabbbbcccc"), *window)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "aaaaccccdddddddd" {
		t.Fatalf("decoded target = %q", got)
	}
	if _, err := decoder.NextWindow(); !errors.Is(err, io.EOF) {
		t.Fatalf("final NextWindow() error = %v", err)
	}
}

func TestSvndiffRoundTrip(t *testing.T) {
	newData := bytes.Repeat([]byte("compressible-data-"), 80)
	window := Window{
		SourceOffset: 10,
		SourceLength: 4,
		TargetLength: 4 + len(newData),
		Ops: []Op{
			{Kind: OpSource, Offset: 0, Length: 4},
			{Kind: OpNew, Offset: 0, Length: len(newData)},
		},
		NewData: newData,
	}
	for _, version := range []int{0, 1, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			var encoded bytes.Buffer
			encoder, err := NewEncoder(&encoded, version)
			if err != nil {
				t.Fatal(err)
			}
			if err := encoder.WriteWindow(window); err != nil {
				t.Fatal(err)
			}
			if err := encoder.Close(); err != nil {
				t.Fatal(err)
			}
			decoder, err := NewDecoder(bytes.NewReader(encoded.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := decoder.NextWindow()
			if err != nil {
				t.Fatal(err)
			}
			got, err := ApplyWindow([]byte("base"), *decoded)
			if err != nil {
				t.Fatal(err)
			}
			want := append([]byte("base"), newData...)
			if !bytes.Equal(got, want) {
				t.Fatal("round-trip target differs")
			}
		})
	}
}

func TestSvndiffStreamAPI(t *testing.T) {
	var encoded bytes.Buffer
	handler := NewSvndiffWriter(&encoded, 1, zlib.BestSpeed)
	want := []byte("stream contents")
	if _, err := SendContents(want, handler); err != nil {
		t.Fatal(err)
	}
	reader, err := NewSvndiffReader(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if _, err := Apply(nil, &got, reader, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("got %q, want %q", got.Bytes(), want)
	}
}

func TestSvndiffRejectsMalformedStreams(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		code svn.Code
	}{
		{"bad header", []byte("BAD\x00"), svn.ErrSvndiffInvalidHeader},
		{"unknown version", []byte("SVN\x03"), svn.ErrSvndiffInvalidHeader},
		{"truncated header", []byte("SVN\x00\x00"), svn.ErrSvndiffUnexpectedEnd},
		{"truncated section", []byte("SVN\x00\x00\x00\x01\x01\x00"), svn.ErrSvndiffUnexpectedEnd},
		{"invalid selector", []byte("SVN\x00\x00\x00\x01\x01\x00\xff"), svn.ErrSvndiffInvalidOps},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoder, err := NewDecoder(bytes.NewReader(test.data))
			if err == nil {
				_, err = decoder.NextWindow()
			}
			if !errors.Is(err, test.code) {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestSvndiffRejectsMalformedCompressedSections(t *testing.T) {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write([]byte{0x81})
	_ = writer.Close()
	zlibWithTrailingData := append(compressed.Bytes(), 0xff)
	tests := []struct {
		name    string
		version byte
		section []byte
	}{
		{"zlib trailing data", 1, append([]byte{1}, zlibWithTrailingData...)},
		{"lz4 zero offset", 2, []byte{5, 0x10, 'a', 0, 0}},
		{"lz4 truncated length", 2, []byte{16, 0xf0}},
		{"lz4 output mismatch", 2, []byte{2, 0x10, 'a'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := []byte{'S', 'V', 'N', test.version, 0, 0, 1, byte(len(test.section)), 0}
			data = append(data, test.section...)
			decoder, err := NewDecoder(bytes.NewReader(data))
			if err == nil {
				_, err = decoder.NextWindow()
			}
			if !errors.Is(err, svn.ErrSvndiffInvalidCompressedData) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func FuzzDecoder(f *testing.F) {
	f.Add([]byte("SVN\x00"))
	f.Add([]byte("SVN\x01\x00\x00\x00\x01\x00\x81"))
	f.Fuzz(func(t *testing.T, data []byte) {
		decoder, err := NewDecoder(bytes.NewReader(data))
		if err != nil {
			return
		}
		for count := 0; count < 1000; count++ {
			_, err := decoder.NextWindow()
			if err != nil {
				return
			}
		}
		t.Fatal("decoder accepted more than 1000 windows from bounded input")
	})
}
