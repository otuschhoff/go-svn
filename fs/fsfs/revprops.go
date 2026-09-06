package fsfs

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/hashfile"
)

func (filesystem *FS) RevisionProps(ctx context.Context, revision svn.Revnum) (svn.Props, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	youngest, err := filesystem.YoungestRevision(ctx)
	if err != nil {
		return nil, err
	}
	if revision < 0 || revision > youngest {
		return nil, fmt.Errorf("%w: revision %d", svn.ErrFSNoSuchRevision, revision)
	}
	data, err := filesystem.revisionPropData(revision)
	if err != nil {
		return nil, err
	}
	properties, err := hashfile.Read(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: revision %d properties: %v", svn.ErrFSCorrupt, revision, err)
	}
	return properties, nil
}

func (filesystem *FS) revisionPropData(revision svn.Revnum) ([]byte, error) {
	base := filepath.Join(filesystem.path, "db", "revprops")
	if filesystem.format.Layout == LayoutLinear {
		return readRevpropFile(filepath.Join(base, strconv.FormatInt(int64(revision), 10)))
	}
	shard := int64(revision) / filesystem.format.ShardSize
	minimum := int64(0)
	if filesystem.format.Number >= 6 {
		var err error
		minimum, err = readNumber(filepath.Join(filesystem.path, "db", "min-unpacked-rev"))
		if err != nil {
			return nil, fmt.Errorf("%w: read revprop packing threshold: %v", svn.ErrFSCorrupt, err)
		}
	}
	if revision == 0 || int64(revision) >= minimum {
		path := filepath.Join(base, strconv.FormatInt(shard, 10), strconv.FormatInt(int64(revision), 10))
		return readRevpropFile(path)
	}
	packDirectory := filepath.Join(base, strconv.FormatInt(shard, 10)+".pack")
	manifest, err := os.ReadFile(filepath.Join(packDirectory, "manifest"))
	if err != nil {
		return nil, fmt.Errorf("%w: read packed revprop manifest: %v", svn.ErrFSCorruptRevpropManifest, err)
	}
	files := strings.Fields(string(manifest))
	firstPacked := shard * filesystem.format.ShardSize
	if firstPacked == 0 {
		firstPacked = 1
	}
	index := int64(revision) - firstPacked
	if index < 0 || index >= int64(len(files)) {
		return nil, fmt.Errorf("%w: revision %d is missing", svn.ErrFSCorruptRevpropManifest, revision)
	}
	packed, err := os.ReadFile(filepath.Join(packDirectory, files[index]))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrFSPackedRevpropReadFailure, err)
	}
	container, err := decodePackedString(packed)
	if err != nil {
		return nil, err
	}
	return revpropFromContainer(container, revision)
}

func readRevpropFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrFSNoSuchRevision, err)
	}
	return data, nil
}

func decodePackedString(data []byte) ([]byte, error) {
	reader := bytes.NewReader(data)
	length, err := readPackedUint(reader)
	if err != nil || length > uint64(^uint(0)>>1) || length > uint64(len(data))*1024 {
		return nil, fmt.Errorf("%w: invalid packed revprop length", svn.ErrFSPackedRevpropReadFailure)
	}
	payload := data[len(data)-reader.Len():]
	if uint64(len(payload)) == length {
		return payload, nil
	}
	compressed, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid compressed packed revprops: %v", svn.ErrFSPackedRevpropReadFailure, err)
	}
	defer compressed.Close()
	result, err := io.ReadAll(io.LimitReader(compressed, int64(length)+1))
	if err != nil || uint64(len(result)) != length {
		return nil, fmt.Errorf("%w: packed revprop size mismatch", svn.ErrFSPackedRevpropReadFailure)
	}
	return result, nil
}

func readPackedUint(reader *bytes.Reader) (uint64, error) {
	var value uint64
	for count := 0; count < 10; count++ {
		current, err := reader.ReadByte()
		if err != nil || value > ^uint64(0)>>7 {
			return 0, fmt.Errorf("invalid packed integer")
		}
		value = value<<7 | uint64(current&0x7f)
		if current&0x80 == 0 {
			return value, nil
		}
	}
	return 0, fmt.Errorf("packed integer overflow")
}

func revpropFromContainer(data []byte, revision svn.Revnum) ([]byte, error) {
	reader := bufio.NewReader(bytes.NewReader(data))
	start, err := readContainerNumber(reader)
	if err != nil {
		return nil, err
	}
	count, err := readContainerNumber(reader)
	if err != nil || count <= 0 || revision < svn.Revnum(start) || revision >= svn.Revnum(start+count) {
		return nil, fmt.Errorf("%w: invalid packed revprop revision range", svn.ErrFSPackedRevpropReadFailure)
	}
	if count > int64(len(data)) {
		return nil, fmt.Errorf("%w: invalid packed revprop count", svn.ErrFSPackedRevpropReadFailure)
	}
	sizes := make([]int64, count)
	var total int64
	for index := range sizes {
		sizes[index], err = readContainerNumber(reader)
		if err != nil || sizes[index] < 0 || total > int64(len(data))-sizes[index] {
			return nil, fmt.Errorf("%w: invalid packed revprop size", svn.ErrFSPackedRevpropReadFailure)
		}
		total += sizes[index]
	}
	separator, err := reader.ReadByte()
	if err != nil || separator != '\n' {
		return nil, fmt.Errorf("%w: missing packed revprop header separator", svn.ErrFSPackedRevpropReadFailure)
	}
	for current, size := range sizes {
		if int64(current)+start == int64(revision) {
			result := make([]byte, size)
			if _, err := io.ReadFull(reader, result); err != nil {
				return nil, fmt.Errorf("%w: truncated packed revprops", svn.ErrFSPackedRevpropReadFailure)
			}
			return result, nil
		}
		if _, err := io.CopyN(io.Discard, reader, size); err != nil {
			return nil, fmt.Errorf("%w: truncated packed revprops", svn.ErrFSPackedRevpropReadFailure)
		}
	}
	return nil, fmt.Errorf("%w: revision %d is missing", svn.ErrFSPackedRevpropReadFailure, revision)
}

func readContainerNumber(reader *bufio.Reader) (int64, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return 0, fmt.Errorf("%w: truncated packed revprop header", svn.ErrFSPackedRevpropReadFailure)
	}
	value, err := strconv.ParseInt(strings.TrimSuffix(line, "\n"), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid packed revprop number", svn.ErrFSPackedRevpropReadFailure)
	}
	return value, nil
}
