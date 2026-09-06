package client

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/otuschhoff/go-svn/delta"
	internaldiff "github.com/otuschhoff/go-svn/internal/diff"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
)

type BlameOptions struct {
	InfoOptions
	Start         svn.Revision
	End           svn.Revision
	IncludeMerged bool
	Force         bool
	DiffOptions   internaldiff.Options
}

type BlameLine struct {
	LineNumber int
	Revision   svn.Revnum
	Author     string
	Date       time.Time
	Line       string
	Merged     bool
}

type blameRevision struct {
	revision svn.Revnum
	author   string
	date     time.Time
	merged   bool
}

func (client *Client) Blame(ctx context.Context, targetValue string, options BlameOptions, callback func(BlameLine) error) error {
	if callback == nil {
		return fmt.Errorf("%w: nil blame callback", svn.ErrIncorrectParams)
	}
	target, err := client.resolveTarget(ctx, targetValue, options.InfoOptions)
	if err != nil {
		return err
	}
	defer target.close()
	start := svn.Revnum(1)
	if options.Start.Kind != svn.RevisionUnspecified {
		start, err = resolveRARevision(ctx, target.session, options.Start)
		if err != nil {
			return err
		}
	}
	end := target.revision
	if options.End.Kind != svn.RevisionUnspecified {
		end, err = resolveRARevision(ctx, target.session, options.End)
		if err != nil {
			return err
		}
	}
	if start > end {
		return fmt.Errorf("%w: blame start %d exceeds end %d", svn.ErrClientRevisionRange, start, end)
	}
	current := []byte(nil)
	origins := []int(nil)
	metadata := make(map[int]blameRevision)
	properties := make(svn.Props)
	err = target.session.GetFileRevs(ctx, target.path, start, end, options.IncludeMerged, func(_ context.Context, revision ra.FileRevision) error {
		for name, value := range revision.PropDiffs {
			if value == nil {
				delete(properties, name)
			} else {
				properties[name] = append([]byte(nil), value...)
			}
		}
		if !options.Force && props.IsBinaryMIMEType(string(properties[props.MIMEType])) {
			return fmt.Errorf("%w: %s", svn.ErrClientIsBinaryFile, targetValue)
		}
		next := current
		if revision.Delta != nil {
			var output bytes.Buffer
			if _, err := delta.Apply(bytes.NewReader(current), &output, revision.Delta, nil); err != nil {
				return err
			}
			next = output.Bytes()
		}
		origin := int(revision.Revision)
		origins = internaldiff.CarryOrigins(current, next, origins, origin, options.DiffOptions)
		current = append(current[:0], next...)
		entry := blameRevision{revision: revision.Revision, author: string(revision.RevProps["svn:author"]), merged: revision.Merged}
		if value := string(revision.RevProps["svn:date"]); value != "" {
			entry.date, _ = time.Parse(time.RFC3339Nano, value)
		}
		metadata[origin] = entry
		client.notify(notify.Notify{Action: notify.ActionBlameRevision, Path: targetValue, Revision: revision.Revision})
		return nil
	})
	if err != nil {
		return err
	}
	lines := internaldiff.Lines(current)
	for index, line := range lines {
		origin := 0
		if index < len(origins) {
			origin = origins[index]
		}
		entry := metadata[origin]
		if err := callback(BlameLine{LineNumber: index + 1, Revision: entry.revision, Author: entry.author, Date: entry.date, Line: line, Merged: entry.merged}); err != nil {
			return err
		}
	}
	return nil
}
