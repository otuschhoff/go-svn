package fsfs

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	fsapi "github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/hashfile"
)

func (root *Root) CheckPath(ctx context.Context, nodePath string) (svn.NodeKind, error) {
	node, found, err := root.findNode(ctx, nodePath)
	if err != nil {
		return svn.NodeUnknown, err
	}
	if !found {
		return svn.NodeNone, nil
	}
	return node.Kind, nil
}

func (root *Root) NodeProps(ctx context.Context, nodePath string) (svn.Props, error) {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	if node.Properties == nil {
		return make(svn.Props), nil
	}
	var data bytes.Buffer
	if err := root.filesystem.writeRepresentation(ctx, node.Properties, &data); err != nil {
		return nil, err
	}
	return hashfile.Read(&data)
}

func (root *Root) DirEntries(ctx context.Context, nodePath string) ([]fsapi.DirEntry, error) {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	if node.Kind != svn.NodeDir {
		return nil, fmt.Errorf("%w: %s is not a directory", svn.ErrFSNotDirectory, nodePath)
	}
	entries, err := root.directoryEntries(ctx, node)
	if err != nil {
		return nil, err
	}
	result := make([]fsapi.DirEntry, 0, len(entries))
	for name, entry := range entries {
		result = append(result, fsapi.DirEntry{Name: name, Kind: entry.kind})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result, nil
}

func (root *Root) FileContents(ctx context.Context, nodePath string, writer io.Writer) error {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return err
	}
	if node.Kind != svn.NodeFile {
		return fmt.Errorf("%w: %s is not a file", svn.ErrFSNotFile, nodePath)
	}
	return root.filesystem.writeRepresentation(ctx, node.Text, writer)
}

func (root *Root) FileLength(ctx context.Context, nodePath string) (int64, error) {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return 0, err
	}
	if node.Kind != svn.NodeFile {
		return 0, fmt.Errorf("%w: %s is not a file", svn.ErrFSNotFile, nodePath)
	}
	if node.Text == nil {
		return 0, nil
	}
	return node.Text.ExpandedSize, nil
}

func (root *Root) FileChecksum(ctx context.Context, nodePath string, kind svn.ChecksumKind) (*svn.Checksum, error) {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	if node.Kind != svn.NodeFile {
		return nil, fmt.Errorf("%w: %s is not a file", svn.ErrFSNotFile, nodePath)
	}
	if node.Text == nil {
		checksum := svn.Sum(kind, nil)
		return &checksum, nil
	}
	value := node.Text.MD5
	if kind == svn.ChecksumSHA1 {
		value = node.Text.SHA1
	}
	if value != "" {
		checksum, err := svn.ParseChecksumHex(kind, value)
		return &checksum, err
	}
	digest, err := checksumHash(kind)
	if err != nil {
		return nil, err
	}
	if err := root.filesystem.writeRepresentation(ctx, node.Text, digest); err != nil {
		return nil, err
	}
	valueBytes := digest.Sum(nil)
	if kind == svn.ChecksumFNV1a32 {
		valueBytes = make([]byte, 4)
		binary.BigEndian.PutUint32(valueBytes, digest.(hash.Hash32).Sum32())
	}
	checksum := svn.Checksum{Kind: kind, Digest: valueBytes}
	return &checksum, nil
}

func checksumHash(kind svn.ChecksumKind) (hash.Hash, error) {
	switch kind {
	case svn.ChecksumMD5:
		return md5.New(), nil
	case svn.ChecksumSHA1:
		return sha1.New(), nil
	case svn.ChecksumFNV1a32:
		return fnv.New32a(), nil
	default:
		return nil, fmt.Errorf("%w: %s", svn.ErrBadChecksumKind, kind)
	}
}

func (root *Root) PathsChanged(ctx context.Context) (map[string]fsapi.PathChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := root.filesystem.openRevision(root.revision)
	if err != nil {
		return nil, err
	}
	var data []byte
	if root.filesystem.format.Addressing == AddressingLogical {
		data, err = file.itemData(root.revision, 1)
	} else {
		_, offset, trailerErr := file.physicalTrailer()
		if trailerErr != nil {
			return nil, trailerErr
		}
		remaining, readErr := file.readRange(offset, file.end)
		if readErr != nil {
			return nil, readErr
		}
		trimmed := bytes.TrimSuffix(remaining, []byte("\n"))
		end := bytes.LastIndexByte(trimmed, '\n')
		if end < 0 {
			return nil, fmt.Errorf("%w: invalid changed paths range", svn.ErrFSCorrupt)
		}
		data = trimmed[:end]
	}
	if err != nil {
		return nil, err
	}
	return parseChanges(data)
}

func (root *Root) NodeCreatedRevision(ctx context.Context, nodePath string) (svn.Revnum, error) {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	return node.ID.Revision, nil
}

func (root *Root) NodeID(ctx context.Context, nodePath string) (string, error) {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return "", err
	}
	return node.ID.Node, nil
}

func (root *Root) NodeCreatedPath(ctx context.Context, nodePath string) (string, error) {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return "", err
	}
	return node.CreatedPath, nil
}

func (root *Root) NodeOrigin(ctx context.Context, nodePath string) (svn.Revnum, error) {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	seen := make(map[ID]struct{})
	for node.Predecessor != nil {
		if err := ctx.Err(); err != nil {
			return svn.InvalidRevnum, err
		}
		if _, duplicate := seen[*node.Predecessor]; duplicate {
			return svn.InvalidRevnum, fmt.Errorf("%w: cyclic node predecessor chain", svn.ErrFSCorrupt)
		}
		seen[*node.Predecessor] = struct{}{}
		node, err = root.filesystem.readNode(*node.Predecessor)
		if err != nil {
			return svn.InvalidRevnum, err
		}
	}
	return node.ID.Revision, nil
}

func (root *Root) ClosestCopy(ctx context.Context, nodePath string) (string, svn.Revnum, error) {
	cleaned, err := cleanPath(nodePath)
	if err != nil {
		return "", svn.InvalidRevnum, err
	}
	closestPath := ""
	closestRevision := svn.InvalidRevnum
	for current := cleaned; ; current = path.Dir(current) {
		node, nodeErr := root.nodeAt(ctx, current)
		if nodeErr != nil {
			return "", svn.InvalidRevnum, nodeErr
		}
		if node.CopyRootRev > closestRevision && node.CopyRootRev > 0 {
			closestPath, closestRevision = node.CopyRootPath, node.CopyRootRev
		}
		if node.CopyFromRev >= 0 && node.ID.Revision > closestRevision {
			closestPath, closestRevision = current, node.ID.Revision
		}
		if current == "/" {
			break
		}
	}
	return closestPath, closestRevision, nil
}

func (root *Root) NodeHistory(ctx context.Context, nodePath string, crossCopies bool) ([]fsapi.HistoryEntry, error) {
	cleaned, err := cleanPath(nodePath)
	if err != nil {
		return nil, err
	}
	return root.nodeHistory(ctx, cleaned, crossCopies, make(map[ID]struct{}))
}

func (root *Root) nodeHistory(ctx context.Context, nodePath string, crossCopies bool, seen map[ID]struct{}) ([]fsapi.HistoryEntry, error) {
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	copyPath, copyRevision, err := root.ClosestCopy(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	history := make([]fsapi.HistoryEntry, 0, node.Count+1)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if copyRevision.IsValid() && node.ID.Revision <= copyRevision {
			break
		}
		history = append(history, fsapi.HistoryEntry{Path: node.CreatedPath, Revision: node.ID.Revision})
		if node.Predecessor == nil {
			break
		}
		if _, duplicate := seen[*node.Predecessor]; duplicate {
			return nil, fmt.Errorf("%w: cyclic node predecessor chain", svn.ErrFSCorrupt)
		}
		seen[*node.Predecessor] = struct{}{}
		node, err = root.filesystem.readNode(*node.Predecessor)
		if err != nil {
			return nil, err
		}
	}
	if !copyRevision.IsValid() {
		return history, nil
	}
	history = append(history, fsapi.HistoryEntry{Path: nodePath, Revision: copyRevision})
	if !crossCopies {
		return history, nil
	}
	copyRootValue, err := root.filesystem.RevisionRoot(ctx, copyRevision)
	if err != nil {
		return nil, err
	}
	copyRoot := copyRootValue.(*Root)
	changes, err := copyRoot.PathsChanged(ctx)
	if err != nil {
		return nil, err
	}
	change, found := changes[copyPath]
	if !found {
		change, found = changes[strings.TrimPrefix(copyPath, "/")]
	}
	if !found || !change.CopyFromRev.IsValid() || change.CopyFromPath == "" {
		return nil, fmt.Errorf("%w: copyroot %s@%d has no copy source", svn.ErrFSCorrupt, copyPath, copyRevision)
	}
	suffix := strings.TrimPrefix(strings.TrimPrefix(nodePath, copyPath), "/")
	sourcePath := path.Join(change.CopyFromPath, suffix)
	sourceRootValue, err := root.filesystem.RevisionRoot(ctx, change.CopyFromRev)
	if err != nil {
		return nil, err
	}
	sourceHistory, err := sourceRootValue.(*Root).nodeHistory(ctx, sourcePath, true, seen)
	if err != nil {
		return nil, err
	}
	return append(history, sourceHistory...), nil
}

func (root *Root) Mergeinfo(ctx context.Context, nodePath string, descendants bool) (map[string]mergeinfo.Mergeinfo, error) {
	cleaned, err := cleanPath(nodePath)
	if err != nil {
		return nil, err
	}
	result := make(map[string]mergeinfo.Mergeinfo)
	if err := root.collectMergeinfo(ctx, cleaned, descendants, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (root *Root) collectMergeinfo(ctx context.Context, nodePath string, descendants bool, result map[string]mergeinfo.Mergeinfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	node, err := root.nodeAt(ctx, nodePath)
	if err != nil {
		return err
	}
	if node.HasMergeinfoCount && node.MergeinfoCount == 0 {
		return nil
	}
	props, err := root.NodeProps(ctx, nodePath)
	if err != nil {
		return err
	}
	if raw, found := props["svn:mergeinfo"]; found {
		parsed, err := mergeinfo.Parse(string(raw))
		if err != nil {
			return err
		}
		result[strings.TrimPrefix(nodePath, "/")] = parsed
	}
	if !descendants || node.Kind != svn.NodeDir {
		return nil
	}
	entries, err := root.DirEntries(ctx, nodePath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := root.collectMergeinfo(ctx, path.Join(nodePath, entry.Name), true, result); err != nil {
			return err
		}
	}
	return nil
}

func (root *Root) GetFileDelta(ctx context.Context, source fsapi.Root, sourcePath, targetPath string) (delta.WindowReader, error) {
	target, err := root.fileTemp(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	files := []*os.File{target}
	var base io.Reader
	if source != nil {
		baseFile, err := rootToTemp(ctx, source, sourcePath)
		if err != nil {
			closeTempFiles(files)
			return nil, err
		}
		files = append(files, baseFile)
		base = baseFile
	}
	return &cleanupWindowReader{WindowReader: delta.NewTxDeltaStream(base, target), files: files}, nil
}

func (root *Root) fileTemp(ctx context.Context, nodePath string) (*os.File, error) {
	return rootToTemp(ctx, root, nodePath)
}

func rootToTemp(ctx context.Context, root fsapi.Root, nodePath string) (*os.File, error) {
	file, err := os.CreateTemp("", "go-svn-fsfs-delta-*")
	if err != nil {
		return nil, err
	}
	if err := root.FileContents(ctx, nodePath, file); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		os.Remove(file.Name())
		return nil, err
	}
	return file, nil
}

type cleanupWindowReader struct {
	delta.WindowReader
	files []*os.File
}

func (reader *cleanupWindowReader) NextWindow() (*delta.Window, error) {
	window, err := reader.WindowReader.NextWindow()
	if err != nil {
		closeTempFiles(reader.files)
		reader.files = nil
	}
	return window, err
}

func closeTempFiles(files []*os.File) {
	for _, file := range files {
		name := file.Name()
		file.Close()
		os.Remove(name)
	}
}

type directoryEntry struct {
	kind svn.NodeKind
	id   ID
}

func (root *Root) nodeAt(ctx context.Context, nodePath string) (NodeRevision, error) {
	node, found, err := root.findNode(ctx, nodePath)
	if err != nil {
		return NodeRevision{}, err
	}
	if !found {
		return NodeRevision{}, svn.NewError(svn.ErrFSNotFound, nodePath)
	}
	return node, nil
}

func (root *Root) findNode(ctx context.Context, nodePath string) (NodeRevision, bool, error) {
	cleaned, err := cleanPath(nodePath)
	if err != nil {
		return NodeRevision{}, false, err
	}
	node := root.root
	if cleaned == "/" {
		return node, true, nil
	}
	for _, name := range strings.Split(strings.TrimPrefix(cleaned, "/"), "/") {
		if err := ctx.Err(); err != nil {
			return NodeRevision{}, false, err
		}
		if node.Kind != svn.NodeDir {
			return NodeRevision{}, false, fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, nodePath)
		}
		entries, err := root.directoryEntries(ctx, node)
		if err != nil {
			return NodeRevision{}, false, err
		}
		entry, ok := entries[name]
		if !ok {
			return NodeRevision{}, false, nil
		}
		node, err = root.filesystem.readNode(entry.id)
		if err != nil {
			return NodeRevision{}, false, err
		}
	}
	return node, true, nil
}

func (root *Root) directoryEntries(ctx context.Context, node NodeRevision) (map[string]directoryEntry, error) {
	if node.Kind != svn.NodeDir {
		return nil, fmt.Errorf("%w: node is not a directory", svn.ErrFSNotDirectory)
	}
	if node.Text == nil {
		return make(map[string]directoryEntry), nil
	}
	var data bytes.Buffer
	if err := root.filesystem.writeRepresentation(ctx, node.Text, &data); err != nil {
		return nil, err
	}
	return parseDirectoryEntries(data.Bytes())
}

func parseDirectoryEntries(data []byte) (map[string]directoryEntry, error) {
	values, err := hashfile.Read(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	entries := make(map[string]directoryEntry, len(values))
	for name, value := range values {
		kindText, idText, ok := strings.Cut(string(value), " ")
		if !ok {
			return nil, fmt.Errorf("%w: invalid directory entry %q", svn.ErrFSCorrupt, value)
		}
		kind, err := svn.ParseNodeKind(kindText)
		if err != nil || kind != svn.NodeFile && kind != svn.NodeDir {
			return nil, fmt.Errorf("%w: invalid directory entry kind %q", svn.ErrFSCorrupt, kindText)
		}
		id, err := ParseID(idText)
		if err != nil {
			return nil, err
		}
		entries[name] = directoryEntry{kind: kind, id: id}
	}
	return entries, nil
}

func (filesystem *FS) readNode(id ID) (NodeRevision, error) {
	file, err := filesystem.openRevision(id.Revision)
	if err != nil {
		return NodeRevision{}, err
	}
	offset, err := file.itemOffset(id.Revision, id.Item)
	if err != nil {
		return NodeRevision{}, err
	}
	if offset < 0 || offset >= file.size {
		return NodeRevision{}, fmt.Errorf("%w: invalid node-revision offset", svn.ErrFSCorrupt)
	}
	data, err := file.recordData(offset)
	if err != nil {
		return NodeRevision{}, err
	}
	node, err := parseNodeRevision(data)
	if err != nil {
		return NodeRevision{}, err
	}
	if node.ID != id {
		return NodeRevision{}, fmt.Errorf("%w: node-revision ID mismatch", svn.ErrFSCorrupt)
	}
	return node, nil
}

func cleanPath(value string) (string, error) {
	if strings.ContainsRune(value, '\x00') || strings.Contains(value, "\\") {
		return "", fmt.Errorf("%w: invalid filesystem path %q", svn.ErrFSPathSyntax, value)
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(value, "/"))
	if cleaned == "/.." || strings.HasPrefix(cleaned, "/../") {
		return "", fmt.Errorf("%w: invalid filesystem path %q", svn.ErrFSPathSyntax, value)
	}
	return cleaned, nil
}
