package fsfs

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/hashfile"
)

type txnRepresentation struct {
	representation *Representation
	offset         int64
	data           []byte
	kind           uint32
}

func (root *TxnRoot) persist(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var nodes []*txnNode
	if err := collectTxnNodes(ctx, root.node, &nodes); err != nil {
		return err
	}
	nextItem := root.transaction.nextNode
	var protorev bytes.Buffer
	representations := make([]txnRepresentation, 0)
	for _, node := range nodes {
		if !node.dirty {
			continue
		}
		var text *Representation
		if node.kind == svn.NodeFile && (node.textModified || node.newNode) {
			if err := node.loadContent(ctx); err != nil {
				return err
			}
			item := int64(protorev.Len())
			if root.transaction.filesystem.format.Addressing == AddressingLogical {
				item = nextItem
				nextItem++
			}
			md5Sum, sha1Sum := md5.Sum(node.content), sha1.Sum(node.content)
			uniquifier := nextItem
			nextItem++
			text = &Representation{Revision: -1, Item: item, Length: int64(len(node.content)), ExpandedSize: int64(len(node.content)), MD5: hex.EncodeToString(md5Sum[:]), SHA1: hex.EncodeToString(sha1Sum[:]), Uniquifier: root.transaction.name + "/_" + strconv.FormatInt(uniquifier, 36)}
			data := append(append([]byte("PLAIN\n"), node.content...), []byte("ENDREP\n")...)
			representations = append(representations, txnRepresentation{representation: text, offset: int64(protorev.Len()), data: data, kind: uint32(p2lFileRepresentation)})
			protorev.Write(data)
		} else {
			text = node.baseNode.Text
		}
		if node.kind == svn.NodeDir && node.childrenReady {
			values := make(svn.Props, len(node.children))
			for name, child := range node.children {
				values[name] = []byte(child.kind.String() + " " + root.transaction.txnNodeID(child))
			}
			if err := writeTxnHash(root.transaction.nodePath(node)+".children", values); err != nil {
				return err
			}
			text = nil
		}
		var properties *Representation
		if node.propsModified || node.newNode {
			if err := node.loadProperties(ctx); err != nil {
				return err
			}
			if len(node.properties) != 0 {
				if err := writeTxnHash(root.transaction.nodePath(node)+".props", node.properties); err != nil {
					return err
				}
				properties = &Representation{Revision: -1}
			}
		} else {
			properties = node.baseNode.Properties
		}
		if err := writeFileAtomic(root.transaction.nodePath(node), root.transaction.nodeRecord(node, text, properties), 0o666); err != nil {
			return err
		}
	}
	if err := writeFileAtomic(root.transaction.protorevPath(), protorev.Bytes(), 0o666); err != nil {
		return err
	}
	if root.transaction.filesystem.format.Addressing == AddressingLogical {
		if err := root.transaction.writeProtoIndexes(representations); err != nil {
			return err
		}
		if err := writeFileAtomic(root.transaction.path("itemidx"), []byte(strconv.FormatInt(nextItem, 10)), 0o666); err != nil {
			return err
		}
	}
	root.transaction.nextNode = nextItem
	if err := writeFileAtomic(root.transaction.path("next-ids"), []byte(strconv.FormatInt(nextItem, 36)+" 0\n\x00"), 0o666); err != nil {
		return err
	}
	return writeFileAtomic(root.transaction.path("changes"), root.transaction.transactionChanges(ctx), 0o666)
}

func collectTxnNodes(ctx context.Context, node *txnNode, result *[]*txnNode) error {
	if !node.dirty {
		return nil
	}
	*result = append(*result, node)
	if node.kind == svn.NodeDir && node.childrenReady {
		names := make([]string, 0, len(node.children))
		for name := range node.children {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if err := collectTxnNodes(ctx, node.children[name], result); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (transaction *Transaction) txnNodeID(node *txnNode) string {
	nodeID, copyID := node.baseNode.ID.Node, node.baseNode.ID.Copy
	if node.temporaryID != "" {
		nodeID, copyID = node.temporaryID, "0"
	}
	return fmt.Sprintf("%s.%s.t%s", nodeID, copyID, transaction.name)
}

func (transaction *Transaction) nodePath(node *txnNode) string {
	parts := strings.Split(transaction.txnNodeID(node), ".")
	return transaction.path("node." + parts[0] + "." + parts[1])
}

func (transaction *Transaction) nodeRecord(node *txnNode, text, properties *Representation) []byte {
	var record bytes.Buffer
	fmt.Fprintf(&record, "id: %s\ntype: %s\n", transaction.txnNodeID(node), node.kind.String())
	if !node.newNode {
		fmt.Fprintf(&record, "pred: %s\ncount: %d\n", node.baseNode.ID.String(), node.baseNode.Count+1)
	} else {
		fmt.Fprintln(&record, "count: 0")
	}
	if node.kind == svn.NodeDir && node.childrenReady {
		fmt.Fprintln(&record, "text: -1")
	} else if text != nil {
		fmt.Fprintf(&record, "text: %s\n", formatRepresentation(text))
	}
	if (node.propsModified || node.newNode) && properties != nil {
		fmt.Fprintln(&record, "props: -1")
	} else if properties != nil {
		fmt.Fprintf(&record, "props: %s\n", formatRepresentation(properties))
	}
	fmt.Fprintf(&record, "cpath: %s\n", node.createdPath)
	copyRootRevision, copyRootPath := node.baseNode.CopyRootRev, node.baseNode.CopyRootPath
	if node.newNode && !node.copyFromRev.IsValid() {
		copyRootRevision, copyRootPath = 0, "/"
	}
	if node.copyFromRev.IsValid() {
		copyRootRevision, copyRootPath = transaction.base, node.createdPath
	}
	if copyRootRevision.IsValid() && copyRootPath != "" {
		fmt.Fprintf(&record, "copyroot: %d %s\n", copyRootRevision, copyRootPath)
	}
	if node.copyFromRev.IsValid() {
		fmt.Fprintf(&record, "copyfrom: %d %s\n", node.copyFromRev, node.copyFromPath)
	}
	fmt.Fprintln(&record)
	return record.Bytes()
}

func (transaction *Transaction) transactionChanges(ctx context.Context) []byte {
	paths := make([]string, 0, len(transaction.changes))
	for nodePath := range transaction.changes {
		paths = append(paths, nodePath)
	}
	sort.Strings(paths)
	var data bytes.Buffer
	for _, nodePath := range paths {
		change := transaction.changes[nodePath]
		id := "0.0.t" + transaction.name
		if node, _, err := transaction.root.lookup(ctx, nodePath); err == nil && node != nil {
			id = transaction.txnNodeID(node)
		}
		fmt.Fprintf(&data, "%s %s-%s %t %t", id, change.Kind, change.NodeKind.String(), change.TextModified, change.PropsModified)
		if transaction.filesystem.format.Addressing == AddressingLogical {
			fmt.Fprint(&data, " false")
		}
		fmt.Fprintf(&data, " %s\n", change.Path)
		if change.CopyFromRev.IsValid() && change.CopyFromPath != "" {
			fmt.Fprintf(&data, "%d %s\n", change.CopyFromRev, change.CopyFromPath)
		} else {
			fmt.Fprintln(&data)
		}
	}
	return data.Bytes()
}

func (transaction *Transaction) writeProtoIndexes(representations []txnRepresentation) error {
	sort.Slice(representations, func(left, right int) bool {
		return representations[left].representation.Item < representations[right].representation.Item
	})
	var l2p, p2l bytes.Buffer
	for _, item := range representations {
		binary.Write(&l2p, binary.LittleEndian, uint64(item.offset+1))
		binary.Write(&l2p, binary.LittleEndian, uint64(item.representation.Item))
		binary.Write(&p2l, binary.LittleEndian, uint64(item.offset))
		binary.Write(&p2l, binary.LittleEndian, uint64(len(item.data)))
		binary.Write(&p2l, binary.LittleEndian, item.kind)
		checksum := fnv1a32x4(item.data)
		binary.Write(&p2l, binary.LittleEndian, checksum)
		binary.Write(&p2l, binary.LittleEndian, [3]uint32{})
		binary.Write(&p2l, binary.LittleEndian, uint64(item.representation.Item))
	}
	if err := writeFileAtomic(transaction.path("index.l2p"), l2p.Bytes(), 0o666); err != nil {
		return err
	}
	return writeFileAtomic(transaction.path("index.p2l"), p2l.Bytes(), 0o666)
}

func writeTxnHash(name string, values svn.Props) error {
	var data bytes.Buffer
	if err := hashfile.Write(&data, values); err != nil {
		return err
	}
	return writeFileAtomic(name, data.Bytes(), 0o666)
}
