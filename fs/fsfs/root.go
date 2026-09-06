package fsfs

import (
	"context"
	"fmt"

	fsapi "github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
)

type Root struct {
	filesystem *FS
	revision   svn.Revnum
	root       NodeRevision
}

func (filesystem *FS) RevisionRoot(ctx context.Context, revision svn.Revnum) (fsapi.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	youngest, err := filesystem.YoungestRevision(ctx)
	if err != nil {
		return nil, err
	}
	if revision < 0 || revision > youngest {
		return nil, fmt.Errorf("%w: revision %d", svn.ErrFSNoSuchRevision, revision)
	}
	file, err := filesystem.openRevision(revision)
	if err != nil {
		return nil, err
	}
	var offset int64
	if filesystem.format.Addressing == AddressingLogical {
		offset, err = file.itemOffset(revision, 2)
	} else {
		offset, _, err = file.physicalTrailer()
	}
	if err != nil {
		return nil, err
	}
	if offset < 0 || offset >= file.size {
		return nil, fmt.Errorf("%w: invalid root node offset", svn.ErrFSCorrupt)
	}
	data, err := file.recordData(offset)
	if err != nil {
		return nil, err
	}
	node, err := parseNodeRevision(data)
	if err != nil {
		return nil, err
	}
	if node.Kind != svn.NodeDir || node.ID.Revision != revision {
		return nil, fmt.Errorf("%w: revision root is invalid", svn.ErrFSCorrupt)
	}
	return &Root{filesystem: filesystem, revision: revision, root: node}, nil
}

func (root *Root) Revision() svn.Revnum { return root.revision }
