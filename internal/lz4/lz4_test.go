package lz4

import (
	"bytes"
	"testing"
)

func TestDecodeSubversionReferenceVector(t *testing.T) {
	compressed := []byte{
		0xc0, 'a', 'a', 'a', 'a', 'b', 'b', 'b', 'b', 'c', 'c', 'c', 'c',
		0x0c, 0x00, 0x00, 0x08, 0x00, 0x00, 0x10, 0x00, 0x00, 0x0c, 0x00,
		0x08, 0x08, 0x00, 0x00, 0x18, 0x00, 0x00, 0x14, 0x00, 0x00, 0x08,
		0x00, 0x08, 0x18, 0x00, 0x00, 0x14, 0x00, 0x00, 0x10, 0x00, 0x00,
		0x18, 0x00, 0x00, 0x0c, 0x00, 0x00, 0x08, 0x00, 0x00, 0x10, 0x00,
		0x90, 'a', 'a', 'a', 'a', 'b', 'b', 'b', 'b', 0,
	}
	want := []byte("aaaabbbbccccaaaaccccbbbbaaaabbbbaaaabbbbccccaaaaccccbbbbaaaabbbbaaaabbbbccccaaaaccccbbbbaaaabbbb\x00")
	got, err := DecodeBlock(compressed, len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded = %q, want %q", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	inputs := [][]byte{nil, []byte("a"), bytes.Repeat([]byte("abcd"), 1000), bytes.Repeat([]byte{0, 1, 2, 3, 4}, 300)}
	for _, input := range inputs {
		encoded := EncodeBlock(input)
		decoded, err := DecodeBlock(encoded, len(input))
		if err != nil || !bytes.Equal(decoded, input) {
			t.Fatalf("round trip length %d: %v", len(input), err)
		}
	}
}

func TestEncodeLeavesRequiredTerminalLiterals(t *testing.T) {
	input := bytes.Repeat([]byte("abcd"), 1000)
	encoded := EncodeBlock(input)
	position := 0
	lastLiteralLength := -1
	lastMatchStart := -1
	decodedLength := 0
	for position < len(encoded) {
		token := encoded[position]
		position++
		literalLength, err := readLength(encoded, &position, int(token>>4))
		if err != nil {
			t.Fatal(err)
		}
		position += literalLength
		decodedLength += literalLength
		lastLiteralLength = literalLength
		if position == len(encoded) {
			break
		}
		if position+2 > len(encoded) {
			t.Fatal("truncated encoded offset")
		}
		position += 2
		matchLength, err := readLength(encoded, &position, int(token&0x0f))
		if err != nil {
			t.Fatal(err)
		}
		lastMatchStart = decodedLength
		decodedLength += matchLength + 4
	}
	if lastLiteralLength < lastLiterals {
		t.Fatalf("last literal run = %d, want at least %d", lastLiteralLength, lastLiterals)
	}
	if lastMatchStart > len(input)-matchFindLimit {
		t.Fatalf("last match starts at %d, limit %d", lastMatchStart, len(input)-matchFindLimit)
	}
}

func TestDecodeRejectsMalformedBlocks(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		size int
	}{
		{"truncated literal", []byte{0x20, 'a'}, 2},
		{"truncated offset", []byte{0x10, 'a', 1}, 5},
		{"zero offset", []byte{0x10, 'a', 0, 0}, 5},
		{"offset before output", []byte{0x10, 'a', 2, 0}, 5},
		{"truncated literal length", []byte{0xf0}, 15},
		{"truncated match length", []byte{0x1f, 'a', 1, 0}, 20},
		{"output overflow", []byte{0x10, 'a', 1, 0}, 4},
		{"wrong output size", []byte{0x10, 'a'}, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeBlock(test.data, test.size); err == nil {
				t.Fatal("accepted malformed block")
			}
		})
	}
}

func FuzzDecodeBlock(f *testing.F) {
	f.Add([]byte{0}, 0)
	f.Add([]byte{0x10, 'a'}, 1)
	f.Fuzz(func(t *testing.T, data []byte, size int) {
		if size < 0 || size > 1<<20 {
			return
		}
		_, _ = DecodeBlock(data, size)
	})
}
