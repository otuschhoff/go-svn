package fsfs

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
)

type ID struct {
	Node     string
	Copy     string
	Revision svn.Revnum
	Item     int64
}

func ParseID(value string) (ID, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "r") {
		return ID{}, fmt.Errorf("%w: invalid node-revision ID %q", svn.ErrFSGeneral, value)
	}
	location := strings.Split(strings.TrimPrefix(parts[2], "r"), "/")
	if len(location) != 2 {
		return ID{}, fmt.Errorf("%w: invalid node-revision location %q", svn.ErrFSGeneral, value)
	}
	revision, err := strconv.ParseInt(location[0], 10, 64)
	if err != nil || revision < 0 {
		return ID{}, fmt.Errorf("%w: invalid node-revision revision %q", svn.ErrFSGeneral, value)
	}
	item, err := strconv.ParseInt(location[1], 10, 64)
	if err != nil || item < 0 {
		return ID{}, fmt.Errorf("%w: invalid node-revision item %q", svn.ErrFSGeneral, value)
	}
	return ID{Node: parts[0], Copy: parts[1], Revision: svn.Revnum(revision), Item: item}, nil
}

func (id ID) String() string {
	return fmt.Sprintf("%s.%s.r%d/%d", id.Node, id.Copy, id.Revision, id.Item)
}
