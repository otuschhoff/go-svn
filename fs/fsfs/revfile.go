package fsfs

import (
	"bytes"
	"container/list"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/otuschhoff/go-svn/svn"
)

type revisionFile struct {
	path      string
	size      int64
	start     int64
	end       int64
	dataEnd   int64
	locations map[svn.Revnum]map[int64]int64
}

type logicalFooter struct {
	l2pOffset int64
	p2lOffset int64
	endOffset int64
	l2pDigest string
	p2lDigest string
}

type p2lEntry struct {
	offset   int64
	size     int64
	kind     uint64
	checksum uint32
	revision svn.Revnum
	item     int64
}

type logicalIndexValue struct {
	path      string
	locations map[svn.Revnum]map[int64]int64
	dataEnd   int64
}

type logicalIndexCache struct {
	mu       sync.Mutex
	capacity int
	list     *list.List
	entries  map[string]*list.Element
}

func newLogicalIndexCache(capacity int) *logicalIndexCache {
	return &logicalIndexCache{capacity: capacity, list: list.New(), entries: make(map[string]*list.Element)}
}

func (cache *logicalIndexCache) get(path string) (map[svn.Revnum]map[int64]int64, int64, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	element := cache.entries[path]
	if element == nil {
		return nil, 0, false
	}
	cache.list.MoveToFront(element)
	value := element.Value.(logicalIndexValue)
	return value.locations, value.dataEnd, true
}

func (cache *logicalIndexCache) put(path string, locations map[svn.Revnum]map[int64]int64, dataEnd int64) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element := cache.entries[path]; element != nil {
		element.Value = logicalIndexValue{path: path, locations: locations, dataEnd: dataEnd}
		cache.list.MoveToFront(element)
		return
	}
	element := cache.list.PushFront(logicalIndexValue{path: path, locations: locations, dataEnd: dataEnd})
	cache.entries[path] = element
	if cache.list.Len() > cache.capacity {
		oldest := cache.list.Back()
		cache.list.Remove(oldest)
		delete(cache.entries, oldest.Value.(logicalIndexValue).path)
	}
}

func (file *revisionFile) itemData(revision svn.Revnum, item int64) ([]byte, error) {
	offset, err := file.itemOffset(revision, item)
	if err != nil {
		return nil, err
	}
	end := file.dataEnd
	if end == 0 {
		end = file.end
	}
	for _, items := range file.locations {
		for _, candidate := range items {
			if candidate > offset && candidate < end {
				end = candidate
			}
		}
	}
	if offset < 0 || end < offset || end > file.size {
		return nil, fmt.Errorf("%w: invalid item range", svn.ErrFSCorrupt)
	}
	return file.readRange(offset, end)
}

func (file *revisionFile) readRange(start, end int64) ([]byte, error) {
	if start < 0 || end < start || end > file.size || end-start > int64(^uint(0)>>1) {
		return nil, fmt.Errorf("%w: invalid revision file range", svn.ErrFSCorrupt)
	}
	reader, err := os.Open(file.path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data := make([]byte, int(end-start))
	if _, err := reader.ReadAt(data, start); err != nil && err != io.EOF {
		return nil, err
	}
	return data, nil
}

func (file *revisionFile) recordData(offset int64) ([]byte, error) {
	const maxRecordSize = 16 * 1024 * 1024
	end := min(file.dataEnd, offset+maxRecordSize)
	if end == 0 {
		end = min(file.end, offset+maxRecordSize)
	}
	data, err := file.readRange(offset, end)
	if err != nil {
		return nil, err
	}
	if recordEnd := bytes.Index(data, []byte("\n\n")); recordEnd >= 0 {
		return data[:recordEnd+2], nil
	}
	return nil, fmt.Errorf("%w: node-revision record is too large or incomplete", svn.ErrFSCorrupt)
}

func (filesystem *FS) openRevision(revision svn.Revnum) (*revisionFile, error) {
	if revision < 0 {
		return nil, fmt.Errorf("%w: revision %d", svn.ErrFSNoSuchRevision, revision)
	}
	path, start, end, err := filesystem.revisionPath(revision)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: revision %d", svn.ErrFSNoSuchRevision, revision)
		}
		return nil, err
	}
	if end == 0 {
		end = info.Size()
	}
	file := &revisionFile{path: path, size: info.Size(), start: start, end: end}
	if filesystem.format.Addressing == AddressingLogical {
		if locations, dataEnd, found := filesystem.indexCache.get(path); found {
			file.locations, file.dataEnd = locations, dataEnd
		} else {
			file.locations, file.dataEnd, err = parseLogicalIndexesFile(path, info.Size())
			if err != nil {
				return nil, err
			}
			filesystem.indexCache.put(path, file.locations, file.dataEnd)
		}
	}
	return file, nil
}

func (filesystem *FS) revisionPath(revision svn.Revnum) (string, int64, int64, error) {
	base := filepath.Join(filesystem.path, "db", "revs")
	if filesystem.format.Layout == LayoutLinear {
		return filepath.Join(base, strconv.FormatInt(int64(revision), 10)), 0, 0, nil
	}
	shard := int64(revision) / filesystem.format.ShardSize
	if revision >= filesystem.minUnpackedRev {
		return filepath.Join(base, strconv.FormatInt(shard, 10), strconv.FormatInt(int64(revision), 10)), 0, 0, nil
	}
	pack := filepath.Join(base, strconv.FormatInt(shard, 10)+".pack", "pack")
	if filesystem.format.Addressing == AddressingLogical {
		return pack, 0, 0, nil
	}
	manifest, err := os.ReadFile(filepath.Join(base, strconv.FormatInt(shard, 10)+".pack", "manifest"))
	if err != nil {
		return "", 0, 0, err
	}
	lines := strings.Fields(string(manifest))
	index := int64(revision) % filesystem.format.ShardSize
	if index < 0 || index >= int64(len(lines)) {
		return "", 0, 0, fmt.Errorf("%w: revision %d missing from pack manifest", svn.ErrFSCorrupt, revision)
	}
	start, err := strconv.ParseInt(lines[index], 10, 64)
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: invalid pack manifest", svn.ErrFSCorrupt)
	}
	var end int64
	if index+1 < int64(len(lines)) {
		end, err = strconv.ParseInt(lines[index+1], 10, 64)
		if err != nil {
			return "", 0, 0, fmt.Errorf("%w: invalid pack manifest", svn.ErrFSCorrupt)
		}
	}
	return pack, start, end, nil
}

func (file *revisionFile) itemOffset(revision svn.Revnum, item int64) (int64, error) {
	if file.locations != nil {
		items := file.locations[revision]
		offset, ok := items[item]
		if !ok || offset < 0 {
			return 0, fmt.Errorf("%w: item %d in revision %d", svn.ErrFSNoSuchRevision, item, revision)
		}
		return offset, nil
	}
	return file.start + item, nil
}

func (file *revisionFile) physicalTrailer() (int64, int64, error) {
	data, err := file.readRange(max(file.start, file.end-4096), file.end)
	if err != nil {
		return 0, 0, err
	}
	data = bytes.TrimSuffix(data, []byte("\n"))
	lineStart := bytes.LastIndexByte(data, '\n') + 1
	fields := strings.Fields(string(data[lineStart:]))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("%w: invalid physical revision trailer", svn.ErrFSCorrupt)
	}
	root, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: invalid root offset", svn.ErrFSCorrupt)
	}
	changes, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: invalid changes offset", svn.ErrFSCorrupt)
	}
	return file.start + root, file.start + changes, nil
}

func parseLogicalIndexesFile(path string, size int64) (map[svn.Revnum]map[int64]int64, int64, error) {
	reader, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer reader.Close()
	if size < 2 {
		return nil, 0, fmt.Errorf("%w: empty logical revision file", svn.ErrFSCorrupt)
	}
	last := []byte{0}
	if _, err := reader.ReadAt(last, size-1); err != nil {
		return nil, 0, err
	}
	footerLength := int64(last[0])
	if footerLength == 0 || footerLength+1 > size {
		return nil, 0, fmt.Errorf("%w: invalid logical revision footer", svn.ErrFSCorrupt)
	}
	footerData := make([]byte, footerLength+1)
	if _, err := reader.ReadAt(footerData, size-footerLength-1); err != nil {
		return nil, 0, err
	}
	footer, err := parseLogicalFooterAt(footerData, size-footerLength-1)
	if err != nil {
		return nil, 0, err
	}
	indexData := make([]byte, footer.endOffset-footer.l2pOffset)
	if _, err := reader.ReadAt(indexData, footer.l2pOffset); err != nil {
		return nil, 0, err
	}
	l2pEnd := footer.p2lOffset - footer.l2pOffset
	l2pData, p2lData := indexData[:l2pEnd], indexData[l2pEnd:]
	if err := checkMD5(l2pData, footer.l2pDigest, "L2P"); err != nil {
		return nil, 0, err
	}
	if err := checkMD5(p2lData, footer.p2lDigest, "P2L"); err != nil {
		return nil, 0, err
	}
	locations, err := parseL2P(l2pData)
	if err != nil {
		return nil, 0, err
	}
	entries, err := parseP2L(p2lData, footer.l2pOffset)
	if err != nil {
		return nil, 0, err
	}
	if err := validateLogicalIndexesReader(reader, footer.l2pOffset, locations, entries); err != nil {
		return nil, 0, err
	}
	return locations, footer.l2pOffset, nil
}

func validateLogicalIndexesReader(reader io.ReaderAt, dataSize int64, locations map[svn.Revnum]map[int64]int64, entries []p2lEntry) error {
	var end int64
	for _, entry := range entries {
		if entry.offset != end {
			return fmt.Errorf("%w: P2L entries do not cover the revision data", svn.ErrFSCorrupt)
		}
		if entry.size < 0 || entry.offset > int64(^uint64(0)>>1)-entry.size {
			return fmt.Errorf("%w: invalid P2L item range", svn.ErrFSCorrupt)
		}
		end = entry.offset + entry.size
		if entry.kind == 0 {
			valid := entry.item == 0 && entry.checksum == 0
			if valid && entry.offset < dataSize && end > dataSize {
				var err error
				valid, err = readerRangeAllZero(reader, entry.offset, dataSize-entry.offset)
				if err != nil {
					return err
				}
			}
			if !valid {
				return fmt.Errorf("%w: invalid unused P2L entry", svn.ErrFSCorrupt)
			}
			continue
		}
		if end > dataSize {
			return fmt.Errorf("%w: P2L item extends beyond revision data", svn.ErrFSCorrupt)
		}
		if locations[entry.revision][entry.item] != entry.offset {
			return fmt.Errorf("%w: L2P and P2L indexes disagree", svn.ErrFSCorrupt)
		}
		checksum, err := fnv1a32x4Reader(reader, entry.offset, entry.size)
		if err != nil {
			return err
		}
		if checksum != entry.checksum {
			return fmt.Errorf("%w: P2L item checksum mismatch", svn.ErrFSCorrupt)
		}
	}
	if end < dataSize || len(entries) == 0 || entries[len(entries)-1].kind != 0 {
		return fmt.Errorf("%w: P2L index does not cover the final page", svn.ErrFSCorrupt)
	}
	return nil
}

func readerRangeAllZero(reader io.ReaderAt, offset, size int64) (bool, error) {
	buffer := make([]byte, 64*1024)
	for size > 0 {
		length := int(min(size, int64(len(buffer))))
		if _, err := reader.ReadAt(buffer[:length], offset); err != nil && err != io.EOF {
			return false, err
		}
		if !allZero(buffer[:length]) {
			return false, nil
		}
		offset += int64(length)
		size -= int64(length)
	}
	return true, nil
}

func fnv1a32x4Reader(reader io.ReaderAt, offset, size int64) (uint32, error) {
	const (
		basis = uint32(2166136261)
		prime = uint32(0x01000193)
	)
	hashes := [4]uint32{basis, basis, basis, basis}
	complete := size &^ 3
	buffer := make([]byte, 64*1024)
	for read := int64(0); read < complete; {
		length := int(min(complete-read, int64(len(buffer))))
		length &^= 3
		if _, err := reader.ReadAt(buffer[:length], offset+read); err != nil && err != io.EOF {
			return 0, err
		}
		for index := 0; index < length; index += 4 {
			for lane := range hashes {
				hashes[lane] = (hashes[lane] ^ uint32(buffer[index+lane])) * prime
			}
		}
		read += int64(length)
	}
	combined := make([]byte, 16, 19)
	for index, hash := range hashes {
		binary.BigEndian.PutUint32(combined[index*4:], hash)
	}
	if remainder := size - complete; remainder != 0 {
		trailing := make([]byte, remainder)
		if _, err := reader.ReadAt(trailing, offset+complete); err != nil && err != io.EOF {
			return 0, err
		}
		combined = append(combined, trailing...)
	}
	result := basis
	for _, value := range combined {
		result = (result ^ uint32(value)) * prime
	}
	return result, nil
}

func parseLogicalFooterAt(data []byte, base int64) (logicalFooter, error) {
	fields := strings.Fields(string(data[:len(data)-1]))
	if len(fields) != 4 || len(fields[1]) != md5.Size*2 || len(fields[3]) != md5.Size*2 {
		return logicalFooter{}, fmt.Errorf("%w: invalid logical revision footer", svn.ErrFSCorrupt)
	}
	l2pOffset, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || l2pOffset < 0 || l2pOffset >= base {
		return logicalFooter{}, fmt.Errorf("%w: invalid L2P offset", svn.ErrFSCorrupt)
	}
	p2lOffset, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || p2lOffset <= l2pOffset || p2lOffset >= base {
		return logicalFooter{}, fmt.Errorf("%w: invalid P2L offset", svn.ErrFSCorrupt)
	}
	if _, err := hex.DecodeString(fields[1]); err != nil {
		return logicalFooter{}, fmt.Errorf("%w: invalid L2P checksum", svn.ErrFSCorrupt)
	}
	if _, err := hex.DecodeString(fields[3]); err != nil {
		return logicalFooter{}, fmt.Errorf("%w: invalid P2L checksum", svn.ErrFSCorrupt)
	}
	return logicalFooter{l2pOffset: l2pOffset, p2lOffset: p2lOffset, endOffset: base, l2pDigest: fields[1], p2lDigest: fields[3]}, nil
}

func parseLogicalIndexes(data []byte) (map[svn.Revnum]map[int64]int64, int64, error) {
	footer, err := parseLogicalFooter(data)
	if err != nil {
		return nil, 0, err
	}
	l2pData := data[footer.l2pOffset:footer.p2lOffset]
	p2lData := data[footer.p2lOffset:footer.endOffset]
	if err := checkMD5(l2pData, footer.l2pDigest, "L2P"); err != nil {
		return nil, 0, err
	}
	if err := checkMD5(p2lData, footer.p2lDigest, "P2L"); err != nil {
		return nil, 0, err
	}
	locations, err := parseL2P(l2pData)
	if err != nil {
		return nil, 0, err
	}
	entries, err := parseP2L(p2lData, footer.l2pOffset)
	if err != nil {
		return nil, 0, err
	}
	if err := validateLogicalIndexes(data[:footer.l2pOffset], locations, entries); err != nil {
		return nil, 0, err
	}
	return locations, footer.l2pOffset, nil
}

func parseLogicalFooter(data []byte) (logicalFooter, error) {
	if len(data) == 0 {
		return logicalFooter{}, fmt.Errorf("%w: empty logical revision file", svn.ErrFSCorrupt)
	}
	footerLength := int(data[len(data)-1])
	if footerLength == 0 || footerLength+1 > len(data) {
		return logicalFooter{}, fmt.Errorf("%w: invalid logical revision footer", svn.ErrFSCorrupt)
	}
	endOffset := int64(len(data) - footerLength - 1)
	fields := strings.Fields(string(data[endOffset : len(data)-1]))
	if len(fields) != 4 || len(fields[1]) != md5.Size*2 || len(fields[3]) != md5.Size*2 {
		return logicalFooter{}, fmt.Errorf("%w: invalid logical revision footer", svn.ErrFSCorrupt)
	}
	l2pOffset, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || l2pOffset < 0 || l2pOffset >= endOffset {
		return logicalFooter{}, fmt.Errorf("%w: invalid L2P offset", svn.ErrFSCorrupt)
	}
	p2lOffset, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || p2lOffset <= l2pOffset || p2lOffset >= endOffset {
		return logicalFooter{}, fmt.Errorf("%w: invalid P2L offset", svn.ErrFSCorrupt)
	}
	if _, err := hex.DecodeString(fields[1]); err != nil {
		return logicalFooter{}, fmt.Errorf("%w: invalid L2P checksum", svn.ErrFSCorrupt)
	}
	if _, err := hex.DecodeString(fields[3]); err != nil {
		return logicalFooter{}, fmt.Errorf("%w: invalid P2L checksum", svn.ErrFSCorrupt)
	}
	return logicalFooter{l2pOffset: l2pOffset, p2lOffset: p2lOffset, endOffset: endOffset, l2pDigest: fields[1], p2lDigest: fields[3]}, nil
}

func checkMD5(data []byte, want, name string) error {
	digest := md5.Sum(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), want) {
		return fmt.Errorf("%w: %s checksum mismatch", svn.ErrFSCorrupt, name)
	}
	return nil
}

func parseL2P(index []byte) (map[svn.Revnum]map[int64]int64, error) {
	if !bytes.HasPrefix(index, []byte("L2P-INDEX\n")) {
		return nil, fmt.Errorf("%w: missing L2P index marker", svn.ErrFSCorrupt)
	}
	reader := varintReader{data: index[len("L2P-INDEX\n"):]}
	firstRevision, err := reader.unsigned()
	if err != nil {
		return nil, err
	}
	pageSize, err := reader.unsigned()
	if err != nil || !powerOfTwoUnsigned(pageSize) {
		return nil, fmt.Errorf("%w: invalid L2P page size", svn.ErrFSCorrupt)
	}
	revisionCount, err := reader.unsigned()
	if err != nil || revisionCount == 0 || revisionCount > uint64(len(reader.data)-reader.index) {
		return nil, fmt.Errorf("%w: invalid L2P revision count", svn.ErrFSCorrupt)
	}
	pageCount, err := reader.unsigned()
	if err != nil || pageCount < revisionCount || pageCount > uint64(len(reader.data)-reader.index)/2 {
		return nil, fmt.Errorf("%w: invalid L2P page count", svn.ErrFSCorrupt)
	}
	revisionPages := make([]uint64, revisionCount)
	for index := range revisionPages {
		revisionPages[index], err = reader.unsigned()
		if err != nil || revisionPages[index] == 0 {
			return nil, fmt.Errorf("%w: invalid L2P revision page count", svn.ErrFSCorrupt)
		}
	}
	type pageHeader struct{ size, entries uint64 }
	pages := make([]pageHeader, pageCount)
	for index := range pages {
		pages[index].size, err = reader.unsigned()
		if err == nil {
			pages[index].entries, err = reader.unsigned()
		}
		if err != nil || pages[index].size == 0 || pages[index].entries == 0 || pages[index].entries > pageSize {
			return nil, fmt.Errorf("%w: invalid L2P page header", svn.ErrFSCorrupt)
		}
	}
	pageDataStart := reader.index
	result := make(map[svn.Revnum]map[int64]int64)
	pageIndex := uint64(0)
	for revisionIndex, count := range revisionPages {
		items := make(map[int64]int64)
		var itemIndex int64
		for range count {
			if pageIndex >= uint64(len(pages)) {
				return nil, fmt.Errorf("%w: incomplete L2P page table", svn.ErrFSCorrupt)
			}
			page := pages[pageIndex]
			if page.size > uint64(len(reader.data)-pageDataStart) {
				return nil, fmt.Errorf("%w: truncated L2P page", svn.ErrFSCorrupt)
			}
			pageReader := varintReader{data: reader.data[pageDataStart : pageDataStart+int(page.size)]}
			var offset int64
			for range page.entries {
				delta, readErr := pageReader.signed()
				if readErr != nil {
					return nil, readErr
				}
				if delta > 0 && offset > int64(^uint64(0)>>1)-delta || delta < 0 && offset < -int64(^uint64(0)>>1)-1-delta {
					return nil, fmt.Errorf("%w: overflowing L2P offset", svn.ErrFSCorrupt)
				}
				offset += delta
				if offset < 0 {
					return nil, fmt.Errorf("%w: invalid L2P offset", svn.ErrFSCorrupt)
				}
				items[itemIndex] = offset - 1
				itemIndex++
			}
			if pageReader.index != len(pageReader.data) {
				return nil, fmt.Errorf("%w: L2P page size mismatch", svn.ErrFSCorrupt)
			}
			pageDataStart += int(page.size)
			pageIndex++
		}
		if firstRevision > uint64(^uint64(0)>>1)-uint64(revisionIndex) {
			return nil, fmt.Errorf("%w: overflowing L2P revision", svn.ErrFSCorrupt)
		}
		result[svn.Revnum(firstRevision+uint64(revisionIndex))] = items
	}
	if pageIndex != pageCount || pageDataStart != len(reader.data) {
		return nil, fmt.Errorf("%w: incomplete L2P index", svn.ErrFSCorrupt)
	}
	return result, nil
}

func parseP2L(index []byte, fileSize int64) ([]p2lEntry, error) {
	if !bytes.HasPrefix(index, []byte("P2L-INDEX\n")) {
		return nil, fmt.Errorf("%w: missing P2L index marker", svn.ErrFSCorrupt)
	}
	reader := varintReader{data: index[len("P2L-INDEX\n"):]}
	firstRevision, err := reader.unsigned()
	if err != nil || firstRevision > uint64(^uint64(0)>>1) {
		return nil, fmt.Errorf("%w: invalid P2L first revision", svn.ErrFSCorrupt)
	}
	declaredSize, err := reader.unsigned()
	if err != nil || declaredSize != uint64(fileSize) {
		return nil, fmt.Errorf("%w: P2L file size mismatch", svn.ErrFSCorrupt)
	}
	pageSize, err := reader.unsigned()
	if err != nil || !powerOfTwoUnsigned(pageSize) {
		return nil, fmt.Errorf("%w: invalid P2L page size", svn.ErrFSCorrupt)
	}
	pageCount, err := reader.unsigned()
	wantPages := (declaredSize-1)/pageSize + 1
	if err != nil || declaredSize == 0 || pageCount != wantPages || pageCount > uint64(len(reader.data)-reader.index) {
		return nil, fmt.Errorf("%w: invalid P2L page count", svn.ErrFSCorrupt)
	}
	pageSizes := make([]uint64, pageCount)
	for page := range pageSizes {
		pageSizes[page], err = reader.unsigned()
		if err != nil {
			return nil, err
		}
	}
	if pageCount > ^uint64(0)/pageSize {
		return nil, fmt.Errorf("%w: overflowing P2L index size", svn.ErrFSCorrupt)
	}
	pageDataStart := reader.index
	indexedSize := pageCount * pageSize
	byOffset := make(map[int64]p2lEntry)
	for _, encodedSize := range pageSizes {
		if encodedSize > uint64(len(reader.data)-pageDataStart) {
			return nil, fmt.Errorf("%w: truncated P2L page", svn.ErrFSCorrupt)
		}
		if encodedSize == 0 {
			continue
		}
		pageReader := varintReader{data: reader.data[pageDataStart : pageDataStart+int(encodedSize)]}
		offsetValue, readErr := pageReader.unsigned()
		if readErr != nil || offsetValue > uint64(fileSize) {
			return nil, fmt.Errorf("%w: invalid P2L item offset", svn.ErrFSCorrupt)
		}
		offset := int64(offsetValue)
		lastRevision := int64(firstRevision)
		var lastCompound int64
		for pageReader.index < len(pageReader.data) {
			size, readErr := pageReader.unsigned()
			if readErr != nil || size == 0 || offset < 0 || uint64(offset) > indexedSize || size > indexedSize-uint64(offset) {
				return nil, fmt.Errorf("%w: invalid P2L item size", svn.ErrFSCorrupt)
			}
			compoundDelta, readErr := pageReader.signed()
			if readErr != nil {
				return nil, readErr
			}
			if compoundDelta > 0 && lastCompound > int64(^uint64(0)>>1)-compoundDelta || compoundDelta < 0 && lastCompound < -int64(^uint64(0)>>1)-1-compoundDelta {
				return nil, fmt.Errorf("%w: overflowing P2L item", svn.ErrFSCorrupt)
			}
			lastCompound += compoundDelta
			if lastCompound < 0 || lastCompound&7 > 6 {
				return nil, fmt.Errorf("%w: invalid P2L item type", svn.ErrFSCorrupt)
			}
			revisionDelta, readErr := pageReader.signed()
			if readErr != nil {
				return nil, readErr
			}
			if revisionDelta > 0 && lastRevision > int64(^uint64(0)>>1)-revisionDelta || revisionDelta < 0 && lastRevision < -int64(^uint64(0)>>1)-1-revisionDelta {
				return nil, fmt.Errorf("%w: overflowing P2L revision", svn.ErrFSCorrupt)
			}
			lastRevision += revisionDelta
			checksum, readErr := pageReader.unsigned()
			if readErr != nil || checksum > 1<<32-1 || lastRevision < 0 {
				return nil, fmt.Errorf("%w: invalid P2L entry", svn.ErrFSCorrupt)
			}
			if size > uint64(^uint64(0)>>1) || offset > int64(^uint64(0)>>1)-int64(size) {
				return nil, fmt.Errorf("%w: overflowing P2L item range", svn.ErrFSCorrupt)
			}
			entry := p2lEntry{offset: offset, size: int64(size), kind: uint64(lastCompound & 7), checksum: uint32(checksum), revision: svn.Revnum(lastRevision), item: lastCompound / 8}
			if previous, exists := byOffset[offset]; exists && previous != entry {
				return nil, fmt.Errorf("%w: inconsistent duplicate P2L entry", svn.ErrFSCorrupt)
			}
			byOffset[offset] = entry
			offset += int64(size)
		}
		pageDataStart += int(encodedSize)
	}
	if pageDataStart != len(reader.data) {
		return nil, fmt.Errorf("%w: P2L page size mismatch", svn.ErrFSCorrupt)
	}
	result := make([]p2lEntry, 0, len(byOffset))
	for _, entry := range byOffset {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].offset < result[j].offset })
	return result, nil
}

func validateLogicalIndexes(data []byte, locations map[svn.Revnum]map[int64]int64, entries []p2lEntry) error {
	var end int64
	for _, entry := range entries {
		if entry.offset != end {
			return fmt.Errorf("%w: P2L entries do not cover the revision data", svn.ErrFSCorrupt)
		}
		end = entry.offset + entry.size
		if entry.kind == 0 {
			if entry.item != 0 || entry.checksum != 0 || entry.offset < int64(len(data)) && end > int64(len(data)) && !allZero(data[entry.offset:]) {
				return fmt.Errorf("%w: invalid unused P2L entry", svn.ErrFSCorrupt)
			}
			continue
		}
		if end > int64(len(data)) {
			return fmt.Errorf("%w: P2L item extends beyond revision data", svn.ErrFSCorrupt)
		}
		if locations[entry.revision][entry.item] != entry.offset {
			return fmt.Errorf("%w: L2P and P2L indexes disagree", svn.ErrFSCorrupt)
		}
		if fnv1a32x4(data[entry.offset:end]) != entry.checksum {
			return fmt.Errorf("%w: P2L item checksum mismatch", svn.ErrFSCorrupt)
		}
	}
	if end < int64(len(data)) || len(entries) == 0 || entries[len(entries)-1].kind != 0 {
		return fmt.Errorf("%w: P2L index does not cover the final page", svn.ErrFSCorrupt)
	}
	return nil
}

func allZero(data []byte) bool {
	for _, value := range data {
		if value != 0 {
			return false
		}
	}
	return true
}

func fnv1a32x4(data []byte) uint32 {
	const (
		basis = uint32(2166136261)
		prime = uint32(0x01000193)
	)
	hashes := [4]uint32{basis, basis, basis, basis}
	complete := len(data) &^ 3
	for index := 0; index < complete; index += 4 {
		for lane := range hashes {
			hashes[lane] = (hashes[lane] ^ uint32(data[index+lane])) * prime
		}
	}
	combined := make([]byte, 16, 19)
	for index, hash := range hashes {
		binary.BigEndian.PutUint32(combined[index*4:], hash)
	}
	combined = append(combined, data[complete:]...)
	result := basis
	for _, value := range combined {
		result = (result ^ uint32(value)) * prime
	}
	return result
}

func powerOfTwoUnsigned(value uint64) bool { return value != 0 && value&(value-1) == 0 }

type varintReader struct {
	data  []byte
	index int
}

func (reader *varintReader) unsigned() (uint64, error) {
	var value uint64
	for shift := uint(0); shift < 64; shift += 7 {
		if reader.index >= len(reader.data) {
			return 0, fmt.Errorf("%w: truncated index integer", svn.ErrFSCorrupt)
		}
		current := reader.data[reader.index]
		reader.index++
		value |= uint64(current&0x7f) << shift
		if current&0x80 == 0 {
			return value, nil
		}
	}
	return 0, fmt.Errorf("%w: overflowing index integer", svn.ErrFSCorrupt)
}

func (reader *varintReader) signed() (int64, error) {
	value, err := reader.unsigned()
	if err != nil {
		return 0, err
	}
	if value&1 == 0 {
		return int64(value >> 1), nil
	}
	return -int64(value>>1) - 1, nil
}
