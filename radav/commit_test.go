package radav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type recordedDAVRequest struct {
	method string
	path   string
	header http.Header
	body   string
}

func TestHTTPv2CommitEditorRequestSequence(t *testing.T) {
	var mutex sync.Mutex
	var requests []recordedDAVRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		mutex.Lock()
		requests = append(requests, recordedDAVRequest{method: request.Method, path: request.URL.Path, header: request.Header.Clone(), body: string(body)})
		mutex.Unlock()
		switch request.Method {
		case http.MethodOptions:
			writer.Header().Set("SVN-Repository-Root", "/repo")
			writer.Header().Set("SVN-Repository-UUID", "uuid")
			writer.Header().Set("SVN-Youngest-Rev", "0")
			writer.Header().Set("SVN-Me-Resource", "/repo/!svn/me")
			writer.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
			writer.Header().Set("SVN-Rev-Stub", "/repo/!svn/rev")
			writer.Header().Set("SVN-Txn-Root-Stub", "/repo/!svn/txr")
			writer.Header().Set("SVN-Txn-Stub", "/repo/!svn/txn")
			writer.Header().Set("SVN-Supported-Posts", "create-txn-with-props")
			writer.Header().Set("DAV", "http://subversion.tigris.org/xmlns/dav/svn/svndiff2")
		case http.MethodPost:
			writer.Header().Set("SVN-Txn-Name", "1-1")
			writer.WriteHeader(http.StatusCreated)
		case "MKCOL", http.MethodPut:
			writer.WriteHeader(http.StatusCreated)
		case "PROPPATCH":
			writer.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(writer, multistatusXML(propResponseXML(request.URL.Path, "")))
		case "MERGE":
			writer.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(writer, `<D:merge-response xmlns:D="DAV:"><D:updated-set><D:response><D:href>/repo/!svn/vcc/default</D:href><D:propstat><D:prop><D:resourcetype><D:baseline/></D:resourcetype><D:version-name>1</D:version-name><D:creationdate>2026-01-02T03:04:05.000000Z</D:creationdate><D:creator-displayname>alice</D:creator-displayname></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:updated-set></D:merge-response>`)
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	session := openTestSession(t, server.URL+"/repo")
	var committed int64
	editor, err := session.GetCommitEditor(context.Background(), svn.Props{"svn:log": []byte("message"), "custom:rev": []byte("value")}, nil, false, func(info *ra.CommitInfo) error { committed = int64(info.Revision); return nil })
	_ = editor
	_ = committed
	if err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := root.AddDirectory(context.Background(), "project", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.ChangeProp(context.Background(), "custom:directory", []byte("set")); err != nil {
		t.Fatal(err)
	}
	file, err := directory.AddFile(context.Background(), "project/data.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	windows, err := file.ApplyTextDelta(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.Window(&delta.Window{TargetLength: 4, Ops: []delta.Op{{Kind: delta.OpNew, Length: 4}}, NewData: []byte("data")}); err != nil {
		t.Fatal(err)
	}
	if err := windows.Close(); err != nil {
		t.Fatal(err)
	}
	checksum := svn.Sum(svn.ChecksumMD5, []byte("data"))
	if err := file.Close(context.Background(), &checksum); err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := editor.CloseEdit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if committed != 1 {
		t.Fatalf("committed revision=%d", committed)
	}

	mutex.Lock()
	defer mutex.Unlock()
	var sequence []string
	for _, request := range requests[1:] {
		sequence = append(sequence, request.method+" "+request.path)
	}
	want := []string{"POST /repo/!svn/me", "MKCOL /repo/!svn/txr/1-1/project", "PUT /repo/!svn/txr/1-1/project/data.txt", "PROPPATCH /repo/!svn/txr/1-1/project", "MERGE /repo"}
	if strings.Join(sequence, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sequence:\n%s", strings.Join(sequence, "\n"))
	}
	if !strings.Contains(requests[1].body, "create-txn-with-props") || !strings.Contains(requests[1].body, "custom:rev") {
		t.Fatalf("POST body=%q", requests[1].body)
	}
	if requests[3].header.Get("Content-Type") != "application/vnd.svn-svndiff" || !strings.HasPrefix(requests[3].body, "SVN\x02") {
		t.Fatalf("PUT headers=%v body=%q", requests[3].header, requests[3].body)
	}
}

func TestHTTPv1CommitRejectedBeforeMutation(t *testing.T) {
	var mutations int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodOptions:
		case "PROPFIND":
			writer.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(writer, multistatusXML(propResponseXML("/repo", `<D:version-controlled-configuration><D:href>/repo/!svn/vcc/default</D:href></D:version-controlled-configuration><V:repository-uuid xmlns:V="http://subversion.tigris.org/xmlns/dav/">uuid</V:repository-uuid><V:baseline-relative-path xmlns:V="http://subversion.tigris.org/xmlns/dav/"></V:baseline-relative-path>`)))
		default:
			mutations++
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	_, err := session.GetCommitEditor(context.Background(), nil, nil, false, nil)
	if !errors.Is(err, svn.ErrUnsupportedFeature) {
		t.Fatalf("error=%v", err)
	}
	if mutations != 0 {
		t.Fatalf("mutating requests=%d", mutations)
	}
}

func TestCommitAbortCleansDeltaAndTransaction(t *testing.T) {
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodOptions:
			setHTTPv2Headers(writer.Header(), true)
		case http.MethodPost:
			writer.Header().Set("SVN-Txn-Name", "1-1")
			writer.WriteHeader(http.StatusCreated)
		case http.MethodHead:
			writer.WriteHeader(http.StatusNotFound)
		case http.MethodDelete:
			deleted.Store(true)
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	editor, err := session.GetCommitEditor(context.Background(), nil, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	fileEditor, err := root.AddFile(context.Background(), "open.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileEditor.ApplyTextDelta(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	temporary := fileEditor.(*commitFile).deltaName
	if err := editor.AbortEdit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !deleted.Load() {
		t.Fatal("transaction was not deleted")
	}
	if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary delta still exists: %v", err)
	}
}

func TestCommitRollsBackTransactionWhenRevpropsFail(t *testing.T) {
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodOptions:
			setHTTPv2Headers(writer.Header(), false)
		case http.MethodPost:
			writer.Header().Set("SVN-Txn-Name", "1-1")
			writer.WriteHeader(http.StatusCreated)
		case "PROPPATCH":
			writer.WriteHeader(http.StatusForbidden)
		case http.MethodDelete:
			deleted.Store(true)
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	editor, err := session.GetCommitEditor(context.Background(), svn.Props{"svn:log": []byte("message")}, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := editor.OpenRoot(context.Background(), 0); err == nil {
		t.Fatal("OpenRoot unexpectedly succeeded")
	}
	if !deleted.Load() {
		t.Fatal("failed transaction was not rolled back")
	}
}

func TestCommitRejectsAddOfExistingFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodOptions:
			setHTTPv2Headers(writer.Header(), true)
		case http.MethodPost:
			writer.Header().Set("SVN-Txn-Name", "1-1")
			writer.WriteHeader(http.StatusCreated)
		case http.MethodHead:
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	editor, err := session.GetCommitEditor(context.Background(), nil, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.AddFile(context.Background(), "existing.txt", nil); !errors.Is(err, svn.ErrFSAlreadyExists) {
		t.Fatalf("error=%v", err)
	}
}

func TestProppatchMultiStatusFailure(t *testing.T) {
	body := `<D:multistatus xmlns:D="DAV:"><D:response><D:href>/repo/file</D:href><D:propstat><D:prop/><D:status>HTTP/1.1 409 Conflict</D:status></D:propstat></D:response></D:multistatus>`
	response := &http.Response{StatusCode: http.StatusMultiStatus, Body: io.NopCloser(strings.NewReader(body))}
	if err := checkProppatchResponse(response); !errors.Is(err, svn.ErrRADAVProppatchFailed) {
		t.Fatalf("error=%v", err)
	}
}

func TestAtomicAndBinaryPropertyXML(t *testing.T) {
	oldAbsent := map[string]*[]byte{"custom:prop": nil}
	body, err := propertyUpdateXML(svn.Props{"custom:prop": []byte{0, 1, 2}}, oldAbsent)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `V:encoding="base64"`) || !strings.Contains(text, `<V:old-value V:old-value-absent="1">`) || !strings.Contains(text, `</V:old-value>AAEC`) {
		t.Fatalf("body=%s", text)
	}
	old := []byte("current")
	body, err = propertyUpdateXML(svn.Props{"custom:prop": nil}, map[string]*[]byte{"custom:prop": &old})
	if err != nil {
		t.Fatal(err)
	}
	text = string(body)
	if !strings.Contains(text, `C:custom:prop V:old-value-absent="1"`) || !strings.Contains(text, `<V:old-value>current</V:old-value>`) {
		t.Fatalf("delete body=%s", text)
	}
}

func TestCommitLockTokenHeaders(t *testing.T) {
	editor := &commitEditor{
		session:    &Session{url: "https://example.test/repo"},
		lockTokens: map[string]string{"dir/file": "opaquelocktoken:file", "dir/child/file": "opaquelocktoken:child"},
		deleted:    make(map[string]bool),
	}
	headers := editor.mutationHeaders("dir/file", 7)
	if headers.Get("X-SVN-Version-Name") != "7" || headers.Get("If") != `<https://example.test/repo/dir/file> (<opaquelocktoken:file>)` {
		t.Fatalf("mutation headers=%v", headers)
	}
	recursive := editor.recursiveLockHeaders("dir")
	if !strings.Contains(recursive.Get("If"), "opaquelocktoken:file") || !strings.Contains(recursive.Get("If"), "opaquelocktoken:child") {
		t.Fatalf("recursive headers=%v", recursive)
	}
	editor.deleted["dir/file"] = true
	if got := editor.mutationHeaders("dir/file", 7).Get("If"); got != "" {
		t.Fatalf("deleted path lock header=%q", got)
	}
}

func TestCommitConflictReturnsAcceptedDAVError(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusConflict, Status: "409 Conflict",
		Body: io.NopCloser(strings.NewReader(`<D:error xmlns:D="DAV:" xmlns:S="svn:"><S:human-readable>out of date: file</S:human-readable></D:error>`)),
	}
	err := responseError(response)
	if !errors.Is(err, svn.ErrRADAVRequestFailed) || !strings.Contains(err.Error(), "out of date: file") {
		t.Fatalf("error=%v", err)
	}
}

func setHTTPv2Headers(header http.Header, withProps bool) {
	header.Set("SVN-Repository-Root", "/repo")
	header.Set("SVN-Repository-UUID", "uuid")
	header.Set("SVN-Youngest-Rev", "0")
	header.Set("SVN-Me-Resource", "/repo/!svn/me")
	header.Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
	header.Set("SVN-Rev-Stub", "/repo/!svn/rev")
	header.Set("SVN-Txn-Root-Stub", "/repo/!svn/txr")
	header.Set("SVN-Txn-Stub", "/repo/!svn/txn")
	if withProps {
		header.Set("SVN-Supported-Posts", "create-txn-with-props")
	}
}
