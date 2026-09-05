package svn

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"strings"
)

type ChecksumKind uint8

const (
	ChecksumMD5 ChecksumKind = iota
	ChecksumSHA1
	ChecksumFNV1a32
)

type Checksum struct {
	Kind   ChecksumKind
	Digest []byte
}

var (
	EmptyMD5     = Sum(ChecksumMD5, nil)
	EmptySHA1    = Sum(ChecksumSHA1, nil)
	EmptyFNV1a32 = Sum(ChecksumFNV1a32, nil)
)

func (kind ChecksumKind) String() string {
	switch kind {
	case ChecksumMD5:
		return "md5"
	case ChecksumSHA1:
		return "sha1"
	case ChecksumFNV1a32:
		return "fnv1"
	default:
		return fmt.Sprintf("ChecksumKind(%d)", kind)
	}
}

func Sum(kind ChecksumKind, data []byte) Checksum {
	var digest []byte
	switch kind {
	case ChecksumMD5:
		sum := md5.Sum(data)
		digest = sum[:]
	case ChecksumSHA1:
		sum := sha1.Sum(data)
		digest = sum[:]
	case ChecksumFNV1a32:
		hash := fnv.New32a()
		_, _ = hash.Write(data)
		digest = make([]byte, 4)
		binary.BigEndian.PutUint32(digest, hash.Sum32())
	default:
		return Checksum{Kind: kind}
	}
	return Checksum{Kind: kind, Digest: digest}
}

func ParseChecksum(value string) (Checksum, error) {
	prefixes := []struct {
		prefix string
		kind   ChecksumKind
	}{
		{"$md5 $", ChecksumMD5},
		{"$md5$", ChecksumMD5},
		{"$sha1$", ChecksumSHA1},
		{"$fnv1$", ChecksumFNV1a32},
	}
	for _, candidate := range prefixes {
		if strings.HasPrefix(value, candidate.prefix) {
			return ParseChecksumHex(candidate.kind, value[len(candidate.prefix):])
		}
	}
	switch len(value) {
	case md5.Size * 2:
		return ParseChecksumHex(ChecksumMD5, value)
	case sha1.Size * 2:
		return ParseChecksumHex(ChecksumSHA1, value)
	case 8:
		return ParseChecksumHex(ChecksumFNV1a32, value)
	default:
		return Checksum{}, fmt.Errorf("%w: invalid checksum prefix or length", ErrBadChecksumParse)
	}
}

func ParseChecksumHex(kind ChecksumKind, value string) (Checksum, error) {
	want := checksumSize(kind)
	if want == 0 {
		return Checksum{}, fmt.Errorf("%w: %s", ErrBadChecksumKind, kind)
	}
	if len(value) != want*2 {
		return Checksum{}, fmt.Errorf("%w: %s checksum has %d hex digits, want %d", ErrBadChecksumParse, kind, len(value), want*2)
	}
	digest, err := hex.DecodeString(value)
	if err != nil {
		return Checksum{}, fmt.Errorf("%w: %v", ErrBadChecksumParse, err)
	}
	return Checksum{Kind: kind, Digest: digest}, nil
}

func checksumSize(kind ChecksumKind) int {
	switch kind {
	case ChecksumMD5:
		return md5.Size
	case ChecksumSHA1:
		return sha1.Size
	case ChecksumFNV1a32:
		return 4
	default:
		return 0
	}
}

func (checksum Checksum) Hex() string { return hex.EncodeToString(checksum.Digest) }

func (checksum Checksum) String() string { return checksum.Hex() }

func (checksum Checksum) Serialize() string {
	prefix := "$" + checksum.Kind.String() + "$"
	if checksum.Kind == ChecksumMD5 {
		prefix = "$md5 $"
	}
	return prefix + checksum.Hex()
}

func (checksum Checksum) Equal(other Checksum) bool {
	return checksum.Kind == other.Kind && bytes.Equal(checksum.Digest, other.Digest)
}

func (checksum Checksum) IsEmpty() bool {
	return checksum.Equal(Sum(checksum.Kind, nil))
}
