package fsfs

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/hashfile"
)

type CreateOptions struct {
	Format      int
	ShardSize   int64
	Compression Compression
}

func Create(ctx context.Context, repositoryPath string, options CreateOptions) (*FS, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Format == 0 {
		options.Format = 8
	}
	if options.Format < 6 || options.Format > 8 {
		return nil, fmt.Errorf("%w: writable FSFS format %d", svn.ErrFSUnsupportedFormat, options.Format)
	}
	if options.ShardSize == 0 {
		options.ShardSize = 1000
	}
	if options.ShardSize < 1 {
		return nil, fmt.Errorf("%w: invalid shard size", svn.ErrIncorrectParams)
	}
	absolute, err := filepath.Abs(repositoryPath)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(absolute, 0o755); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(absolute)
		}
	}()
	directories := []string{
		"conf", "hooks", "locks", "db/revs/0", "db/revprops/0", "db/transactions", "db/txn-protorevs", "db/locks", "db/node-origins",
	}
	for _, directory := range directories {
		if err := os.MkdirAll(filepath.Join(absolute, directory), 0o755); err != nil {
			return nil, err
		}
	}
	formatData := fmt.Sprintf("%d\nlayout sharded %d\n", options.Format, options.ShardSize)
	if options.Format >= 7 {
		formatData += "addressing logical\n"
	}
	uuid := randomUUID()
	uuidData := uuid + "\n"
	if options.Format >= 7 {
		uuidData += randomUUID() + "\n"
	}
	files := map[string]string{
		"format":                         "5\n",
		"README.txt":                     "This is a Subversion repository; use the 'svnadmin' tool to examine it.\n",
		"db/format":                      formatData,
		"db/fs-type":                     "fsfs\n",
		"db/current":                     "0\n",
		"db/txn-current":                 "0\n",
		"db/min-unpacked-rev":            "0\n",
		"db/uuid":                        uuidData,
		"db/fsfs.conf":                   createFSFSConfig(options),
		"db/write-lock":                  "",
		"db/txn-current-lock":            "",
		"locks/db.lock":                  "",
		"locks/db-logs.lock":             "",
		"conf/svnserve.conf":             "[general]\n",
		"conf/passwd":                    "[users]\n",
		"conf/authz":                     "[aliases]\n[groups]\n",
		"conf/hooks-env.tmpl":            "[default]\n",
		"hooks/start-commit.tmpl":        "#!/bin/sh\nexit 0\n",
		"hooks/pre-commit.tmpl":          "#!/bin/sh\nexit 0\n",
		"hooks/post-commit.tmpl":         "#!/bin/sh\nexit 0\n",
		"hooks/pre-lock.tmpl":            "#!/bin/sh\nexit 0\n",
		"hooks/post-lock.tmpl":           "#!/bin/sh\nexit 0\n",
		"hooks/pre-unlock.tmpl":          "#!/bin/sh\nexit 0\n",
		"hooks/post-unlock.tmpl":         "#!/bin/sh\nexit 0\n",
		"hooks/pre-revprop-change.tmpl":  "#!/bin/sh\nexit 0\n",
		"hooks/post-revprop-change.tmpl": "#!/bin/sh\nexit 0\n",
	}
	for name, data := range files {
		mode := os.FileMode(0o666)
		if strings.HasPrefix(name, "hooks/") && strings.HasSuffix(name, ".tmpl") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(absolute, name), []byte(data), mode); err != nil {
			return nil, err
		}
	}
	format := Format{Number: options.Format, Layout: LayoutSharded, ShardSize: options.ShardSize, Addressing: AddressingPhysical}
	if options.Format >= 7 {
		format.Addressing = AddressingLogical
	}
	revisionData, err := initialRevision(format)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(absolute, "db", "revs", "0", "0"), revisionData, 0o444); err != nil {
		return nil, err
	}
	var revprops bytes.Buffer
	if err := writeInitialRevprops(&revprops); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(absolute, "db", "revprops", "0", "0"), revprops.Bytes(), 0o444); err != nil {
		return nil, err
	}
	cleanup = false
	return Open(ctx, absolute)
}

func initialRevision(format Format) ([]byte, error) {
	content := []byte("END\n")
	digest := md5.Sum(content)
	representation := &Representation{Revision: 0, Length: int64(len(content)), ExpandedSize: int64(len(content)), MD5: hex.EncodeToString(digest[:])}
	representationData := append(append([]byte("PLAIN\n"), content...), []byte("ENDREP\n")...)
	if format.Addressing == AddressingPhysical {
		representation.Item = 0
		rootOffset := int64(len(representationData))
		record := fmt.Sprintf("id: 0.0.r0/%d\ntype: dir\ncount: 0\ntext: %s\ncpath: /\n\n", rootOffset, formatRepresentation(representation))
		changesOffset := rootOffset + int64(len(record))
		return fmt.Appendf(nil, "%s%s\n%d %d\n", representationData, record, rootOffset, changesOffset), nil
	}
	representation.Item = 3
	record := []byte("id: 0.0.r0/2\ntype: dir\ncount: 0\ntext: " + formatRepresentation(representation) + "\ncpath: /\n\n")
	builder := &commitBuilder{revision: 0, items: []*revisionItem{
		{number: 3, kind: p2lDirRepresentation, data: representationData},
		{number: 2, kind: p2lNodeRevision, data: record},
		{number: 1, kind: p2lChanges, data: []byte("\n")},
	}}
	return builder.logicalRevision()
}

func writeInitialRevprops(buffer *bytes.Buffer) error {
	return hashfile.Write(buffer, svn.Props{"svn:date": []byte(svn.FormatDate(time.Now().UTC()))})
}

func createFSFSConfig(options CreateOptions) string {
	if options.Compression == "" {
		return ""
	}
	return "[deltification]\ncompression = " + string(options.Compression) + "\n"
}

func randomUUID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}
