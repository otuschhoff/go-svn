package fsfs

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"

	"github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
)

func parseChanges(data []byte) (map[string]fs.PathChange, error) {
	result := make(map[string]fs.PathChange)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return nil, fmt.Errorf("%w: invalid changed path %q", svn.ErrFSCorrupt, line)
		}
		action := fields[1]
		kind := svn.NodeUnknown
		if base, suffix, found := strings.Cut(action, "-"); found {
			action = base
			switch suffix {
			case "file":
				kind = svn.NodeFile
			case "dir":
				kind = svn.NodeDir
			default:
				return nil, fmt.Errorf("%w: invalid changed path kind %q", svn.ErrFSCorrupt, suffix)
			}
		}
		changeKind := fs.ChangeKind(action)
		if changeKind != fs.ChangeAdd && changeKind != fs.ChangeDelete && changeKind != fs.ChangeModify && changeKind != fs.ChangeReplace && changeKind != fs.ChangeReset {
			return nil, fmt.Errorf("%w: invalid change action %q", svn.ErrFSCorrupt, action)
		}
		pathIndex := 4
		if len(fields) >= 6 && (fields[4] == "true" || fields[4] == "false") {
			pathIndex = 5
		}
		if len(fields) <= pathIndex || (fields[2] != "true" && fields[2] != "false") || (fields[3] != "true" && fields[3] != "false") {
			return nil, fmt.Errorf("%w: invalid changed path flags %q", svn.ErrFSCorrupt, line)
		}
		change := fs.PathChange{Path: fields[pathIndex], Kind: changeKind, NodeKind: kind, TextModified: fields[2] == "true", PropsModified: fields[3] == "true", CopyFromRev: svn.InvalidRevnum}
		if !scanner.Scan() {
			return nil, fmt.Errorf("%w: changed path copyfrom line is missing", svn.ErrFSCorrupt)
		}
		copyfrom := scanner.Text()
		if copyfrom != "" {
			revision, path, err := parseRevisionPath(copyfrom)
			if err != nil {
				return nil, err
			}
			change.CopyFromRev, change.CopyFromPath = revision, path
		}
		result[change.Path] = change
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
