package svn

import (
	"errors"
	"testing"
)

func TestChecksumVectors(t *testing.T) {
	tests := []struct {
		kind ChecksumKind
		data string
		want string
	}{
		{ChecksumMD5, "", "d41d8cd98f00b204e9800998ecf8427e"},
		{ChecksumSHA1, "", "da39a3ee5e6b4b0d3255bfef95601890afd80709"},
		{ChecksumFNV1a32, "", "811c9dc5"},
		{ChecksumMD5, "abcde", "ab56b4d92b40713acc5af89985d4b786"},
		{ChecksumSHA1, "abcde", "03de6c570bfe24bfc328ccd7ca46b76eadaf4334"},
		{ChecksumFNV1a32, "abcde", "749bcf08"},
	}
	for _, test := range tests {
		if got := Sum(test.kind, []byte(test.data)).Hex(); got != test.want {
			t.Errorf("Sum(%s, %q) = %q, want %q", test.kind, test.data, got, test.want)
		}
	}
}

func TestChecksumParseAndSerialize(t *testing.T) {
	for _, value := range []string{
		"$md5 $ab56b4d92b40713acc5af89985d4b786",
		"$sha1$03de6c570bfe24bfc328ccd7ca46b76eadaf4334",
		"$fnv1$749bcf08",
	} {
		checksum, err := ParseChecksum(value)
		if err != nil {
			t.Fatal(err)
		}
		if checksum.Serialize() != value {
			t.Errorf("round trip = %q, want %q", checksum.Serialize(), value)
		}
	}
}

func TestChecksumRejectsMalformedInput(t *testing.T) {
	for _, value := range []string{"", "$sha1$abc", "$what$deadbeef", "gggggggg"} {
		if _, err := ParseChecksum(value); !errors.Is(err, ErrBadChecksumParse) {
			t.Errorf("ParseChecksum(%q) error = %v", value, err)
		}
	}
}
