package rasvn

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type editorDriver struct {
	conn   *connection
	editor delta.Editor
	dirs   map[string]delta.DirEditor
	files  map[string]delta.FileEditor
	deltas map[string]*deltaStream
}

type deltaStream struct {
	writer *io.PipeWriter
	done   chan error
}

func (conn *connection) driveEditor(ctx context.Context, editor delta.Editor, replay bool) error {
	if editor == nil {
		return fmt.Errorf("%w: nil editor", svn.ErrIncorrectParams)
	}
	driver := &editorDriver{conn: conn, editor: editor, dirs: make(map[string]delta.DirEditor), files: make(map[string]delta.FileEditor), deltas: make(map[string]*deltaStream)}
	for {
		item, err := conn.reader.Decode()
		if err != nil {
			_ = editor.AbortEdit(ctx)
			return err
		}
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != WordKind || item.List[1].Kind != ListKind {
			_ = editor.AbortEdit(ctx)
			return malformed("invalid editor command")
		}
		command := item.List[0].Word
		if command == "finish-replay" && replay {
			return nil
		}
		finished, err := driver.dispatch(ctx, command, item.List[1].List)
		if err != nil {
			writeErr := conn.writeFailure(err)
			driver.abortDeltas(err)
			_ = editor.AbortEdit(ctx)
			if writeErr != nil {
				return writeErr
			}
			if command == "close-edit" {
				return err
			}
			drainErr := conn.drainEditor(replay)
			if drainErr != nil {
				return drainErr
			}
			return err
		}
		if finished {
			return nil
		}
	}
}

func (driver *editorDriver) dispatch(ctx context.Context, command string, params []Item) (bool, error) {
	switch command {
	case "target-rev":
		if len(params) != 1 || params[0].Kind != NumberKind {
			return false, malformed("invalid target-rev")
		}
		return false, driver.editor.SetTargetRevision(ctx, svn.Revnum(params[0].Number))
	case "open-root":
		if len(params) != 2 || params[0].Kind != ListKind || params[1].Kind != StringKind {
			return false, malformed("invalid open-root")
		}
		revision, err := optionalRevisionValue(params[0])
		if err != nil {
			return false, err
		}
		directory, err := driver.editor.OpenRoot(ctx, revision)
		if err == nil {
			driver.dirs[string(params[1].String)] = directory
		}
		return false, err
	case "delete-entry":
		if len(params) != 3 || params[0].Kind != StringKind || params[1].Kind != ListKind || params[2].Kind != StringKind {
			return false, malformed("invalid delete-entry")
		}
		revision, err := optionalRevisionValue(params[1])
		if err != nil {
			return false, err
		}
		parent, err := driver.directory(params[2])
		if err != nil {
			return false, err
		}
		return false, parent.DeleteEntry(ctx, string(params[0].String), revision)
	case "add-dir", "add-file":
		return false, driver.addNode(ctx, command, params)
	case "open-dir", "open-file":
		return false, driver.openNode(ctx, command, params)
	case "change-dir-prop", "change-file-prop":
		return false, driver.changeProp(ctx, command, params)
	case "close-dir":
		if len(params) != 1 || params[0].Kind != StringKind {
			return false, malformed("invalid close-dir")
		}
		token := string(params[0].String)
		directory, err := driver.directory(params[0])
		if err == nil {
			err = directory.Close(ctx)
			delete(driver.dirs, token)
		}
		return false, err
	case "absent-dir", "absent-file":
		if len(params) != 2 || params[0].Kind != StringKind || params[1].Kind != StringKind {
			return false, malformed("invalid absent node")
		}
		parent, err := driver.directory(params[1])
		if err != nil {
			return false, err
		}
		if command == "absent-dir" {
			return false, parent.AbsentDirectory(ctx, string(params[0].String))
		}
		return false, parent.AbsentFile(ctx, string(params[0].String))
	case "apply-textdelta":
		return false, driver.applyTextDelta(ctx, params)
	case "textdelta-chunk":
		return false, driver.textDeltaChunk(params)
	case "textdelta-end":
		return false, driver.textDeltaEnd(params)
	case "close-file":
		return false, driver.closeFile(ctx, params)
	case "close-edit":
		if err := driver.editor.CloseEdit(ctx); err != nil {
			return false, err
		}
		return true, driver.conn.write(commandResponse("success"))
	case "abort-edit":
		driver.abortDeltas(errors.New("server aborted edit"))
		err := driver.editor.AbortEdit(ctx)
		if writeErr := driver.conn.write(commandResponse("success")); err == nil {
			err = writeErr
		}
		return true, err
	default:
		return false, malformed("unknown editor command %q", command)
	}
}

func (driver *editorDriver) addNode(ctx context.Context, command string, params []Item) error {
	if len(params) != 4 || params[0].Kind != StringKind || params[1].Kind != StringKind || params[2].Kind != StringKind || params[3].Kind != ListKind {
		return malformed("invalid %s", command)
	}
	parent, err := driver.directory(params[1])
	if err != nil {
		return err
	}
	copySource, err := parseCopySource(params[3])
	if err != nil {
		return err
	}
	path, token := string(params[0].String), string(params[2].String)
	if command == "add-dir" {
		directory, err := parent.AddDirectory(ctx, path, copySource)
		if err == nil {
			driver.dirs[token] = directory
		}
		return err
	}
	file, err := parent.AddFile(ctx, path, copySource)
	if err == nil {
		driver.files[token] = file
	}
	return err
}

func (driver *editorDriver) openNode(ctx context.Context, command string, params []Item) error {
	if len(params) != 4 || params[0].Kind != StringKind || params[1].Kind != StringKind || params[2].Kind != StringKind || params[3].Kind != ListKind {
		return malformed("invalid %s", command)
	}
	parent, err := driver.directory(params[1])
	if err != nil {
		return err
	}
	revision, err := optionalRevisionValue(params[3])
	if err != nil {
		return err
	}
	path, token := string(params[0].String), string(params[2].String)
	if command == "open-dir" {
		directory, err := parent.OpenDirectory(ctx, path, revision)
		if err == nil {
			driver.dirs[token] = directory
		}
		return err
	}
	file, err := parent.OpenFile(ctx, path, revision)
	if err == nil {
		driver.files[token] = file
	}
	return err
}

func (driver *editorDriver) changeProp(ctx context.Context, command string, params []Item) error {
	if len(params) != 3 || params[0].Kind != StringKind || params[1].Kind != StringKind || params[2].Kind != ListKind || len(params[2].List) > 1 {
		return malformed("invalid %s", command)
	}
	var value []byte
	if len(params[2].List) == 1 {
		if params[2].List[0].Kind != StringKind {
			return malformed("property value is not a string")
		}
		value = append([]byte{}, params[2].List[0].String...)
	}
	if command == "change-dir-prop" {
		directory, err := driver.directory(params[0])
		if err != nil {
			return err
		}
		return directory.ChangeProp(ctx, string(params[1].String), value)
	}
	file, err := driver.file(params[0])
	if err != nil {
		return err
	}
	return file.ChangeProp(ctx, string(params[1].String), value)
}

func (driver *editorDriver) applyTextDelta(ctx context.Context, params []Item) error {
	if len(params) != 2 || params[0].Kind != StringKind || params[1].Kind != ListKind {
		return malformed("invalid apply-textdelta")
	}
	token := string(params[0].String)
	file, err := driver.file(params[0])
	if err != nil {
		return err
	}
	checksum, err := optionalChecksum(params[1])
	if err != nil {
		return err
	}
	handler, err := file.ApplyTextDelta(ctx, checksum)
	if err != nil {
		return err
	}
	reader, writer := io.Pipe()
	stream := &deltaStream{writer: writer, done: make(chan error, 1)}
	driver.deltas[token] = stream
	go func() {
		windows, err := delta.NewSvndiffReader(reader)
		if err == nil {
			for {
				window, readErr := windows.NextWindow()
				if errors.Is(readErr, io.EOF) {
					break
				}
				if readErr != nil {
					err = readErr
					break
				}
				if err = handler.Window(window); err != nil {
					break
				}
			}
		}
		if err == nil {
			err = handler.Close()
		}
		_ = reader.CloseWithError(err)
		stream.done <- err
	}()
	return nil
}

func (driver *editorDriver) textDeltaChunk(params []Item) error {
	if len(params) != 2 || params[0].Kind != StringKind || params[1].Kind != StringKind {
		return malformed("invalid textdelta-chunk")
	}
	stream := driver.deltas[string(params[0].String)]
	if stream == nil {
		return malformed("unknown text delta token")
	}
	_, err := stream.writer.Write(params[1].String)
	return err
}

func (driver *editorDriver) textDeltaEnd(params []Item) error {
	if len(params) != 1 || params[0].Kind != StringKind {
		return malformed("invalid textdelta-end")
	}
	token := string(params[0].String)
	stream := driver.deltas[token]
	if stream == nil {
		return malformed("unknown text delta token")
	}
	_ = stream.writer.Close()
	err := <-stream.done
	delete(driver.deltas, token)
	return err
}

func (driver *editorDriver) abortDeltas(cause error) {
	for token, stream := range driver.deltas {
		_ = stream.writer.CloseWithError(cause)
		<-stream.done
		delete(driver.deltas, token)
	}
}

func (driver *editorDriver) closeFile(ctx context.Context, params []Item) error {
	if len(params) != 2 || params[0].Kind != StringKind || params[1].Kind != ListKind {
		return malformed("invalid close-file")
	}
	token := string(params[0].String)
	file, err := driver.file(params[0])
	if err != nil {
		return err
	}
	checksum, err := optionalChecksum(params[1])
	if err == nil {
		err = file.Close(ctx, checksum)
		delete(driver.files, token)
	}
	return err
}

func (driver *editorDriver) directory(item Item) (delta.DirEditor, error) {
	if item.Kind != StringKind {
		return nil, malformed("directory token is not a string")
	}
	directory := driver.dirs[string(item.String)]
	if directory == nil {
		return nil, malformed("unknown directory token")
	}
	return directory, nil
}

func (driver *editorDriver) file(item Item) (delta.FileEditor, error) {
	if item.Kind != StringKind {
		return nil, malformed("file token is not a string")
	}
	file := driver.files[string(item.String)]
	if file == nil {
		return nil, malformed("unknown file token")
	}
	return file, nil
}

func parseCopySource(item Item) (*delta.CopySource, error) {
	if item.Kind != ListKind || len(item.List) != 0 && len(item.List) != 2 {
		return nil, malformed("invalid copy source")
	}
	if len(item.List) == 0 {
		return nil, nil
	}
	if item.List[0].Kind != StringKind || item.List[1].Kind != NumberKind {
		return nil, malformed("invalid copy source")
	}
	return &delta.CopySource{Path: string(item.List[0].String), Rev: svn.Revnum(item.List[1].Number)}, nil
}

func optionalRevisionValue(item Item) (svn.Revnum, error) {
	if item.Kind != ListKind || len(item.List) > 1 {
		return svn.InvalidRevnum, malformed("invalid optional revision")
	}
	if len(item.List) == 0 {
		return svn.InvalidRevnum, nil
	}
	if item.List[0].Kind != NumberKind {
		return svn.InvalidRevnum, malformed("revision is not a number")
	}
	return svn.Revnum(item.List[0].Number), nil
}

func optionalChecksum(item Item) (*svn.Checksum, error) {
	if item.Kind != ListKind || len(item.List) > 1 {
		return nil, malformed("invalid optional checksum")
	}
	if len(item.List) == 0 {
		return nil, nil
	}
	if item.List[0].Kind != StringKind {
		return nil, malformed("checksum is not a string")
	}
	checksum, err := svn.ParseChecksum(string(item.List[0].String))
	if err != nil {
		return nil, err
	}
	return &checksum, nil
}

func (conn *connection) writeFailure(err error) error {
	code, ok := svn.ErrorCode(err)
	if !ok {
		code = svn.ErrRASvnCmdErr
	}
	return conn.write(commandResponse("failure", List(Number(uint64(code)), String([]byte(err.Error())), String(nil), Number(0))))
}

func commandResponse(status string, items ...Item) Item { return List(Word(status), List(items...)) }

func (conn *connection) drainEditor(replay bool) error {
	for {
		item, err := conn.reader.Decode()
		if err != nil {
			return err
		}
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != WordKind {
			return malformed("invalid editor command while aborting")
		}
		switch item.List[0].Word {
		case "abort-edit":
			return conn.write(commandResponse("success"))
		case "finish-replay":
			if replay {
				return nil
			}
		}
	}
}
