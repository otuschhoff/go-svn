package radav

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/svn"
)

func (session *Session) Replay(ctx context.Context, revision, lowWaterMark svn.Revnum, sendDeltas bool, editor delta.Editor) error {
	if !revision.IsValid() || !lowWaterMark.IsValid() || editor == nil {
		return fmt.Errorf("%w: invalid replay arguments", svn.ErrIncorrectParams)
	}
	var body strings.Builder
	body.WriteString(`<S:replay-report xmlns:S="svn:">`)
	xmlText(&body, "S:revision", strconv.FormatInt(int64(revision), 10))
	xmlText(&body, "S:low-water-mark", strconv.FormatInt(int64(lowWaterMark), 10))
	if sendDeltas {
		xmlText(&body, "S:send-deltas", "1")
	} else {
		xmlText(&body, "S:send-deltas", "0")
	}
	body.WriteString(`</S:replay-report>`)
	err := session.report(ctx, session.url, []byte(body.String()), func(decoder *xml.Decoder) error {
		return session.driveEditor(ctx, decoder, editor)
	})
	if err != nil {
		if abortErr := editor.AbortEdit(ctx); abortErr != nil {
			return fmt.Errorf("%w; abort edit: %v", err, abortErr)
		}
	}
	return err
}

func (session *Session) ReplayRange(ctx context.Context, start, end, lowWaterMark svn.Revnum, sendDeltas bool, startRevision func(svn.Revnum, svn.Props) (delta.Editor, error), finishRevision func(svn.Revnum, svn.Props, delta.Editor) error) error {
	if !start.IsValid() || end < start || !lowWaterMark.IsValid() || startRevision == nil || finishRevision == nil {
		return fmt.Errorf("%w: invalid replay range", svn.ErrIncorrectParams)
	}
	for revision := start; revision <= end; revision++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		props, err := session.RevProps(ctx, revision)
		if err != nil {
			return err
		}
		editor, err := startRevision(revision, props)
		if err != nil {
			return err
		}
		if editor == nil {
			return fmt.Errorf("%w: nil replay editor", svn.ErrIncorrectParams)
		}
		if err := session.Replay(ctx, revision, lowWaterMark, sendDeltas, editor); err != nil {
			return err
		}
		if err := finishRevision(revision, props, editor); err != nil {
			return err
		}
	}
	return nil
}
