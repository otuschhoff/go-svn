package fsfs

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	fsapi "github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/hashfile"
)

const (
	p2lFileRepresentation = 1
	p2lDirRepresentation  = 2
	p2lFileProperties     = 3
	p2lDirProperties      = 4
	p2lNodeRevision       = 5
	p2lChanges            = 6
)

type revisionItem struct {
	number int64
	kind   uint64
	data   []byte
	offset int64
}

type commitNode struct {
	id         ID
	text       *Representation
	properties *Representation
}

type commitBuilder struct {
	transaction *Transaction
	revision    svn.Revnum
	nextItem    int64
	nodes       map[*txnNode]*commitNode
	items       []*revisionItem
}

func (transaction *Transaction) Commit(ctx context.Context, lockTokens map[string]string, keepLocks bool) (svn.Revnum, error) {
	if err := ctx.Err(); err != nil {
		return svn.InvalidRevnum, err
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.root == nil {
		rootValue, err := transaction.filesystem.RevisionRoot(ctx, transaction.base)
		if err != nil {
			return svn.InvalidRevnum, err
		}
		revisionRoot := rootValue.(*Root)
		transaction.root = &TxnRoot{transaction: transaction, node: newTxnNode(revisionRoot, "/", revisionRoot.root)}
		transaction.changes = make(map[string]fsapi.PathChange)
	}
	var committed svn.Revnum
	err := withWriteLock(ctx, filepath.Join(transaction.filesystem.path, "db", "write-lock"), func() error {
		youngest, err := transaction.filesystem.YoungestRevision(ctx)
		if err != nil {
			return err
		}
		if youngest != transaction.base {
			if err := transaction.mergeYoungest(ctx, youngest); err != nil {
				return err
			}
		}
		for nodePath := range transaction.changes {
			lock, err := transaction.filesystem.GetLock(ctx, nodePath)
			if err != nil {
				return err
			}
			if lock != nil && lockTokens[nodePath] != lock.Token {
				return fmt.Errorf("%w: %s", svn.ErrFSBadLockToken, nodePath)
			}
		}
		committed = youngest + 1
		builder := &commitBuilder{transaction: transaction, revision: committed, nextItem: 3, nodes: make(map[*txnNode]*commitNode)}
		var revisionData []byte
		if transaction.filesystem.format.Addressing == AddressingLogical {
			if err := builder.assignNodes(ctx, transaction.root.node, true); err != nil {
				return err
			}
			if err := builder.buildNode(ctx, transaction.root.node); err != nil {
				return err
			}
			changes, err := builder.buildChanges(ctx)
			if err != nil {
				return err
			}
			builder.items = append(builder.items, &revisionItem{number: 1, kind: p2lChanges, data: changes})
			revisionData, err = builder.logicalRevision()
		} else {
			revisionData, err = builder.physicalRevision(ctx)
		}
		if err != nil {
			return err
		}
		revisionPath := transaction.filesystem.unpackedRevisionPath(committed)
		if err := os.MkdirAll(filepath.Dir(revisionPath), 0o755); err != nil {
			return err
		}
		if err := writeFileAtomic(revisionPath, revisionData, 0o444); err != nil {
			return err
		}
		properties, err := transaction.Properties(ctx)
		if err != nil {
			_ = os.Remove(revisionPath)
			return err
		}
		properties["svn:date"] = []byte(svn.FormatDate(time.Now().UTC()))
		revpropPath := transaction.filesystem.unpackedRevpropPath(committed)
		if err := os.MkdirAll(filepath.Dir(revpropPath), 0o755); err != nil {
			_ = os.Remove(revisionPath)
			return err
		}
		if err := writeHashAtomic(revpropPath, properties); err != nil {
			_ = os.Remove(revisionPath)
			return err
		}
		currentPath := filepath.Join(transaction.filesystem.path, "db", "current")
		if err := writeFileAtomic(currentPath, []byte(strconv.FormatInt(int64(committed), 10)+"\n"), 0o666); err != nil {
			_ = os.Remove(revisionPath)
			_ = os.Remove(revpropPath)
			return err
		}
		if !keepLocks {
			for nodePath, token := range lockTokens {
				if _, changed := transaction.changes[nodePath]; !changed {
					continue
				}
				if lock, _ := transaction.filesystem.GetLock(ctx, nodePath); lock != nil && lock.Token == token {
					digest := lockDigest(nodePath)
					_ = os.Remove(transaction.filesystem.lockPath(digest))
					_ = transaction.filesystem.updateLockParents(nodePath, digest, false)
				}
			}
		}
		return transaction.remove()
	})
	return committed, err
}

func (builder *commitBuilder) physicalRevision(ctx context.Context) ([]byte, error) {
	var data bytes.Buffer
	if err := builder.buildPhysicalNode(ctx, builder.transaction.root.node, &data); err != nil {
		return nil, err
	}
	rootOffset := builder.nodes[builder.transaction.root.node].id.Item
	changes, err := builder.buildChanges(ctx)
	if err != nil {
		return nil, err
	}
	changesOffset := data.Len()
	data.Write(changes)
	fmt.Fprintf(&data, "%d %d\n", rootOffset, changesOffset)
	return data.Bytes(), nil
}

func (builder *commitBuilder) buildPhysicalNode(ctx context.Context, node *txnNode, data *bytes.Buffer) error {
	if !node.dirty && node != builder.transaction.root.node {
		return nil
	}
	if node.kind == svn.NodeDir {
		if err := node.loadChildren(ctx); err != nil {
			return err
		}
		for _, name := range sortedTxnChildren(node.children) {
			if node.children[name].dirty {
				if err := builder.buildPhysicalNode(ctx, node.children[name], data); err != nil {
					return err
				}
			}
		}
	}
	info := &commitNode{}
	builder.nodes[node] = info
	if node.kind == svn.NodeDir {
		values := make(svn.Props, len(node.children))
		for name, child := range node.children {
			childID := child.baseNode.ID
			if childInfo := builder.nodes[child]; childInfo != nil {
				childID = childInfo.id
			}
			values[name] = []byte(child.kind.String() + " " + childID.String())
		}
		var directory bytes.Buffer
		if err := hashfile.Write(&directory, values); err != nil {
			return err
		}
		info.text = builder.addPhysicalRepresentation(data, directory.Bytes(), false)
	} else if node.textModified || node.newNode {
		if err := node.loadContent(ctx); err != nil {
			return err
		}
		info.text = builder.addPhysicalRepresentation(data, node.content, true)
	} else {
		info.text = node.baseNode.Text
	}
	if node.propsModified || node.newNode {
		if err := node.loadProperties(ctx); err != nil {
			return err
		}
		if len(node.properties) != 0 {
			var properties bytes.Buffer
			if err := hashfile.Write(&properties, node.properties); err != nil {
				return err
			}
			info.properties = builder.addPhysicalRepresentation(data, properties.Bytes(), false)
		}
	} else {
		info.properties = node.baseNode.Properties
	}
	item := int64(data.Len())
	nodeID, copyID := node.baseNode.ID.Node, node.baseNode.ID.Copy
	if node.newNode {
		nodeID = permanentIDPart(node.temporaryID, builder.revision)
	}
	if node.copyFromRev.IsValid() {
		copyID = permanentIDPart(node.temporaryID, builder.revision)
	} else if node.newNode {
		copyID = "0"
	}
	info.id = ID{Node: nodeID, Copy: copyID, Revision: builder.revision, Item: item}
	data.Write(builder.nodeRecord(node, info))
	return nil
}

func (builder *commitBuilder) addPhysicalRepresentation(data *bytes.Buffer, content []byte, shareable bool) *Representation {
	item := int64(data.Len())
	md5Sum, sha1Sum := md5.Sum(content), sha1.Sum(content)
	representation := &Representation{Revision: builder.revision, Item: item, Length: int64(len(content)), ExpandedSize: int64(len(content)), MD5: hex.EncodeToString(md5Sum[:])}
	if shareable {
		representation.SHA1 = hex.EncodeToString(sha1Sum[:])
		representation.Uniquifier = builder.transaction.name + "/_" + strconv.FormatInt(item, 36)
	}
	data.WriteString("PLAIN\n")
	data.Write(content)
	data.WriteString("ENDREP\n")
	return representation
}

func (builder *commitBuilder) assignNodes(ctx context.Context, node *txnNode, root bool) error {
	if !node.dirty && !root {
		return nil
	}
	item := int64(2)
	if !root {
		item = builder.nextItem
		builder.nextItem++
	}
	nodeID, copyID := node.baseNode.ID.Node, node.baseNode.ID.Copy
	if node.newNode {
		nodeID = permanentIDPart(node.temporaryID, builder.revision)
	}
	if node.copyFromRev.IsValid() {
		copyID = permanentIDPart(node.temporaryID, builder.revision)
	} else if node.newNode {
		copyID = "0"
	}
	builder.nodes[node] = &commitNode{id: ID{Node: nodeID, Copy: copyID, Revision: builder.revision, Item: item}}
	if node.kind == svn.NodeDir && node.childrenReady {
		names := sortedTxnChildren(node.children)
		for _, name := range names {
			if node.children[name].dirty {
				if err := builder.assignNodes(ctx, node.children[name], false); err != nil {
					return err
				}
			}
		}
	}
	return ctx.Err()
}

func (builder *commitBuilder) buildNode(ctx context.Context, node *txnNode) error {
	info := builder.nodes[node]
	if info == nil {
		return nil
	}
	if node.kind == svn.NodeDir {
		if err := node.loadChildren(ctx); err != nil {
			return err
		}
		for _, name := range sortedTxnChildren(node.children) {
			if node.children[name].dirty {
				if err := builder.buildNode(ctx, node.children[name]); err != nil {
					return err
				}
			}
		}
		var directory bytes.Buffer
		values := make(svn.Props, len(node.children))
		for name, child := range node.children {
			childID := child.baseNode.ID
			if childInfo := builder.nodes[child]; childInfo != nil {
				childID = childInfo.id
			}
			values[name] = []byte(child.kind.String() + " " + childID.String())
		}
		if err := hashfile.Write(&directory, values); err != nil {
			return err
		}
		info.text = builder.addRepresentation(directory.Bytes(), p2lDirRepresentation)
	} else if node.textModified || node.newNode {
		if err := node.loadContent(ctx); err != nil {
			return err
		}
		info.text = builder.addRepresentation(node.content, p2lFileRepresentation)
	} else {
		info.text = node.baseNode.Text
	}
	if node.propsModified || node.newNode {
		if err := node.loadProperties(ctx); err != nil {
			return err
		}
		if len(node.properties) != 0 {
			var properties bytes.Buffer
			if err := hashfile.Write(&properties, node.properties); err != nil {
				return err
			}
			kind := uint64(p2lFileProperties)
			if node.kind == svn.NodeDir {
				kind = p2lDirProperties
			}
			info.properties = builder.addRepresentation(properties.Bytes(), kind)
		}
	} else {
		info.properties = node.baseNode.Properties
	}
	builder.items = append(builder.items, &revisionItem{number: info.id.Item, kind: p2lNodeRevision, data: builder.nodeRecord(node, info)})
	return nil
}

func (builder *commitBuilder) addRepresentation(content []byte, kind uint64) *Representation {
	item := builder.nextItem
	builder.nextItem++
	md5Sum, sha1Sum := md5.Sum(content), sha1.Sum(content)
	representation := &Representation{Revision: builder.revision, Item: item, Length: int64(len(content)), ExpandedSize: int64(len(content)), MD5: hex.EncodeToString(md5Sum[:]), SHA1: hex.EncodeToString(sha1Sum[:]), Uniquifier: builder.transaction.name + "/_" + strconv.FormatInt(item, 36)}
	data := make([]byte, 0, len(content)+13)
	data = append(data, "PLAIN\n"...)
	data = append(data, content...)
	data = append(data, "ENDREP\n"...)
	builder.items = append(builder.items, &revisionItem{number: item, kind: kind, data: data})
	return representation
}

func (builder *commitBuilder) nodeRecord(node *txnNode, info *commitNode) []byte {
	var record bytes.Buffer
	fmt.Fprintf(&record, "id: %s\ntype: %s\n", info.id.String(), node.kind.String())
	if !node.newNode {
		fmt.Fprintf(&record, "pred: %s\ncount: %d\n", node.baseNode.ID.String(), node.baseNode.Count+1)
	} else {
		fmt.Fprintln(&record, "count: 0")
	}
	if info.text != nil {
		fmt.Fprintf(&record, "text: %s\n", formatRepresentation(info.text))
	}
	if info.properties != nil {
		fmt.Fprintf(&record, "props: %s\n", formatRepresentation(info.properties))
	}
	fmt.Fprintf(&record, "cpath: %s\n", node.createdPath)
	copyRootRevision, copyRootPath := node.baseNode.CopyRootRev, node.baseNode.CopyRootPath
	if node.newNode && !node.copyFromRev.IsValid() {
		copyRootRevision, copyRootPath = 0, "/"
	}
	if node.copyFromRev.IsValid() {
		copyRootRevision, copyRootPath = builder.revision, node.createdPath
	}
	if copyRootRevision.IsValid() && copyRootPath != "" {
		fmt.Fprintf(&record, "copyroot: %d %s\n", copyRootRevision, copyRootPath)
	}
	if node.copyFromRev.IsValid() {
		fmt.Fprintf(&record, "copyfrom: %d %s\n", node.copyFromRev, node.copyFromPath)
	}
	if node.baseNode.HasMergeinfoCount {
		fmt.Fprintf(&record, "minfo-cnt: %d\n", node.baseNode.MergeinfoCount)
	}
	if node.baseNode.MergeinfoHere {
		fmt.Fprintln(&record, "minfo-here: true")
	}
	fmt.Fprintln(&record)
	return record.Bytes()
}

func formatRepresentation(representation *Representation) string {
	if representation.SHA1 == "" && representation.Uniquifier == "" {
		return fmt.Sprintf("%d %d %d %d %s", representation.Revision, representation.Item, representation.Length, representation.ExpandedSize, representation.MD5)
	}
	sha1Value, uniquifier := representation.SHA1, representation.Uniquifier
	if sha1Value == "" {
		sha1Value = "-"
	}
	if uniquifier == "" {
		uniquifier = "-"
	}
	return fmt.Sprintf("%d %d %d %d %s %s %s", representation.Revision, representation.Item, representation.Length, representation.ExpandedSize, representation.MD5, sha1Value, uniquifier)
}

func (builder *commitBuilder) buildChanges(ctx context.Context) ([]byte, error) {
	paths := make([]string, 0, len(builder.transaction.changes))
	for nodePath := range builder.transaction.changes {
		paths = append(paths, nodePath)
	}
	sort.Strings(paths)
	var data bytes.Buffer
	for _, nodePath := range paths {
		change := builder.transaction.changes[nodePath]
		id := "0.0.r0/2"
		if node, _, err := builder.transaction.root.lookup(ctx, nodePath); err == nil && node != nil {
			if info := builder.nodes[node]; info != nil {
				id = info.id.String()
			} else {
				id = node.baseNode.ID.String()
			}
		}
		fmt.Fprintf(&data, "%s %s-%s %t %t false %s\n", id, change.Kind, change.NodeKind.String(), change.TextModified, change.PropsModified, change.Path)
		if change.CopyFromRev.IsValid() && change.CopyFromPath != "" {
			fmt.Fprintf(&data, "%d %s\n", change.CopyFromRev, change.CopyFromPath)
		} else {
			fmt.Fprintln(&data)
		}
	}
	fmt.Fprintln(&data)
	return data.Bytes(), nil
}

func (builder *commitBuilder) logicalRevision() ([]byte, error) {
	var data bytes.Buffer
	entries := make([]p2lEntry, 0, len(builder.items))
	locations := make(map[int64]int64, len(builder.items))
	for _, item := range builder.items {
		item.offset = int64(data.Len())
		locations[item.number] = item.offset
		if _, err := data.Write(item.data); err != nil {
			return nil, err
		}
		entries = append(entries, p2lEntry{offset: item.offset, size: int64(len(item.data)), kind: item.kind, checksum: fnv1a32x4(item.data), revision: builder.revision, item: item.number})
	}
	l2p := encodeL2P(builder.revision, locations)
	p2l := encodeP2L(builder.revision, int64(data.Len()), entries)
	l2pOffset := data.Len()
	data.Write(l2p)
	p2lOffset := data.Len()
	data.Write(p2l)
	l2pDigest, p2lDigest := md5.Sum(l2p), md5.Sum(p2l)
	footer := fmt.Sprintf("%d %x %d %x", l2pOffset, l2pDigest, p2lOffset, p2lDigest)
	if len(footer) > 255 {
		return nil, fmt.Errorf("%w: logical footer is too long", svn.ErrFSCorrupt)
	}
	data.WriteString(footer)
	data.WriteByte(byte(len(footer)))
	return data.Bytes(), nil
}

func encodeL2P(revision svn.Revnum, locations map[int64]int64) []byte {
	maximum := int64(0)
	for item := range locations {
		if item > maximum {
			maximum = item
		}
	}
	var page bytes.Buffer
	var previous int64
	for item := int64(0); item <= maximum; item++ {
		offset := int64(-1)
		if value, found := locations[item]; found {
			offset = value
		}
		current := offset + 1
		writeSigned(&page, current-previous)
		previous = current
	}
	var index bytes.Buffer
	index.WriteString("L2P-INDEX\n")
	writeUnsigned(&index, uint64(revision))
	writeUnsigned(&index, nextPowerOfTwo(uint64(maximum+1)))
	writeUnsigned(&index, 1)
	writeUnsigned(&index, 1)
	writeUnsigned(&index, 1)
	writeUnsigned(&index, uint64(page.Len()))
	writeUnsigned(&index, uint64(maximum+1))
	index.Write(page.Bytes())
	return index.Bytes()
}

func encodeP2L(revision svn.Revnum, dataSize int64, entries []p2lEntry) []byte {
	pageSize := nextPowerOfTwo(uint64(max(dataSize, 1)))
	if pageSize < 1<<20 {
		pageSize = 1 << 20
	}
	entries = append(entries, p2lEntry{offset: dataSize, size: int64(pageSize) - dataSize, revision: revision})
	var page bytes.Buffer
	writeUnsigned(&page, 0)
	lastCompound, lastRevision := int64(0), int64(revision)
	for _, entry := range entries {
		writeUnsigned(&page, uint64(entry.size))
		compound := entry.item*8 + int64(entry.kind)
		writeSigned(&page, compound-lastCompound)
		writeSigned(&page, int64(entry.revision)-lastRevision)
		writeUnsigned(&page, uint64(entry.checksum))
		lastCompound, lastRevision = compound, int64(entry.revision)
	}
	var index bytes.Buffer
	index.WriteString("P2L-INDEX\n")
	writeUnsigned(&index, uint64(revision))
	writeUnsigned(&index, uint64(dataSize))
	writeUnsigned(&index, pageSize)
	writeUnsigned(&index, 1)
	writeUnsigned(&index, uint64(page.Len()))
	index.Write(page.Bytes())
	return index.Bytes()
}

func writeUnsigned(writer *bytes.Buffer, value uint64) {
	for value >= 0x80 {
		writer.WriteByte(byte(value) | 0x80)
		value >>= 7
	}
	writer.WriteByte(byte(value))
}

func writeSigned(writer *bytes.Buffer, value int64) {
	encoded := uint64(value << 1)
	if value < 0 {
		encoded = uint64(-value<<1 - 1)
	}
	writeUnsigned(writer, encoded)
}

func nextPowerOfTwo(value uint64) uint64 {
	result := uint64(1)
	for result < value {
		result <<= 1
	}
	return result
}

func sortedTxnChildren(children map[string]*txnNode) []string {
	names := make([]string, 0, len(children))
	for name := range children {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func permanentIDPart(temporary string, revision svn.Revnum) string {
	counter := strings.TrimPrefix(temporary, "_")
	if counter == "" {
		counter = "0"
	}
	return counter + "-" + strconv.FormatInt(int64(revision), 36)
}

func (filesystem *FS) unpackedRevisionPath(revision svn.Revnum) string {
	base := filepath.Join(filesystem.path, "db", "revs")
	if filesystem.format.Layout == LayoutLinear {
		return filepath.Join(base, strconv.FormatInt(int64(revision), 10))
	}
	return filepath.Join(base, strconv.FormatInt(int64(revision)/filesystem.format.ShardSize, 10), strconv.FormatInt(int64(revision), 10))
}

func (filesystem *FS) unpackedRevpropPath(revision svn.Revnum) string {
	base := filepath.Join(filesystem.path, "db", "revprops")
	if filesystem.format.Layout == LayoutLinear {
		return filepath.Join(base, strconv.FormatInt(int64(revision), 10))
	}
	return filepath.Join(base, strconv.FormatInt(int64(revision)/filesystem.format.ShardSize, 10), strconv.FormatInt(int64(revision), 10))
}
