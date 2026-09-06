package rasvn

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestEditorCallbackFailureDrainsThroughAbort(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer clientStream.Close()
	defer serverStream.Close()
	conn := newConnection(clientStream, nil, "", "", false)
	want := errors.New("editor rejected revision")
	editor := &failingEditor{err: want}
	serverDone := make(chan error, 1)
	go func() {
		reader, writer := NewReader(serverStream), NewWriter(serverStream)
		if err := writeTestItem(writer, List(Word("target-rev"), List(Number(7)))); err != nil {
			serverDone <- err
			return
		}
		failure, err := reader.Decode()
		if err != nil {
			serverDone <- err
			return
		}
		if failure.Kind != ListKind || failure.List[0].Word != "failure" {
			serverDone <- malformed("expected editor failure")
			return
		}
		if err := writeTestItem(writer, List(Word("open-root"), List(List(Number(6)), String([]byte("discarded"))))); err != nil {
			serverDone <- err
			return
		}
		if err := writeTestItem(writer, List(Word("abort-edit"), List())); err != nil {
			serverDone <- err
			return
		}
		response, err := reader.Decode()
		if err != nil {
			serverDone <- err
			return
		}
		if response.Kind != ListKind || response.List[0].Word != "success" {
			serverDone <- malformed("expected abort success")
			return
		}
		serverDone <- nil
	}()
	if err := conn.driveEditor(context.Background(), editor, false); !errors.Is(err, want) {
		t.Fatalf("drive error = %v", err)
	}
	if editor.aborts != 1 {
		t.Fatalf("abort calls = %d", editor.aborts)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestCloseEditFailureDoesNotWaitForAbort(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer clientStream.Close()
	defer serverStream.Close()
	conn := newConnection(clientStream, nil, "", "", false)
	want := errors.New("close rejected")
	editor := &closeFailingEditor{err: want}
	serverDone := make(chan error, 1)
	go func() {
		reader, writer := NewReader(serverStream), NewWriter(serverStream)
		if err := writeTestItem(writer, List(Word("close-edit"), List())); err != nil {
			serverDone <- err
			return
		}
		failure, err := reader.Decode()
		if err != nil {
			serverDone <- err
			return
		}
		if failure.Kind != ListKind || failure.List[0].Word != "failure" {
			serverDone <- malformed("expected close-edit failure")
			return
		}
		serverDone <- nil
	}()
	if err := conn.driveEditor(context.Background(), editor, false); !errors.Is(err, want) {
		t.Fatalf("drive error = %v", err)
	}
	if editor.aborts != 1 {
		t.Fatalf("abort calls = %d", editor.aborts)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestReportCallbackFailureConsumesOuterResponse(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer clientStream.Close()
	defer serverStream.Close()
	conn := newConnection(clientStream, nil, "", "", false)
	want := errors.New("editor rejected revision")
	serverDone := make(chan error, 1)
	go func() {
		reader, writer := NewReader(serverStream), NewWriter(serverStream)
		if _, err := reader.Decode(); err != nil {
			serverDone <- err
			return
		}
		if err := writeTestItem(writer, List(Word("success"), List(List(), String(nil)))); err != nil {
			serverDone <- err
			return
		}
		if err := writeTestItem(writer, List(Word("target-rev"), List(Number(7)))); err != nil {
			serverDone <- err
			return
		}
		if _, err := reader.Decode(); err != nil {
			serverDone <- err
			return
		}
		if err := writeTestItem(writer, List(Word("abort-edit"), List())); err != nil {
			serverDone <- err
			return
		}
		if _, err := reader.Decode(); err != nil {
			serverDone <- err
			return
		}
		if err := writeTestItem(writer, List(Word("success"), List())); err != nil {
			serverDone <- err
			return
		}
		if _, err := reader.Decode(); err != nil {
			serverDone <- err
			return
		}
		if err := writeTestItem(writer, List(Word("success"), List(List(), String(nil)))); err != nil {
			serverDone <- err
			return
		}
		serverDone <- writeTestItem(writer, List(Word("success"), List(Number(8))))
	}()

	conn.mu.Lock()
	reporter := &reporter{conn: conn, editor: &failingEditor{err: want}}
	if err := reporter.FinishReport(context.Background()); !errors.Is(err, want) {
		t.Fatalf("finish report error = %v", err)
	}
	items, err := conn.command(context.Background(), "get-latest-rev")
	if err != nil || len(items) != 1 || items[0].Number != 8 {
		t.Fatalf("next command items=%v error=%v", items, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestParseChangedPathNodeKind(t *testing.T) {
	for _, kind := range []Item{String([]byte("file")), Word("file")} {
		changed, err := parseChangedPath(List(
			String([]byte("/trunk/file")),
			Word("M"),
			List(),
			List(kind, Word("true"), Word("false")),
		))
		if err != nil {
			t.Fatal(err)
		}
		if changed.NodeKind != svn.NodeFile {
			t.Fatalf("node kind = %v", changed.NodeKind)
		}
	}
}

func TestParseLogEntryRejectsMalformedDate(t *testing.T) {
	_, err := parseLogEntry(List(
		List(), Number(1), List(String([]byte("author"))),
		List(String([]byte("not-a-date"))), List(String([]byte("message"))),
	))
	if err == nil {
		t.Fatal("expected malformed log date error")
	}
}

func TestParseLogEntryHonorsInvalidRevisionSentinel(t *testing.T) {
	item := List(List(), Number(0), List(), List(), List(), Word("false"), Word("true"), Number(0), List(), Word("false"))
	entry, err := parseLogEntry(item)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Revision != svn.InvalidRevnum {
		t.Fatalf("revision=%d, want invalid", entry.Revision)
	}
}

func TestReportRecurseMatchesLegacyDepthSemantics(t *testing.T) {
	for _, test := range []struct {
		depth svn.Depth
		want  bool
	}{
		{svn.DepthUnknown, true},
		{svn.DepthEmpty, false},
		{svn.DepthFiles, false},
		{svn.DepthImmediates, true},
		{svn.DepthInfinity, true},
	} {
		if got := reportRecurse(test.depth); got != test.want {
			t.Errorf("depth=%s recurse=%v, want %v", test.depth, got, test.want)
		}
	}
}

func TestListRequiresCapability(t *testing.T) {
	session := &Session{capabilities: map[string]bool{}}
	err := session.List(context.Background(), "", 1, nil, svn.DepthInfinity, svn.DirentAll, func(string, *svn.Dirent) error { return nil })
	if !errors.Is(err, svn.ErrUnsupportedFeature) {
		t.Fatalf("list error = %v", err)
	}
}

func TestDecodeWrapperOnlyFailure(t *testing.T) {
	err := decodeFailure([]Item{List(
		Number(uint64(svn.ErrRASvnCmdErr)),
		String([]byte("command failed")),
		String(nil),
		Number(0),
	)})
	if !errors.Is(err, svn.ErrRASvnCmdErr) {
		t.Fatalf("decoded error = %v", err)
	}
}

func TestReporterRejectsInvalidRevision(t *testing.T) {
	reporter := &reporter{}
	if err := reporter.SetPath(context.Background(), "", svn.InvalidRevnum, svn.DepthInfinity, false, ""); !errors.Is(err, svn.ErrIncorrectParams) {
		t.Fatalf("set path error=%v", err)
	}
	if err := reporter.LinkPath(context.Background(), "", "svn://example.invalid", svn.InvalidRevnum, svn.DepthInfinity, false, ""); !errors.Is(err, svn.ErrIncorrectParams) {
		t.Fatalf("link path error=%v", err)
	}
}

func TestParseListDirentRejectsWrongOptionalKind(t *testing.T) {
	_, _, err := parseListDirent(List(
		String([]byte("file")), Word("file"), List(String([]byte("not-a-size"))),
	), svn.DirentKind|svn.DirentSize)
	if err == nil {
		t.Fatal("expected malformed size error")
	}
}

func TestFileRevisionDeltaDrainsWithoutConsumer(t *testing.T) {
	for _, handler := range []ra.FileRevHandler{
		nil,
		func(context.Context, ra.FileRevision) error { return nil },
	} {
		var wire bytes.Buffer
		writer := NewWriter(&wire)
		if handler == nil {
			_ = writer.Encode(String([]byte("discarded")))
		} else {
			_ = writer.Encode(String([]byte("discarded after header")))
		}
		_ = writer.Encode(String(nil))
		_ = writer.Encode(Word("after-delta"))
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		stream := &bufferStream{Buffer: &wire}
		conn := newConnection(stream, nil, "", "", false)
		first := []byte("discarded")
		if handler != nil {
			first = []byte("SVN\x00")
		}
		if err := (&Session{conn: conn}).streamFileRevisionDelta(context.Background(), ra.FileRevision{}, first, handler); err != nil {
			t.Fatalf("handler nil=%v: %v", handler == nil, err)
		}
		item, err := conn.reader.Decode()
		if err != nil || item.Kind != WordKind || item.Word != "after-delta" {
			t.Fatalf("next item=%#v error=%v", item, err)
		}
	}
}

func TestServerAbortClosesActiveDeltas(t *testing.T) {
	stream := &bufferStream{Buffer: new(bytes.Buffer)}
	done := make(chan error, 1)
	reader, writer := io.Pipe()
	go func() {
		_, err := io.Copy(io.Discard, reader)
		done <- err
	}()
	driver := &editorDriver{
		conn:   newConnection(stream, nil, "", "", false),
		editor: &failingEditor{},
		dirs:   make(map[string]delta.DirEditor),
		files:  make(map[string]delta.FileEditor),
		deltas: map[string]*deltaStream{"token": {writer: writer, done: done}},
	}
	finished, err := driver.dispatch(context.Background(), "abort-edit", nil)
	if err != nil || !finished || len(driver.deltas) != 0 {
		t.Fatalf("finished=%v deltas=%d error=%v", finished, len(driver.deltas), err)
	}
}

type bufferStream struct{ *bytes.Buffer }

func (*bufferStream) Close() error { return nil }

type failingEditor struct {
	err    error
	aborts int
}

func (editor *failingEditor) SetTargetRevision(context.Context, svn.Revnum) error { return editor.err }
func (*failingEditor) OpenRoot(context.Context, svn.Revnum) (delta.DirEditor, error) {
	panic("unexpected callback")
}
func (*failingEditor) CloseEdit(context.Context) error        { return nil }
func (editor *failingEditor) AbortEdit(context.Context) error { editor.aborts++; return nil }

type closeFailingEditor struct {
	err    error
	aborts int
}

func (*closeFailingEditor) SetTargetRevision(context.Context, svn.Revnum) error { return nil }
func (*closeFailingEditor) OpenRoot(context.Context, svn.Revnum) (delta.DirEditor, error) {
	panic("unexpected callback")
}
func (editor *closeFailingEditor) CloseEdit(context.Context) error { return editor.err }
func (editor *closeFailingEditor) AbortEdit(context.Context) error { editor.aborts++; return nil }
