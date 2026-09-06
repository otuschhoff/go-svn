package repos

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/hashfile"
)

type UUIDAction string

const (
	UUIDDefault UUIDAction = "default"
	UUIDIgnore  UUIDAction = "ignore"
	UUIDForce   UUIDAction = "force"
)

type LoadOptions struct {
	ParentDir string
	UUID      UUIDAction
}

type dumpRecord struct {
	headers map[string]string
	content []byte
}

type loadRevision struct {
	number      svn.Revnum
	properties  svn.Props
	transaction fs.Txn
	root        fs.TxnRoot
}

func Load(ctx context.Context, repository *Repository, reader io.Reader, options LoadOptions) error {
	return repository.Load(ctx, reader, options)
}

func (repository *Repository) Load(ctx context.Context, input io.Reader, options LoadOptions) error {
	parentDir := cleanCommitPath(options.ParentDir)
	if options.ParentDir == "" {
		parentDir = "/"
	}
	if options.UUID == "" {
		options.UUID = UUIDDefault
	}
	if options.UUID != UUIDDefault && options.UUID != UUIDIgnore && options.UUID != UUIDForce {
		return fmt.Errorf("%w: invalid UUID action %q", svn.ErrIncorrectParams, options.UUID)
	}
	youngest, err := repository.Youngest(ctx)
	if err != nil {
		return err
	}
	if parentDir != "/" {
		root, _, err := repository.Root(ctx, youngest)
		if err != nil {
			return err
		}
		if kind, err := root.CheckPath(ctx, parentDir); err != nil || kind != svn.NodeDir {
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: load parent %s", svn.ErrFSNotDirectory, parentDir)
		}
	}
	buffered := bufio.NewReader(input)
	version := 0
	var current *loadRevision
	revisionMap := make(map[svn.Revnum]svn.Revnum)
	for {
		record, err := readDumpRecord(buffered)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return abortLoad(ctx, current, err)
		}
		if value := record.headers["SVN-fs-dump-format-version"]; value != "" {
			version, err = strconv.Atoi(value)
			if err != nil || version < 2 || version > 3 {
				return fmt.Errorf("%w: unsupported dump format %q", svn.ErrMalformedFile, value)
			}
			continue
		}
		if uuid := record.headers["UUID"]; uuid != "" {
			if err := repository.applyLoadUUID(ctx, uuid, options.UUID, youngest); err != nil {
				return err
			}
			continue
		}
		if value := record.headers["Revision-number"]; value != "" {
			if current != nil {
				if err := repository.finishLoadRevision(ctx, current, revisionMap); err != nil {
					return err
				}
				youngest, err = repository.Youngest(ctx)
				if err != nil {
					return err
				}
			}
			number, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || number < 0 {
				return fmt.Errorf("%w: invalid revision number %q", svn.ErrMalformedFile, value)
			}
			properties, err := parseDumpProperties(record.content, false, nil)
			if err != nil {
				return err
			}
			current = &loadRevision{number: svn.Revnum(number), properties: properties}
			if number == 0 {
				if err := repository.replaceRevisionProperties(ctx, 0, properties); err != nil {
					return err
				}
				revisionMap[0] = 0
				continue
			}
			transaction, err := repository.fs.BeginTxn(ctx, youngest)
			if err != nil {
				return err
			}
			root, err := transaction.Root(ctx)
			if err != nil {
				_ = transaction.Abort(ctx)
				return err
			}
			current.transaction, current.root = transaction, root
			for name, value := range properties {
				if err := transaction.ChangeProperty(ctx, name, value); err != nil {
					return abortLoad(ctx, current, err)
				}
			}
			continue
		}
		if record.headers["Node-path"] != "" {
			if version == 0 || current == nil || current.transaction == nil {
				return abortLoad(ctx, current, fmt.Errorf("%w: node record outside a revision", svn.ErrMalformedFile))
			}
			if err := repository.applyLoadNode(ctx, current, record, parentDir, revisionMap); err != nil {
				return abortLoad(ctx, current, err)
			}
			continue
		}
		return abortLoad(ctx, current, fmt.Errorf("%w: unknown dump record", svn.ErrMalformedFile))
	}
	if current != nil && current.transaction != nil {
		return repository.finishLoadRevision(ctx, current, revisionMap)
	}
	return nil
}

func (repository *Repository) replaceRevisionProperties(ctx context.Context, revision svn.Revnum, properties svn.Props) error {
	current, err := repository.fs.RevisionProps(ctx, revision)
	if err != nil {
		return err
	}
	for name := range current {
		if _, exists := properties[name]; !exists {
			if err := repository.fs.ChangeRevisionProp(ctx, revision, name, nil, nil, true); err != nil {
				return err
			}
		}
	}
	for name, value := range properties {
		if !bytes.Equal(current[name], value) {
			if err := repository.fs.ChangeRevisionProp(ctx, revision, name, value, nil, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (repository *Repository) applyLoadNode(ctx context.Context, revision *loadRevision, record dumpRecord, parentDir string, revisions map[svn.Revnum]svn.Revnum) error {
	nodePath, err := joinLoadPath(parentDir, record.headers["Node-path"])
	if err != nil {
		return err
	}
	action := record.headers["Node-action"]
	if action == "" {
		return fmt.Errorf("%w: node action is missing", svn.ErrMalformedFile)
	}
	if action == "delete" || action == "replace" {
		if err := revision.root.Delete(ctx, nodePath); err != nil {
			return err
		}
	}
	kind := parseDumpNodeKind(record.headers["Node-kind"])
	if action == "add" || action == "replace" {
		copyRevision := svn.InvalidRevnum
		copyPathValue := record.headers["Node-copyfrom-path"]
		if value := record.headers["Node-copyfrom-rev"]; value != "" {
			if copyPathValue == "" {
				return fmt.Errorf("%w: copyfrom path is missing", svn.ErrMalformedFile)
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed < 0 {
				return fmt.Errorf("%w: invalid copyfrom revision %q", svn.ErrMalformedFile, value)
			}
			copyRevision = svn.Revnum(parsed)
			if mapped, found := revisions[copyRevision]; found {
				copyRevision = mapped
			}
		}
		if !copyRevision.IsValid() && copyPathValue != "" {
			return fmt.Errorf("%w: copyfrom revision is missing", svn.ErrMalformedFile)
		}
		if copyRevision.IsValid() {
			copyPath, err := joinLoadPath(parentDir, copyPathValue)
			if err != nil {
				return err
			}
			if err := revision.root.Copy(ctx, copyRevision, copyPath, nodePath); err != nil {
				return err
			}
		} else if kind == svn.NodeDir {
			if err := revision.root.MakeDir(ctx, nodePath); err != nil {
				return err
			}
		} else if kind == svn.NodeFile {
			if err := revision.root.MakeFile(ctx, nodePath); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("%w: node kind is missing", svn.ErrMalformedFile)
		}
	}
	if action != "add" && action != "replace" && action != "change" && action != "delete" {
		return fmt.Errorf("%w: invalid node action %q", svn.ErrMalformedFile, action)
	}
	if action == "delete" {
		return nil
	}
	propertyLength, err := dumpLength(record.headers, "Prop-content-length")
	if err != nil {
		return err
	}
	textLength, err := dumpLength(record.headers, "Text-content-length")
	if err != nil {
		return err
	}
	if propertyLength+textLength > len(record.content) {
		return svn.NewError(svn.ErrIncompleteData, "node content is truncated")
	}
	if propertyLength+textLength != len(record.content) {
		return fmt.Errorf("%w: node content lengths do not match Content-length", svn.ErrMalformedFile)
	}
	if propertyLength != 0 {
		current, err := revision.root.NodeProps(ctx, nodePath)
		if err != nil {
			return err
		}
		deltaProps := record.headers["Prop-delta"] == "true"
		desired, err := parseDumpProperties(record.content[:propertyLength], deltaProps, current)
		if err != nil {
			return err
		}
		for name := range current {
			if _, exists := desired[name]; !exists {
				if err := revision.root.ChangeNodeProp(ctx, nodePath, name, nil); err != nil {
					return err
				}
			}
		}
		for name, value := range desired {
			if !bytes.Equal(current[name], value) {
				if err := revision.root.ChangeNodeProp(ctx, nodePath, name, value); err != nil {
					return err
				}
			}
		}
	}
	if textLength != 0 || record.headers["Text-content-length"] != "" {
		text := record.content[propertyLength : propertyLength+textLength]
		if record.headers["Text-delta"] == "true" {
			decoder, err := delta.NewSvndiffReader(bytes.NewReader(text))
			if err != nil {
				return err
			}
			var baseChecksum *svn.Checksum
			for _, candidate := range []struct {
				name string
				kind svn.ChecksumKind
			}{{"Text-delta-base-md5", svn.ChecksumMD5}, {"Text-delta-base-sha1", svn.ChecksumSHA1}} {
				if value := record.headers[candidate.name]; value != "" {
					parsed, err := svn.ParseChecksumHex(candidate.kind, value)
					if err != nil {
						return err
					}
					baseChecksum = &parsed
					break
				}
			}
			handler, err := revision.root.ApplyTextDelta(ctx, nodePath, baseChecksum)
			if err != nil {
				return err
			}
			for {
				window, err := decoder.NextWindow()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
				if err := handler.Window(window); err != nil {
					return err
				}
			}
			if err := handler.Close(); err != nil {
				return err
			}
		} else if err := revision.root.ApplyText(ctx, nodePath, bytes.NewReader(text)); err != nil {
			return err
		}
		if record.headers["Text-content-md5"] != "" || record.headers["Text-content-sha1"] != "" {
			var actual bytes.Buffer
			if err := revision.root.FileContents(ctx, nodePath, &actual); err != nil {
				return err
			}
			for _, candidate := range []struct {
				name string
				kind svn.ChecksumKind
			}{{"Text-content-md5", svn.ChecksumMD5}, {"Text-content-sha1", svn.ChecksumSHA1}} {
				if expected := record.headers[candidate.name]; expected != "" && svn.Sum(candidate.kind, actual.Bytes()).Hex() != expected {
					return fmt.Errorf("%w: text content for %s", svn.ErrChecksumMismatch, nodePath)
				}
			}
		}
	}
	return nil
}

func (repository *Repository) finishLoadRevision(ctx context.Context, revision *loadRevision, revisions map[svn.Revnum]svn.Revnum) error {
	if revision.transaction == nil {
		return nil
	}
	committed, err := revision.transaction.Commit(ctx, nil, false)
	if err != nil {
		return err
	}
	revisions[revision.number] = committed
	if date, exists := revision.properties["svn:date"]; exists {
		if err := repository.fs.ChangeRevisionProp(ctx, committed, "svn:date", date, nil, true); err != nil {
			return err
		}
	}
	return nil
}

func abortLoad(ctx context.Context, revision *loadRevision, err error) error {
	if revision != nil && revision.transaction != nil {
		_ = revision.transaction.Abort(ctx)
	}
	return err
}

func readDumpRecord(reader *bufio.Reader) (dumpRecord, error) {
	headers := make(map[string]string)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return dumpRecord{}, err
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if len(headers) == 0 {
				if err != nil {
					return dumpRecord{}, err
				}
				continue
			}
			break
		}
		name, value, found := strings.Cut(line, ": ")
		if _, duplicate := headers[name]; !found || duplicate {
			return dumpRecord{}, fmt.Errorf("%w: invalid dump header %q", svn.ErrMalformedFile, line)
		}
		headers[name] = value
		if err != nil {
			return dumpRecord{}, svn.Wrap(svn.ErrIncompleteData, "dump header is truncated", err)
		}
	}
	length, err := dumpLength(headers, "Content-length")
	if err != nil {
		return dumpRecord{}, err
	}
	content := make([]byte, length)
	if _, err := io.ReadFull(reader, content); err != nil {
		return dumpRecord{}, svn.Wrap(svn.ErrIncompleteData, "dump content is truncated", err)
	}
	return dumpRecord{headers: headers, content: content}, nil
}

func joinLoadPath(parentDir, value string) (string, error) {
	if value == "" || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return "", fmt.Errorf("%w: non-canonical node path %q", svn.ErrMalformedFile, value)
	}
	joined := path.Join(parentDir, value)
	if parentDir != "/" && joined != parentDir && !strings.HasPrefix(joined, strings.TrimSuffix(parentDir, "/")+"/") {
		return "", fmt.Errorf("%w: node path escapes load parent", svn.ErrMalformedFile)
	}
	return joined, nil
}

func dumpLength(headers map[string]string, name string) (int, error) {
	value := headers[name]
	if value == "" {
		return 0, nil
	}
	length, err := strconv.ParseInt(value, 10, 64)
	if err != nil || length < 0 || length > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("%w: invalid %s %q", svn.ErrMalformedFile, name, value)
	}
	return int(length), nil
}

func parseDumpProperties(data []byte, incremental bool, base svn.Props) (svn.Props, error) {
	if !bytes.HasSuffix(data, []byte("PROPS-END\n")) {
		return nil, fmt.Errorf("%w: property block has no PROPS-END", svn.ErrMalformedFile)
	}
	converted := append([]byte(nil), data[:len(data)-len("PROPS-END\n")]...)
	converted = append(converted, "END\n"...)
	if incremental {
		return hashfile.ReadIncremental(bytes.NewReader(converted), base)
	}
	return hashfile.Read(bytes.NewReader(converted))
}

func parseDumpNodeKind(value string) svn.NodeKind {
	switch value {
	case "file":
		return svn.NodeFile
	case "dir":
		return svn.NodeDir
	default:
		return svn.NodeUnknown
	}
}
