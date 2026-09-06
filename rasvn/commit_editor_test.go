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

func TestCommitEditorStreamsDeltaAndCommitInfo(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer clientStream.Close()
	defer serverStream.Close()
	session := &Session{
		conn:          newConnection(clientStream, nil, "", "", false),
		repositoryURL: "svn://host/repo",
		capabilities: map[string]bool{
			"commit-revprops":    true,
			"ephemeral-txnprops": true,
			"accepts-svndiff2":   true,
		},
	}
	serverDone := make(chan error, 1)
	content := make([]byte, 256*1024)
	for index := range content {
		content[index] = byte(index % 251)
	}
	go func() {
		serverDone <- serveCommitExchange(serverStream, content)
		_ = serverStream.Close()
	}()

	var info *ra.CommitInfo
	editor, err := session.GetCommitEditor(context.Background(), svn.Props{
		"svn:log": []byte("add a"), "custom:ticket": []byte("42"),
	}, map[string]string{"locked": "token"}, true, func(committed *ra.CommitInfo) error {
		info = committed
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.AddFile(context.Background(), "a", nil)
	if err != nil {
		t.Fatal(err)
	}
	windows, err := file.ApplyTextDelta(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(content); offset += 32 * 1024 {
		chunk := content[offset : offset+32*1024]
		window := &delta.Window{TargetLength: len(chunk), Ops: []delta.Op{{Kind: delta.OpNew, Length: len(chunk)}}, NewData: chunk}
		if err := windows.Window(window); err != nil {
			t.Fatal(err)
		}
	}
	if err := windows.Close(); err != nil {
		t.Fatal(err)
	}
	checksum := svn.Sum(svn.ChecksumMD5, content)
	if err := file.Close(context.Background(), &checksum); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	closeErr := editor.CloseEdit(context.Background())
	serverErr := <-serverDone
	if closeErr != nil || serverErr != nil {
		t.Fatalf("close error=%v server error=%v", closeErr, serverErr)
	}
	if info == nil || info.Revision != 2 || info.Author != "fixture" || info.PostCommitError != "hook warning" || info.ReposRoot != "svn://host/repo" {
		t.Fatalf("commit info=%#v", info)
	}
}

func serveCommitExchange(stream net.Conn, expected []byte) error {
	reader, writer := NewReader(stream), NewWriter(stream)
	command, err := reader.Decode()
	if err != nil {
		return err
	}
	if command.Kind != ListKind || len(command.List) != 2 || command.List[0].Word != "commit" {
		return malformed("expected commit command")
	}
	params := command.List[1].List
	if len(params) != 4 || params[0].Kind != StringKind || string(params[0].String) != "add a" || params[1].Kind != ListKind || params[2].Word != "true" || params[3].Kind != ListKind {
		return malformed("invalid commit parameters")
	}
	props, err := parseProps(params[3].List)
	if err != nil {
		return err
	}
	if string(props["custom:ticket"]) != "42" || len(props["svn:txn-client-compat-version"]) == 0 || len(props["svn:txn-user-agent"]) == 0 {
		return malformed("missing commit properties")
	}
	if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
		return err
	}
	if err := writeTestItem(writer, response("success")); err != nil {
		return err
	}
	var deltaWire bytes.Buffer
	commands := []string{"open-root", "add-file", "apply-textdelta"}
	for _, want := range commands {
		item, err := reader.Decode()
		if err != nil {
			return err
		}
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Word != want {
			return malformed("editor command=%#v, want %s", item, want)
		}
	}
	for {
		item, err := reader.Decode()
		if err != nil {
			return err
		}
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != WordKind {
			return malformed("invalid text delta command")
		}
		if item.List[0].Word == "textdelta-end" {
			break
		}
		if item.List[0].Word != "textdelta-chunk" || len(item.List[1].List) != 2 || item.List[1].List[1].Kind != StringKind {
			return malformed("unexpected text delta command %#v", item)
		}
		if _, err := deltaWire.Write(item.List[1].List[1].String); err != nil {
			return err
		}
	}
	for _, want := range []string{"close-file", "close-dir", "close-edit"} {
		item, err := reader.Decode()
		if err != nil {
			return err
		}
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Word != want {
			return malformed("editor command=%#v, want %s", item, want)
		}
	}
	decoder, err := delta.NewSvndiffReader(bytes.NewReader(deltaWire.Bytes()))
	if err != nil {
		return err
	}
	var content bytes.Buffer
	for {
		window, err := decoder.NextWindow()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		part, err := delta.ApplyWindow(nil, *window)
		if err != nil {
			return err
		}
		_, _ = content.Write(part)
	}
	if !bytes.Equal(content.Bytes(), expected) || decoder.(*delta.Decoder).Version() != 2 {
		return malformed("decoded delta length=%d", content.Len())
	}
	if err := writeTestItem(writer, response("success")); err != nil {
		return err
	}
	if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
		return err
	}
	return writeTestItem(writer, List(
		Number(2),
		List(String([]byte("2020-01-06T00:00:00.000000Z"))),
		List(String([]byte("fixture"))),
		List(String([]byte("hook warning"))),
	))
}

func TestParseCommitInfoRejectsMalformedDate(t *testing.T) {
	_, err := parseCommitInfo(List(Number(1), List(String([]byte("bad"))), List()), "root")
	if err == nil {
		t.Fatal("accepted malformed commit date")
	}
}

func TestCommitEditorEarlyErrorSendsAbort(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	session := &Session{conn: newConnection(client, nil, "", "", false), capabilities: map[string]bool{}}
	done := make(chan error, 1)
	go func() {
		reader, writer := NewReader(server), NewWriter(server)
		if _, err := reader.Decode(); err != nil {
			done <- err
			return
		}
		if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
			done <- err
			return
		}
		if err := writeTestItem(writer, response("success")); err != nil {
			done <- err
			return
		}
		if _, err := reader.Decode(); err != nil {
			done <- err
			return
		}
		failure := List(Number(uint64(svn.ErrReposHookFailure)), String([]byte("blocked")), String(nil), Number(0))
		if err := writeTestItem(writer, List(Word("failure"), List(failure))); err != nil {
			done <- err
			return
		}
		for {
			item, err := reader.Decode()
			if err != nil {
				done <- err
				return
			}
			if item.Kind == ListKind && item.List[0].Word == "abort-edit" {
				break
			}
		}
		done <- nil
	}()
	editor, err := session.GetCommitEditor(context.Background(), svn.Props{"svn:log": []byte("fail")}, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := editor.OpenRoot(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := editor.CloseEdit(context.Background()); !errors.Is(err, svn.ErrReposHookFailure) {
		t.Fatalf("close error=%v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCommitAbortReleasesConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	session := &Session{conn: newConnection(client, nil, "", "", false), capabilities: map[string]bool{}}
	done := make(chan error, 1)
	go func() {
		reader, writer := NewReader(server), NewWriter(server)
		if _, err := reader.Decode(); err != nil {
			done <- err
			return
		}
		if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
			done <- err
			return
		}
		if err := writeTestItem(writer, response("success")); err != nil {
			done <- err
			return
		}
		item, err := reader.Decode()
		if err != nil || item.List[0].Word != "abort-edit" {
			done <- errors.Join(err, malformed("expected abort-edit"))
			return
		}
		if err := writeTestItem(writer, response("success")); err != nil {
			done <- err
			return
		}
		item, err = reader.Decode()
		if err != nil || item.List[0].Word != "get-latest-rev" {
			done <- errors.Join(err, malformed("expected follow-up command"))
			return
		}
		if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
			done <- err
			return
		}
		done <- writeTestItem(writer, response("success", Number(7)))
	}()
	editor, err := session.GetCommitEditor(context.Background(), svn.Props{"svn:log": []byte("abort")}, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.AbortEdit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if revision, err := session.LatestRevision(context.Background()); err != nil || revision != 7 {
		t.Fatalf("revision=%d error=%v", revision, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
