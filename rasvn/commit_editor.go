package rasvn

import (
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

const (
	txnClientCompatVersion = "1.14.5"
	txnUserAgent           = "go-svn/0"
)

type commitEditor struct {
	conn           *connection
	callback       func(*ra.CommitInfo) error
	repositoryURL  string
	svndiffVersion int
	mu             sync.Mutex
	nextToken      uint64
	done           bool
	response       chan error
}

type commitDir struct {
	editor *commitEditor
	token  string
}

type commitFile struct {
	editor *commitEditor
	token  string
}

type commitDelta struct {
	file    *commitFile
	handler delta.WindowHandler
	ctx     context.Context
	closed  bool
}

type deltaChunkWriter struct {
	ctx  context.Context
	file *commitFile
}

func (session *Session) startCommit(ctx context.Context, revprops svn.Props, lockTokens map[string]string, keepLocks bool, callback func(*ra.CommitInfo) error) (_ delta.Editor, resultErr error) {
	if revprops == nil {
		revprops = make(svn.Props)
	}
	hasCommitRevprops := session.capabilities[string(ra.CapabilityCommitRevprops)]
	logMessage, hasLogMessage := revprops["svn:log"]
	if !hasCommitRevprops {
		if !hasLogMessage {
			return nil, fmt.Errorf("%w: a log message is required by this server", svn.ErrBadPropertyValue)
		}
		if len(revprops) > 1 {
			return nil, fmt.Errorf("%w: server does not support commit revision properties", svn.ErrRANotImplemented)
		}
	}
	props := cloneProps(revprops)
	if hasCommitRevprops && session.capabilities[string(ra.CapabilityEphemeralTxnprops)] {
		props["svn:txn-client-compat-version"] = []byte(txnClientCompatVersion)
		props["svn:txn-user-agent"] = []byte(txnUserAgent)
	}
	if !hasLogMessage {
		logMessage = []byte{}
	}
	parameters := []Item{String(logMessage), lockTokenItems(lockTokens), Word(boolWord(keepLocks))}
	if hasCommitRevprops {
		parameters = append(parameters, propertyItems(props))
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
	if err := conn.write(List(Word("commit"), List(parameters...))); err != nil {
		return nil, err
	}
	if err := conn.authenticate(ctx); err != nil {
		return nil, err
	}
	if _, err := conn.readResponse(); err != nil {
		return nil, err
	}
	version := 0
	if session.capabilities["accepts-svndiff2"] {
		version = 2
	} else if session.capabilities["svndiff1"] {
		version = 1
	}
	editor := &commitEditor{conn: conn, callback: callback, repositoryURL: session.repositoryURL, svndiffVersion: version, response: make(chan error, 1)}
	go func() {
		_, err := conn.readResponse()
		editor.response <- err
	}()
	return editor, nil
}

func (editor *commitEditor) SetTargetRevision(ctx context.Context, revision svn.Revnum) error {
	return editor.send(ctx, "target-rev", Number(uint64(revision)))
}

func (editor *commitEditor) OpenRoot(ctx context.Context, revision svn.Revnum) (delta.DirEditor, error) {
	token := editor.token("d")
	if err := editor.send(ctx, "open-root", optionalRevision(revision), String([]byte(token))); err != nil {
		return nil, err
	}
	return &commitDir{editor: editor, token: token}, nil
}

func (editor *commitEditor) CloseEdit(ctx context.Context) (resultErr error) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if editor.done {
		return fmt.Errorf("%w: commit editor is already complete", svn.ErrIncorrectParams)
	}
	editor.done = true
	unlocked := false
	defer func() {
		if !unlocked {
			editor.conn.mu.Unlock()
		}
	}()
	if err := editor.finish(ctx, "close-edit"); err != nil {
		_ = editor.conn.write(List(Word("abort-edit"), List()))
		return normalizeContextError(ctx, err)
	}
	if err := editor.conn.authenticate(ctx); err != nil {
		return normalizeContextError(ctx, err)
	}
	item, err := editor.conn.reader.Decode()
	if err != nil {
		return normalizeContextError(ctx, err)
	}
	info, err := parseCommitInfo(item, editor.repositoryURL)
	if err != nil {
		return err
	}
	editor.conn.mu.Unlock()
	unlocked = true
	if editor.callback != nil {
		return editor.callback(info)
	}
	return nil
}

func (editor *commitEditor) AbortEdit(ctx context.Context) (resultErr error) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if editor.done {
		return nil
	}
	editor.done = true
	defer editor.conn.mu.Unlock()
	return editor.finish(ctx, "abort-edit")
}

func (editor *commitEditor) finish(ctx context.Context, command string) (resultErr error) {
	stop, err := editor.conn.interruptOnCancel(ctx)
	if err != nil {
		return err
	}
	defer stop()
	written := make(chan error, 1)
	go func() { written <- editor.conn.write(List(Word(command), List())) }()
	var writeErr, responseErr error
	for written != nil || editor.response != nil {
		select {
		case writeErr = <-written:
			written = nil
		case responseErr = <-editor.response:
			editor.response = nil
		}
	}
	return normalizeContextError(ctx, errors.Join(writeErr, responseErr))
}

func (editor *commitEditor) send(ctx context.Context, command string, parameters ...Item) error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if editor.done {
		return fmt.Errorf("%w: commit editor is already complete", svn.ErrIncorrectParams)
	}
	select {
	case err := <-editor.response:
		editor.done = true
		_ = editor.conn.write(List(Word("abort-edit"), List()))
		editor.conn.mu.Unlock()
		if err == nil {
			return malformed("successful edit response arrived before close-edit")
		}
		return err
	default:
	}
	stop, err := editor.conn.interruptOnCancel(ctx)
	if err != nil {
		return err
	}
	defer stop()
	return normalizeContextError(ctx, editor.conn.write(List(Word(command), List(parameters...))))
}

func (editor *commitEditor) token(prefix string) string {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	token := fmt.Sprintf("%s%d", prefix, editor.nextToken)
	editor.nextToken++
	return token
}

func (directory *commitDir) DeleteEntry(ctx context.Context, path string, revision svn.Revnum) error {
	return directory.editor.send(ctx, "delete-entry", String([]byte(path)), optionalRevision(revision), String([]byte(directory.token)))
}

func (directory *commitDir) AddDirectory(ctx context.Context, path string, source *delta.CopySource) (delta.DirEditor, error) {
	token := directory.editor.token("d")
	if err := directory.editor.send(ctx, "add-dir", String([]byte(path)), String([]byte(directory.token)), String([]byte(token)), copySourceItem(source)); err != nil {
		return nil, err
	}
	return &commitDir{editor: directory.editor, token: token}, nil
}

func (directory *commitDir) OpenDirectory(ctx context.Context, path string, revision svn.Revnum) (delta.DirEditor, error) {
	token := directory.editor.token("d")
	if err := directory.editor.send(ctx, "open-dir", String([]byte(path)), String([]byte(directory.token)), String([]byte(token)), optionalRevision(revision)); err != nil {
		return nil, err
	}
	return &commitDir{editor: directory.editor, token: token}, nil
}

func (directory *commitDir) ChangeProp(ctx context.Context, name string, value []byte) error {
	return directory.editor.send(ctx, "change-dir-prop", String([]byte(directory.token)), String([]byte(name)), optionalBytes(value))
}

func (directory *commitDir) AbsentDirectory(ctx context.Context, path string) error {
	return directory.editor.send(ctx, "absent-dir", String([]byte(path)), String([]byte(directory.token)))
}

func (directory *commitDir) AddFile(ctx context.Context, path string, source *delta.CopySource) (delta.FileEditor, error) {
	token := directory.editor.token("c")
	if err := directory.editor.send(ctx, "add-file", String([]byte(path)), String([]byte(directory.token)), String([]byte(token)), copySourceItem(source)); err != nil {
		return nil, err
	}
	return &commitFile{editor: directory.editor, token: token}, nil
}

func (directory *commitDir) OpenFile(ctx context.Context, path string, revision svn.Revnum) (delta.FileEditor, error) {
	token := directory.editor.token("c")
	if err := directory.editor.send(ctx, "open-file", String([]byte(path)), String([]byte(directory.token)), String([]byte(token)), optionalRevision(revision)); err != nil {
		return nil, err
	}
	return &commitFile{editor: directory.editor, token: token}, nil
}

func (directory *commitDir) AbsentFile(ctx context.Context, path string) error {
	return directory.editor.send(ctx, "absent-file", String([]byte(path)), String([]byte(directory.token)))
}

func (directory *commitDir) Close(ctx context.Context) error {
	return directory.editor.send(ctx, "close-dir", String([]byte(directory.token)))
}

func (file *commitFile) ApplyTextDelta(ctx context.Context, base *svn.Checksum) (delta.WindowHandler, error) {
	if err := file.editor.send(ctx, "apply-textdelta", String([]byte(file.token)), optionalChecksumItem(base)); err != nil {
		return nil, err
	}
	handler := delta.NewSvndiffWriter(deltaChunkWriter{ctx: ctx, file: file}, file.editor.svndiffVersion, zlib.DefaultCompression)
	return &commitDelta{file: file, handler: handler, ctx: ctx}, nil
}

func (file *commitFile) ChangeProp(ctx context.Context, name string, value []byte) error {
	return file.editor.send(ctx, "change-file-prop", String([]byte(file.token)), String([]byte(name)), optionalBytes(value))
}

func (file *commitFile) Close(ctx context.Context, checksum *svn.Checksum) error {
	return file.editor.send(ctx, "close-file", String([]byte(file.token)), optionalChecksumItem(checksum))
}

func (writer deltaChunkWriter) Write(data []byte) (int, error) {
	if err := writer.file.editor.send(writer.ctx, "textdelta-chunk", String([]byte(writer.file.token)), String(append([]byte(nil), data...))); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (deltaStream *commitDelta) Window(window *delta.Window) error {
	if deltaStream.closed {
		return fmt.Errorf("text delta is closed")
	}
	return deltaStream.handler.Window(window)
}

func (deltaStream *commitDelta) Close() error {
	if deltaStream.closed {
		return fmt.Errorf("text delta is already closed")
	}
	deltaStream.closed = true
	if err := deltaStream.handler.Close(); err != nil {
		return err
	}
	return deltaStream.file.editor.send(deltaStream.ctx, "textdelta-end", String([]byte(deltaStream.file.token)))
}

func parseCommitInfo(item Item, repositoryURL string) (*ra.CommitInfo, error) {
	if item.Kind != ListKind || len(item.List) < 3 || item.List[0].Kind != NumberKind {
		return nil, malformed("invalid commit info")
	}
	info := &ra.CommitInfo{Revision: svn.Revnum(item.List[0].Number), ReposRoot: repositoryURL}
	date, err := optionalItemString(item.List[1])
	if err != nil {
		return nil, malformedCause(err, "invalid commit date")
	}
	if date != "" {
		info.Date, err = time.Parse(time.RFC3339Nano, date)
		if err != nil {
			return nil, malformedCause(err, "invalid commit date")
		}
	}
	info.Author, err = optionalItemString(item.List[2])
	if err != nil {
		return nil, malformedCause(err, "invalid commit author")
	}
	if len(item.List) > 3 {
		info.PostCommitError, err = optionalItemString(item.List[3])
		if err != nil {
			return nil, malformedCause(err, "invalid post-commit error")
		}
	}
	return info, nil
}

func optionalItemString(item Item) (string, error) {
	if item.Kind != ListKind || len(item.List) > 1 || len(item.List) == 1 && item.List[0].Kind != StringKind {
		return "", errors.New("invalid optional string")
	}
	if len(item.List) == 0 {
		return "", nil
	}
	return string(item.List[0].String), nil
}

func propertyItems(props svn.Props) Item {
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]Item, 0, len(names))
	for _, name := range names {
		items = append(items, List(String([]byte(name)), String(props[name])))
	}
	return List(items...)
}

func lockTokenItems(tokens map[string]string) Item {
	paths := make([]string, 0, len(tokens))
	for path := range tokens {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	items := make([]Item, 0, len(paths))
	for _, path := range paths {
		items = append(items, List(String([]byte(path)), String([]byte(tokens[path]))))
	}
	return List(items...)
}

func copySourceItem(source *delta.CopySource) Item {
	if source == nil {
		return List()
	}
	return List(String([]byte(source.Path)), Number(uint64(source.Rev)))
}

func optionalBytes(value []byte) Item {
	if value == nil {
		return List()
	}
	return List(String(value))
}

func optionalText(value string) Item {
	if value == "" {
		return List()
	}
	return List(String([]byte(value)))
}

func optionalChecksumItem(checksum *svn.Checksum) Item {
	if checksum == nil {
		return List()
	}
	return List(String([]byte(checksum.Hex())))
}

func cloneProps(props svn.Props) svn.Props {
	clone := make(svn.Props, len(props)+2)
	for name, value := range props {
		clone[name] = append([]byte(nil), value...)
	}
	return clone
}
