package fsfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	fsapi "github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
)

type FS struct {
	path           string
	format         Format
	config         Config
	minUnpackedRev svn.Revnum
	uuid           string
	instanceID     string
	indexCache     *logicalIndexCache
}

func init() {
	fsapi.RegisterOpener(func(ctx context.Context, path string) (fsapi.FS, error) {
		return Open(ctx, path)
	})
}

func Open(ctx context.Context, path string) (*FS, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	repositoryFormat, err := readNumber(filepath.Join(absolute, "format"))
	if err != nil {
		return nil, fmt.Errorf("%w: repository format: %v", svn.ErrFSNotFound, err)
	}
	if repositoryFormat != 5 && repositoryFormat != 3 {
		return nil, formatError("unsupported repository format %d", repositoryFormat)
	}
	fsType, err := os.ReadFile(filepath.Join(absolute, "db", "fs-type"))
	if err != nil {
		return nil, fmt.Errorf("%w: read fs-type: %v", svn.ErrFSNotFound, err)
	}
	if strings.TrimSpace(string(fsType)) != "fsfs" {
		return nil, formatError("filesystem type is not fsfs")
	}
	data, err := os.ReadFile(filepath.Join(absolute, "db", "format"))
	if os.IsNotExist(err) {
		data, err = []byte("1\n"), nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read FSFS format: %v", svn.ErrFSNotFound, err)
	}
	format, err := parseFormat(data)
	if err != nil {
		return nil, err
	}
	configuration, err := readConfig(filepath.Join(absolute, "db", "fsfs.conf"), format.Number)
	if err != nil {
		return nil, err
	}
	minimum := int64(0)
	if format.Layout == LayoutSharded {
		minimum, err = readNumber(filepath.Join(absolute, "db", "min-unpacked-rev"))
		if os.IsNotExist(err) && format.Number < 4 {
			minimum, err = 0, nil
		}
		if err != nil || minimum < 0 || minimum%format.ShardSize != 0 {
			return nil, fmt.Errorf("%w: invalid min-unpacked-rev", svn.ErrFSCorrupt)
		}
	}
	uuidData, err := os.ReadFile(filepath.Join(absolute, "db", "uuid"))
	if err != nil {
		return nil, fmt.Errorf("%w: read UUID: %v", svn.ErrFSCorrupt, err)
	}
	uuidLines := strings.Fields(string(uuidData))
	if len(uuidLines) == 0 {
		return nil, fmt.Errorf("%w: repository UUID is missing", svn.ErrFSCorrupt)
	}
	filesystem := &FS{path: absolute, format: format, config: configuration, minUnpackedRev: svn.Revnum(minimum), uuid: uuidLines[0], indexCache: newLogicalIndexCache(8)}
	if len(uuidLines) > 1 {
		filesystem.instanceID = uuidLines[1]
	}
	return filesystem, nil
}

func (filesystem *FS) Path() string                    { return filesystem.path }
func (filesystem *FS) Format() Format                  { return filesystem.format }
func (filesystem *FS) Config() Config                  { return filesystem.config }
func (filesystem *FS) MinUnpackedRevision() svn.Revnum { return filesystem.minUnpackedRev }
func (filesystem *FS) InstanceID() string              { return filesystem.instanceID }

func (filesystem *FS) UUID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return filesystem.uuid, nil
}

func (filesystem *FS) YoungestRevision(ctx context.Context) (svn.Revnum, error) {
	if err := ctx.Err(); err != nil {
		return svn.InvalidRevnum, err
	}
	number, err := readNumber(filepath.Join(filesystem.path, "db", "current"))
	if err != nil || number < 0 {
		return svn.InvalidRevnum, fmt.Errorf("%w: invalid current revision", svn.ErrFSCorrupt)
	}
	return svn.Revnum(number), nil
}

func readNumber(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty file")
	}
	return strconv.ParseInt(fields[0], 10, 64)
}
