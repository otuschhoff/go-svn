package delta

import (
	"bufio"
	"bytes"
	"testing"
)

func TestVarintVectors(t *testing.T) {
	tests := []struct {
		value uint64
		want  []byte
	}{
		{0, []byte{0x00}},
		{127, []byte{0x7f}},
		{128, []byte{0x81, 0x00}},
		{130, []byte{0x81, 0x02}},
		{16383, []byte{0xff, 0x7f}},
		{16384, []byte{0x81, 0x80, 0x00}},
	}
	for _, test := range tests {
		encoded := appendVarint(nil, test.value)
		if !bytes.Equal(encoded, test.want) {
			t.Errorf("appendVarint(%d) = %x, want %x", test.value, encoded, test.want)
		}
		decoded, err := readVarint(bufio.NewReader(bytes.NewReader(encoded)))
		if err != nil || decoded != test.value {
			t.Errorf("readVarint(%x) = %d, %v", encoded, decoded, err)
		}
	}
}
