package radav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

func TestLockUnlockAndAtomicRevpropRequests(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		methods = append(methods, request.Method+" "+request.URL.Path)
		switch request.Method {
		case http.MethodOptions:
			writer.Header().Set("SVN-Repository-Root", "/repo")
			writer.Header().Set("SVN-Repository-UUID", "uuid")
			writer.Header().Set("SVN-Youngest-Rev", "3")
			writer.Header().Set("SVN-Me-Resource", "/repo/!svn/me")
			writer.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
			writer.Header().Set("SVN-Rev-Stub", "/repo/!svn/rev")
			writer.Header().Set("SVN-Txn-Root-Stub", "/repo/!svn/txr")
			writer.Header().Set("SVN-Txn-Stub", "/repo/!svn/txn")
			writer.Header().Set("DAV", nsDAVSVN+"svn/atomic-revprops")
		case "LOCK":
			if request.Header.Get("X-SVN-Version-Name") != "3" || request.Header.Get("Timeout") != "Infinite" || !strings.Contains(string(body), "comment") {
				t.Errorf("LOCK headers=%v body=%s", request.Header, body)
			}
			writer.Header().Set("X-SVN-Lock-Owner", "alice")
			writer.Header().Set("X-SVN-Creation-Date", "2026-01-02T03:04:05.000000Z")
			_, _ = io.WriteString(writer, `<D:prop xmlns:D="DAV:"><D:lockdiscovery><D:activelock><D:timeout>Infinite</D:timeout><D:locktoken><D:href>opaquelocktoken:one</D:href></D:locktoken><D:owner>comment</D:owner></D:activelock></D:lockdiscovery></D:prop>`)
		case "UNLOCK":
			if request.Header.Get("Lock-Token") != "<opaquelocktoken:one>" || request.Header.Get("X-SVN-Options") != "lock-break" {
				t.Errorf("UNLOCK headers=%v", request.Header)
			}
			writer.WriteHeader(http.StatusNoContent)
		case "PROPPATCH":
			if request.URL.Path != "/repo/!svn/rev/3" || !strings.Contains(string(body), `<V:old-value>old</V:old-value>`) {
				t.Errorf("PROPPATCH path=%s body=%s", request.URL.Path, body)
			}
			writer.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(writer, multistatusXML(propResponseXML(request.URL.Path, "")))
		}
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")

	var locked *svn.Lock
	if err := session.Lock(context.Background(), map[string]svn.Revnum{"file": 3}, "comment", false, func(path string, lock *svn.Lock, err error) error {
		if path != "file" || err != nil {
			t.Fatalf("lock callback path=%q error=%v", path, err)
		}
		locked = lock
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if locked == nil || locked.Token != "opaquelocktoken:one" || locked.Owner != "alice" || locked.Comment != "comment" {
		t.Fatalf("lock=%#v", locked)
	}
	if err := session.Unlock(context.Background(), map[string]string{"file": locked.Token}, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.ChangeRevProp(context.Background(), 3, "custom:prop", []byte("new"), []byte("old"), false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(methods[1:], ","); got != "LOCK /repo/file,UNLOCK /repo/file,PROPPATCH /repo/!svn/rev/3" {
		t.Fatalf("methods=%s", got)
	}
}

func TestLockCallbacksContinueAfterPathFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodOptions {
			writer.Header().Set("SVN-Repository-Root", "/repo")
			writer.Header().Set("SVN-Me-Resource", "/repo/!svn/me")
			writer.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
			return
		}
		writer.WriteHeader(http.StatusLocked)
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	var callbacks int
	err := session.Lock(context.Background(), map[string]svn.Revnum{"a": 1, "b": 1}, "", false, func(_ string, _ *svn.Lock, pathErr error) error {
		callbacks++
		if pathErr == nil {
			t.Fatal("missing per-path error")
		}
		return nil
	})
	if err != nil || callbacks != 2 {
		t.Fatalf("callbacks=%d error=%v", callbacks, err)
	}
}

func TestLockStatusErrors(t *testing.T) {
	tests := map[int]svn.Code{
		http.StatusBadRequest:       svn.ErrFSNoSuchLock,
		http.StatusForbidden:        svn.ErrFSLockOwnerMismatch,
		http.StatusMethodNotAllowed: svn.ErrFSOutOfDate,
		http.StatusLocked:           svn.ErrFSPathAlreadyLocked,
	}
	for status, want := range tests {
		response := &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(""))}
		if err := lockStatusError(response, "file"); !errors.Is(err, want) {
			t.Errorf("status %d: error=%v, want %v", status, err, want)
		}
	}
}

var _ ra.LockCallback
