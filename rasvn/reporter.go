package rasvn

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type reporter struct {
	conn   *connection
	editor delta.Editor
	mu     sync.Mutex
	done   bool
}

func (session *Session) startReport(ctx context.Context, command string, editor delta.Editor, parameters ...Item) (_ ra.Reporter, resultErr error) {
	if editor == nil {
		return nil, fmt.Errorf("%w: nil editor", svn.ErrIncorrectParams)
	}
	conn := session.conn
	conn.mu.Lock()
	defer func() {
		resultErr = normalizeContextError(ctx, resultErr)
		if resultErr != nil {
			conn.mu.Unlock()
		}
	}()
	stop, err := conn.interruptOnCancel(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()
	if err := conn.write(List(Word(command), List(parameters...))); err != nil {
		return nil, err
	}
	if err := conn.authenticate(ctx); err != nil {
		return nil, err
	}
	return &reporter{conn: conn, editor: editor}, nil
}

func (reporter *reporter) SetPath(ctx context.Context, path string, revision svn.Revnum, depth svn.Depth, startEmpty bool, lockToken string) error {
	if !revision.IsValid() {
		return fmt.Errorf("%w: invalid report revision", svn.ErrIncorrectParams)
	}
	return reporter.send(ctx, "set-path", String([]byte(path)), Number(uint64(revision)), Word(boolWord(startEmpty)), optionalString(lockToken), Word(depth.String()))
}

func (reporter *reporter) LinkPath(ctx context.Context, path, url string, revision svn.Revnum, depth svn.Depth, startEmpty bool, lockToken string) error {
	if !revision.IsValid() {
		return fmt.Errorf("%w: invalid report revision", svn.ErrIncorrectParams)
	}
	return reporter.send(ctx, "link-path", String([]byte(path)), String([]byte(url)), Number(uint64(revision)), Word(boolWord(startEmpty)), optionalString(lockToken), Word(depth.String()))
}

func (reporter *reporter) DeletePath(ctx context.Context, path string) error {
	return reporter.send(ctx, "delete-path", String([]byte(path)))
}

func (reporter *reporter) FinishReport(ctx context.Context) (resultErr error) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.done {
		return fmt.Errorf("%w: report is already complete", svn.ErrIncorrectParams)
	}
	reporter.done = true
	defer reporter.conn.mu.Unlock()
	defer func() { resultErr = normalizeContextError(ctx, resultErr) }()
	stop, err := reporter.conn.interruptOnCancel(ctx)
	if err != nil {
		return err
	}
	defer stop()
	if err := reporter.conn.write(List(Word("finish-report"), List())); err != nil {
		return err
	}
	if err := reporter.conn.authenticate(ctx); err != nil {
		return err
	}
	editorErr := reporter.conn.driveEditor(ctx, reporter.editor, false)
	_, responseErr := reporter.conn.readResponse()
	if editorErr != nil {
		return errors.Join(editorErr, responseErr)
	}
	return responseErr
}

func (reporter *reporter) AbortReport(ctx context.Context) (resultErr error) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.done {
		return nil
	}
	reporter.done = true
	defer reporter.conn.mu.Unlock()
	defer func() { resultErr = normalizeContextError(ctx, resultErr) }()
	stop, err := reporter.conn.interruptOnCancel(ctx)
	if err != nil {
		return err
	}
	defer stop()
	return reporter.conn.write(List(Word("abort-report"), List()))
}

func (reporter *reporter) send(ctx context.Context, command string, parameters ...Item) (resultErr error) {
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.done {
		return fmt.Errorf("%w: report is already complete", svn.ErrIncorrectParams)
	}
	defer func() { resultErr = normalizeContextError(ctx, resultErr) }()
	stop, err := reporter.conn.interruptOnCancel(ctx)
	if err != nil {
		return err
	}
	defer stop()
	return reporter.conn.write(List(Word(command), List(parameters...)))
}

func optionalString(value string) Item {
	if value == "" {
		return List()
	}
	return List(String([]byte(value)))
}
