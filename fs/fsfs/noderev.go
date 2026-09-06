package fsfs

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
)

type Representation struct {
	Revision     svn.Revnum
	Item         int64
	Length       int64
	ExpandedSize int64
	MD5          string
	SHA1         string
	Uniquifier   string
}

type NodeRevision struct {
	ID                ID
	Kind              svn.NodeKind
	Predecessor       *ID
	Count             int64
	Text              *Representation
	Properties        *Representation
	CreatedPath       string
	CopyRootPath      string
	CopyRootRev       svn.Revnum
	CopyFromPath      string
	CopyFromRev       svn.Revnum
	MergeinfoCount    int64
	MergeinfoHere     bool
	HasMergeinfoCount bool
}

func parseNodeRevision(data []byte) (NodeRevision, error) {
	fields := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ": ")
		if !ok || fields[name] != "" {
			return NodeRevision{}, fmt.Errorf("%w: invalid node-revision field %q", svn.ErrFSCorrupt, line)
		}
		fields[name] = value
	}
	if err := scanner.Err(); err != nil {
		return NodeRevision{}, err
	}
	id, err := ParseID(fields["id"])
	if err != nil {
		return NodeRevision{}, err
	}
	node := NodeRevision{ID: id, CreatedPath: fields["cpath"], CopyRootRev: svn.InvalidRevnum, CopyFromRev: svn.InvalidRevnum}
	if node.CreatedPath == "" || !canonicalAbsolutePath(node.CreatedPath) {
		return NodeRevision{}, fmt.Errorf("%w: invalid created path %q", svn.ErrFSCorrupt, node.CreatedPath)
	}
	switch fields["type"] {
	case "file":
		node.Kind = svn.NodeFile
	case "dir":
		node.Kind = svn.NodeDir
	default:
		return NodeRevision{}, fmt.Errorf("%w: invalid node kind %q", svn.ErrFSCorrupt, fields["type"])
	}
	if value := fields["pred"]; value != "" {
		predecessor, parseErr := ParseID(value)
		if parseErr != nil {
			return NodeRevision{}, parseErr
		}
		node.Predecessor = &predecessor
	}
	if value := fields["count"]; value != "" {
		node.Count, err = strconv.ParseInt(value, 10, 64)
		if err != nil || node.Count < 0 {
			return NodeRevision{}, fmt.Errorf("%w: invalid predecessor count %q", svn.ErrFSCorrupt, value)
		}
	}
	if value := fields["text"]; value != "" {
		node.Text, err = parseRepresentation(value)
		if err != nil {
			return NodeRevision{}, err
		}
	}
	if value := fields["props"]; value != "" {
		node.Properties, err = parseRepresentation(value)
		if err != nil {
			return NodeRevision{}, err
		}
	}
	if value := fields["copyroot"]; value != "" {
		node.CopyRootRev, node.CopyRootPath, err = parseRevisionPath(value)
		if err != nil {
			return NodeRevision{}, err
		}
	}
	if value := fields["copyfrom"]; value != "" {
		node.CopyFromRev, node.CopyFromPath, err = parseRevisionPath(value)
		if err != nil {
			return NodeRevision{}, err
		}
	}
	if value := fields["minfo-cnt"]; value != "" {
		node.HasMergeinfoCount = true
		node.MergeinfoCount, err = strconv.ParseInt(value, 10, 64)
		if err != nil || node.MergeinfoCount < 0 {
			return NodeRevision{}, fmt.Errorf("%w: invalid mergeinfo count %q", svn.ErrFSCorrupt, value)
		}
	}
	_, node.MergeinfoHere = fields["minfo-here"]
	return node, nil
}

func parseRepresentation(value string) (*Representation, error) {
	fields := strings.Fields(value)
	if len(fields) < 5 || len(fields) > 7 {
		return nil, fmt.Errorf("%w: invalid representation %q", svn.ErrFSCorrupt, value)
	}
	numbers := make([]int64, 4)
	for index := range numbers {
		parsed, err := strconv.ParseInt(fields[index], 10, 64)
		if err != nil || parsed < 0 {
			return nil, fmt.Errorf("%w: invalid representation %q", svn.ErrFSCorrupt, value)
		}
		numbers[index] = parsed
	}
	representation := &Representation{Revision: svn.Revnum(numbers[0]), Item: numbers[1], Length: numbers[2], ExpandedSize: numbers[3], MD5: fields[4]}
	if !validHexDigest(representation.MD5, 16) {
		return nil, fmt.Errorf("%w: invalid representation MD5 %q", svn.ErrFSCorrupt, representation.MD5)
	}
	if len(fields) > 5 && fields[5] != "-" {
		representation.SHA1 = fields[5]
		if !validHexDigest(representation.SHA1, 20) {
			return nil, fmt.Errorf("%w: invalid representation SHA1 %q", svn.ErrFSCorrupt, representation.SHA1)
		}
	}
	if len(fields) > 6 && fields[6] != "-" {
		representation.Uniquifier = fields[6]
	}
	return representation, nil
}

func parseRevisionPath(value string) (svn.Revnum, string, error) {
	revisionText, path, ok := strings.Cut(value, " ")
	if !ok {
		return svn.InvalidRevnum, "", fmt.Errorf("%w: invalid revision path %q", svn.ErrFSCorrupt, value)
	}
	revision, err := strconv.ParseInt(revisionText, 10, 64)
	if err != nil || revision < 0 || !canonicalAbsolutePath(path) {
		return svn.InvalidRevnum, "", fmt.Errorf("%w: invalid revision path %q", svn.ErrFSCorrupt, value)
	}
	return svn.Revnum(revision), path, nil
}

func validHexDigest(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size
}

func canonicalAbsolutePath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.Contains(value, "//") && value == path.Clean(value)
}
