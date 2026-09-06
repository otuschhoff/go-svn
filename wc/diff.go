package wc

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/svn"
)

type Difference struct {
	Path              string
	RelativePath      string
	Kind              svn.NodeKind
	Status            *Status
	Base              io.Reader
	Working           io.Reader
	BaseProperties    svn.Props
	WorkingProperties svn.Props
	CopyFromPath      string
	CopyFromRevision  svn.Revnum
}

type DiffCallback func(context.Context, *Difference) error

func (database *Database) Diff(ctx context.Context, targetPath string, depth svn.Depth, callback DiffCallback) error {
	return database.Status(ctx, targetPath, StatusOptions{Depth: depth, Verbose: true}, func(status *Status) error {
		if status.NodeStatus == StatusNormal && status.PropertyStatus == StatusNormal {
			return nil
		}
		if status.NodeStatus == StatusUnversioned || status.NodeStatus == StatusIgnored || status.NodeStatus == StatusExternal {
			return nil
		}
		info, err := database.Info(ctx, status.Path)
		if err != nil {
			return err
		}
		difference := &Difference{
			Path: info.Path, RelativePath: info.RelativePath, Kind: info.Kind, Status: status,
			BaseProperties: info.BaseProperties.Clone(), WorkingProperties: info.WorkingProperties.Clone(),
			CopyFromPath: info.CopyFromPath, CopyFromRevision: info.CopyFromRevision,
		}
		var base, working io.ReadCloser
		if info.Kind == svn.NodeFile || info.Kind == svn.NodeSymlink {
			baseChecksum := info.Checksum
			if info.Schedule == ScheduleReplace {
				baseRow, baseErr := database.readNode(ctx, "NODES_BASE", info.RelativePath)
				if baseErr != nil {
					return baseErr
				}
				if baseRow.checksum != "" {
					checksum, parseErr := svn.ParseChecksum(baseRow.checksum)
					if parseErr != nil {
						return parseErr
					}
					baseChecksum = &checksum
				}
			}
			if baseChecksum != nil && info.Schedule != ScheduleAdd {
				base, err = database.OpenPristine(ctx, *baseChecksum)
				if err != nil {
					return err
				}
				difference.Base = base
			} else {
				difference.Base = bytes.NewReader(nil)
			}
			if info.Schedule != ScheduleDelete {
				working, err = openWorkingContents(info)
				if err != nil {
					if base != nil {
						base.Close()
					}
					return err
				}
				difference.Working = working
			} else {
				difference.Working = bytes.NewReader(nil)
			}
		}
		err = callback(ctx, difference)
		if working != nil {
			if closeErr := working.Close(); err == nil {
				err = closeErr
			}
		}
		if base != nil {
			if closeErr := base.Close(); err == nil {
				err = closeErr
			}
		}
		return err
	})
}

func openWorkingContents(info *Info) (io.ReadCloser, error) {
	if len(info.WorkingProperties[props.Special]) != 0 {
		target, err := os.Readlink(info.Path)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(props.EncodeSpecial(target))), nil
	}
	return os.Open(filepath.Clean(info.Path))
}
