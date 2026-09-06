package repos

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
)

type DumpOptions struct {
	StartRevision svn.Revnum
	EndRevision   svn.Revnum
	Incremental   bool
	UseDeltas     bool
}

func Dump(ctx context.Context, repository *Repository, writer io.Writer, options DumpOptions) error {
	return repository.Dump(ctx, writer, options)
}

func (repository *Repository) Dump(ctx context.Context, writer io.Writer, options DumpOptions) error {
	youngest, err := repository.Youngest(ctx)
	if err != nil {
		return err
	}
	start, end := options.StartRevision, options.EndRevision
	if start == 0 && end == 0 {
		end = youngest
	}
	if start < 0 {
		start = 0
	}
	if end < 0 {
		end = youngest
	}
	if start > end || end > youngest {
		return fmt.Errorf("%w: invalid dump revision range %d:%d", svn.ErrIncorrectParams, start, end)
	}
	version := 2
	if options.UseDeltas {
		version = 3
	}
	uuid, err := repository.UUID(ctx)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "SVN-fs-dump-format-version: %d\n\nUUID: %s\n\n", version, uuid); err != nil {
		return err
	}
	for revision := start; revision <= end; revision++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		properties, err := repository.RevisionProps(ctx, revision)
		if err != nil {
			return err
		}
		propertyData, err := encodeDumpProperties(properties)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(writer, "Revision-number: %d\nProp-content-length: %d\nContent-length: %d\n\n", revision, len(propertyData), len(propertyData)); err != nil {
			return err
		}
		if _, err := writer.Write(propertyData); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return err
		}
		if revision == 0 {
			continue
		}
		rootValue, err := repository.fs.RevisionRoot(ctx, revision)
		if err != nil {
			return err
		}
		var changes map[string]fs.PathChange
		if revision == start && start > 0 && !options.Incremental {
			changes = make(map[string]fs.PathChange)
			if err := collectDumpSnapshot(ctx, rootValue, "/", changes); err != nil {
				return err
			}
		} else {
			changes, err = rootValue.PathsChanged(ctx)
			if err != nil {
				return err
			}
		}
		paths := make([]string, 0, len(changes))
		for nodePath := range changes {
			paths = append(paths, nodePath)
		}
		sort.Strings(paths)
		for _, nodePath := range paths {
			if err := repository.dumpNode(ctx, writer, revision, rootValue, changes[nodePath], options.UseDeltas); err != nil {
				return err
			}
		}
	}
	return nil
}

func collectDumpSnapshot(ctx context.Context, root fs.Root, directoryPath string, changes map[string]fs.PathChange) error {
	entries, err := root.DirEntries(ctx, directoryPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		nodePath := path.Join(directoryPath, entry.Name)
		changes[nodePath] = fs.PathChange{Path: nodePath, Kind: fs.ChangeAdd, NodeKind: entry.Kind, TextModified: entry.Kind == svn.NodeFile, PropsModified: true, CopyFromRev: svn.InvalidRevnum}
		if entry.Kind == svn.NodeDir {
			if err := collectDumpSnapshot(ctx, root, nodePath, changes); err != nil {
				return err
			}
		}
	}
	return nil
}

func (repository *Repository) dumpNode(ctx context.Context, writer io.Writer, revision svn.Revnum, root fs.Root, change fs.PathChange, useDeltas bool) error {
	nodePath := "/" + strings.TrimPrefix(change.Path, "/")
	if _, err := fmt.Fprintf(writer, "Node-path: %s\n", strings.TrimPrefix(nodePath, "/")); err != nil {
		return err
	}
	if change.Kind != fs.ChangeDelete {
		if _, err := fmt.Fprintf(writer, "Node-kind: %s\n", change.NodeKind); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(writer, "Node-action: %s\n", dumpAction(change.Kind)); err != nil {
		return err
	}
	if change.CopyFromRev.IsValid() && change.CopyFromPath != "" {
		if _, err := fmt.Fprintf(writer, "Node-copyfrom-rev: %d\nNode-copyfrom-path: %s\n", change.CopyFromRev, strings.TrimPrefix(change.CopyFromPath, "/")); err != nil {
			return err
		}
	}
	if change.Kind == fs.ChangeDelete {
		_, err := io.WriteString(writer, "\n\n")
		return err
	}
	includeProperties := change.PropsModified || (change.Kind == fs.ChangeAdd || change.Kind == fs.ChangeReplace) && !change.CopyFromRev.IsValid()
	includeText := change.NodeKind == svn.NodeFile && (change.TextModified || (change.Kind == fs.ChangeAdd || change.Kind == fs.ChangeReplace) && !change.CopyFromRev.IsValid())
	var propertyData, textData []byte
	var err error
	if includeProperties {
		properties, readErr := root.NodeProps(ctx, nodePath)
		if readErr != nil {
			return readErr
		}
		propertyData, err = encodeDumpProperties(properties)
		if err != nil {
			return err
		}
	}
	if change.NodeKind == svn.NodeFile {
		var content bytes.Buffer
		if err := root.FileContents(ctx, nodePath, &content); err != nil {
			return err
		}
		fulltext := content.Bytes()
		if includeText {
			textData = fulltext
			if useDeltas {
				var sourceRoot fs.Root
				sourcePath := nodePath
				sourceRevision := revision - 1
				if change.CopyFromRev.IsValid() {
					sourceRevision, sourcePath = change.CopyFromRev, change.CopyFromPath
				}
				if change.Kind == fs.ChangeModify || change.CopyFromRev.IsValid() {
					sourceRoot, err = repository.fs.RevisionRoot(ctx, sourceRevision)
					if err != nil {
						return err
					}
					baseMD5, err := sourceRoot.FileChecksum(ctx, sourcePath, svn.ChecksumMD5)
					if err != nil {
						return err
					}
					baseSHA1, err := sourceRoot.FileChecksum(ctx, sourcePath, svn.ChecksumSHA1)
					if err != nil {
						return err
					}
					fmt.Fprintf(writer, "Text-delta-base-md5: %s\nText-delta-base-sha1: %s\n", baseMD5.Hex(), baseSHA1.Hex())
				}
				var encoded bytes.Buffer
				encoder, err := delta.NewEncoder(&encoded, 1)
				if err != nil {
					return err
				}
				windows, err := root.GetFileDelta(ctx, sourceRoot, sourcePath, nodePath)
				if err != nil {
					return err
				}
				for err == nil {
					var window *delta.Window
					window, err = windows.NextWindow()
					if err == nil {
						err = encoder.WriteWindow(*window)
					}
				}
				if err != io.EOF {
					return err
				}
				if err := encoder.Close(); err != nil {
					return err
				}
				textData = encoded.Bytes()
			}
			fmt.Fprintf(writer, "Text-content-md5: %s\nText-content-sha1: %s\n", svn.Sum(svn.ChecksumMD5, fulltext).Hex(), svn.Sum(svn.ChecksumSHA1, fulltext).Hex())
			if useDeltas {
				fmt.Fprintln(writer, "Text-delta: true")
			}
		} else if change.CopyFromRev.IsValid() {
			fmt.Fprintf(writer, "Text-copy-source-md5: %s\nText-copy-source-sha1: %s\n", svn.Sum(svn.ChecksumMD5, fulltext).Hex(), svn.Sum(svn.ChecksumSHA1, fulltext).Hex())
		}
	}
	if includeProperties {
		fmt.Fprintf(writer, "Prop-content-length: %d\n", len(propertyData))
	}
	if includeText {
		fmt.Fprintf(writer, "Text-content-length: %d\n", len(textData))
	}
	if len(propertyData)+len(textData) != 0 {
		fmt.Fprintf(writer, "Content-length: %d\n", len(propertyData)+len(textData))
	}
	if _, err := io.WriteString(writer, "\n"); err != nil {
		return err
	}
	if _, err := writer.Write(propertyData); err != nil {
		return err
	}
	if _, err := writer.Write(textData); err != nil {
		return err
	}
	_, err = io.WriteString(writer, "\n\n")
	return err
}

func dumpAction(kind fs.ChangeKind) string {
	if kind == fs.ChangeModify {
		return "change"
	}
	return string(kind)
}

func encodeDumpProperties(properties svn.Props) ([]byte, error) {
	var data bytes.Buffer
	keys := make([]string, 0, len(properties))
	for name := range properties {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		value := properties[name]
		if _, err := fmt.Fprintf(&data, "K %d\n%s\nV %d\n", len(name), name, len(value)); err != nil {
			return nil, err
		}
		if _, err := data.Write(value); err != nil {
			return nil, err
		}
		data.WriteByte('\n')
	}
	data.WriteString("PROPS-END\n")
	return data.Bytes(), nil
}
