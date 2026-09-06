package radav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/svn"
)

func TestUpdateAndReplayDriveEditors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == "OPTIONS" {
			writer.Header().Set("SVN-Repository-Root", "/repo")
			writer.Header().Set("SVN-Repository-UUID", "uuid")
			writer.Header().Set("SVN-Youngest-Rev", "3")
			writer.Header().Set("SVN-Me-Resource", "/repo/!svn/me")
			writer.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
			writer.Header().Set("SVN-Rev-Stub", "/repo/!svn/rev")
			writer.Header().Set("SVN-Allow-Bulk-Updates", "Prefer")
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/xml")
		if strings.Contains(string(body), "update-report") {
			if request.URL.Path != "/repo/!svn/me" {
				t.Fatalf("update path=%q", request.URL.Path)
			}
			if !strings.Contains(string(body), `send-all="true"`) || !strings.Contains(string(body), `<S:src-path>/repo</S:src-path>`) {
				t.Fatalf("update body=%s", body)
			}
			_, _ = io.WriteString(writer, `<S:update-report xmlns:S="svn:"><S:target-revision rev="3"/><S:open-directory rev="1"><S:add-directory name="dir"><S:add-file name="file"><S:set-prop name="custom">value</S:set-prop></S:add-file></S:add-directory></S:open-directory></S:update-report>`)
			return
		}
		if strings.Contains(string(body), "replay-report") {
			if request.URL.Path != "/repo" {
				t.Fatalf("replay path=%q", request.URL.Path)
			}
			_, _ = io.WriteString(writer, `<S:editor-report xmlns:S="svn:"><S:target-revision rev="3"/><S:open-root rev="2"/><S:add-directory name="dir"/><S:add-file name="dir/file"/><S:change-file-prop name="custom">dmFsdWU=</S:change-file-prop><S:close-file/><S:close-directory/><S:close-directory/></S:editor-report>`)
			return
		}
		t.Fatalf("body=%s", body)
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	ctx := context.Background()

	updateEditor := &recordingDAVEditor{}
	reporter, err := session.DoUpdate(ctx, 3, "", svn.DepthInfinity, false, false, updateEditor)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.SetPath(ctx, "", 1, svn.DepthInfinity, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.FinishReport(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(updateEditor.calls, ","); got != "target:3,root:1,add-dir:dir,add-file:dir/file,prop:custom=value,close-file,close-dir,close-dir,close-edit" {
		t.Fatalf("update calls=%s", got)
	}

	replayEditor := &recordingDAVEditor{}
	if err := session.Replay(ctx, 3, 0, false, replayEditor); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(replayEditor.calls, ","); got != "target:3,root:2,add-dir:dir,add-file:dir/file,prop:custom=value,close-file,close-dir,close-dir,close-edit" {
		t.Fatalf("replay calls=%s", got)
	}
}

func TestSkeltaUpdateFetchesFulltext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == "OPTIONS" {
			writer.Header().Set("SVN-Repository-Root", "/repo")
			writer.Header().Set("SVN-Repository-UUID", "uuid")
			writer.Header().Set("SVN-Youngest-Rev", "2")
			writer.Header().Set("SVN-Me-Resource", "/repo/!svn/me")
			writer.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
			writer.Header().Set("SVN-Allow-Bulk-Updates", "Off")
			return
		}
		if request.Method == "GET" {
			if request.URL.Path != "/repo/content" {
				t.Fatalf("GET path=%q", request.URL.Path)
			}
			_, _ = io.WriteString(writer, "fulltext")
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "send-all") || !strings.Contains(string(body), `<S:include-props>yes</S:include-props>`) {
			t.Fatalf("skelta body=%s", body)
		}
		_, _ = io.WriteString(writer, `<S:update-report xmlns:S="svn:" xmlns:D="DAV:"><S:open-directory rev="1"><S:add-file name="file"><D:checked-in><D:href>/repo/content</D:href></D:checked-in><S:fetch-file/></S:add-file></S:open-directory></S:update-report>`)
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	editor := &recordingDAVEditor{}
	reporter, err := session.DoUpdate(context.Background(), 2, "", svn.DepthInfinity, false, false, editor)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.SetPath(context.Background(), "", 1, svn.DepthInfinity, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.FinishReport(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(editor.calls, ","); got != "root:1,add-file:file,delta:8,delta-close,close-file,close-dir,close-edit" {
		t.Fatalf("calls=%s", got)
	}
}

type recordingDAVEditor struct{ calls []string }

func (editor *recordingDAVEditor) SetTargetRevision(_ context.Context, revision svn.Revnum) error {
	editor.calls = append(editor.calls, "target:"+strconv.FormatInt(int64(revision), 10))
	return nil
}
func (editor *recordingDAVEditor) OpenRoot(_ context.Context, revision svn.Revnum) (delta.DirEditor, error) {
	editor.calls = append(editor.calls, "root:"+strconv.FormatInt(int64(revision), 10))
	return &recordingDAVDir{editor: editor}, nil
}
func (editor *recordingDAVEditor) CloseEdit(context.Context) error {
	editor.calls = append(editor.calls, "close-edit")
	return nil
}
func (editor *recordingDAVEditor) AbortEdit(context.Context) error {
	editor.calls = append(editor.calls, "abort")
	return nil
}

type recordingDAVDir struct{ editor *recordingDAVEditor }

func (dir *recordingDAVDir) DeleteEntry(context.Context, string, svn.Revnum) error { return nil }
func (dir *recordingDAVDir) AddDirectory(_ context.Context, name string, _ *delta.CopySource) (delta.DirEditor, error) {
	dir.editor.calls = append(dir.editor.calls, "add-dir:"+name)
	return &recordingDAVDir{editor: dir.editor}, nil
}
func (dir *recordingDAVDir) OpenDirectory(_ context.Context, name string, _ svn.Revnum) (delta.DirEditor, error) {
	dir.editor.calls = append(dir.editor.calls, "open-dir:"+name)
	return &recordingDAVDir{editor: dir.editor}, nil
}
func (dir *recordingDAVDir) ChangeProp(context.Context, string, []byte) error { return nil }
func (dir *recordingDAVDir) AbsentDirectory(context.Context, string) error    { return nil }
func (dir *recordingDAVDir) AddFile(_ context.Context, name string, _ *delta.CopySource) (delta.FileEditor, error) {
	dir.editor.calls = append(dir.editor.calls, "add-file:"+name)
	return &recordingDAVFile{editor: dir.editor}, nil
}
func (dir *recordingDAVDir) OpenFile(_ context.Context, name string, _ svn.Revnum) (delta.FileEditor, error) {
	dir.editor.calls = append(dir.editor.calls, "open-file:"+name)
	return &recordingDAVFile{editor: dir.editor}, nil
}
func (dir *recordingDAVDir) AbsentFile(context.Context, string) error { return nil }
func (dir *recordingDAVDir) Close(context.Context) error {
	dir.editor.calls = append(dir.editor.calls, "close-dir")
	return nil
}

type recordingDAVFile struct{ editor *recordingDAVEditor }

func (file *recordingDAVFile) ApplyTextDelta(context.Context, *svn.Checksum) (delta.WindowHandler, error) {
	return delta.WindowHandlerFunc(func(window *delta.Window) error {
		if window == nil {
			file.editor.calls = append(file.editor.calls, "delta-close")
		} else {
			file.editor.calls = append(file.editor.calls, "delta:"+strconv.Itoa(window.TargetLength))
		}
		return nil
	}), nil
}
func (file *recordingDAVFile) ChangeProp(_ context.Context, name string, value []byte) error {
	file.editor.calls = append(file.editor.calls, "prop:"+name+"="+string(value))
	return nil
}
func (file *recordingDAVFile) Close(context.Context, *svn.Checksum) error {
	file.editor.calls = append(file.editor.calls, "close-file")
	return nil
}
