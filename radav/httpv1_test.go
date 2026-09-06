package radav

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestHTTPv1BaselineDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case "OPTIONS":
		case "PROPFIND":
			writer.Header().Set("Content-Type", "application/xml")
			writer.WriteHeader(http.StatusMultiStatus)
			switch request.URL.Path {
			case "/repo":
				_, _ = io.WriteString(writer, multistatusXML(propResponseXML("/repo", `<D:version-controlled-configuration><D:href>/repo/!svn/vcc/default</D:href></D:version-controlled-configuration><V:repository-uuid>legacy-uuid</V:repository-uuid><V:baseline-relative-path></V:baseline-relative-path>`)))
			case "/repo/!svn/vcc/default":
				_, _ = io.WriteString(writer, multistatusXML(propResponseXML(request.URL.Path, `<D:checked-in><D:href>/repo/!svn/bln/4</D:href></D:checked-in>`)))
			case "/repo/!svn/bln/4":
				_, _ = io.WriteString(writer, multistatusXML(propResponseXML(request.URL.Path, `<D:version-name>4</D:version-name><D:baseline-collection><D:href>/repo/!svn/bc/4</D:href></D:baseline-collection>`)))
			case "/repo/!svn/bc/4/file":
				_, _ = io.WriteString(writer, multistatusXML(propResponseXML(request.URL.Path, `<D:resourcetype/><D:version-name>4</D:version-name>`)))
			default:
				t.Fatalf("PROPFIND path=%q", request.URL.Path)
			}
		case "GET":
			if request.URL.Path != "/repo/!svn/bc/4/file" {
				t.Fatalf("GET path=%q", request.URL.Path)
			}
			_, _ = io.WriteString(writer, "content")
		default:
			t.Fatalf("method=%s", request.Method)
		}
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	ctx := context.Background()
	root, err := session.RepositoryRoot(ctx)
	if err != nil || root != server.URL+"/repo" {
		t.Fatalf("root=%q error=%v", root, err)
	}
	uuid, err := session.UUID(ctx)
	if err != nil || uuid != "legacy-uuid" {
		t.Fatalf("uuid=%q error=%v", uuid, err)
	}
	latest, err := session.LatestRevision(ctx)
	if err != nil || latest != 4 {
		t.Fatalf("latest=%d error=%v", latest, err)
	}
	var output bytes.Buffer
	revision, _, err := session.GetFile(ctx, "file", svn.InvalidRevnum, &output, false)
	if err != nil || revision != 4 || output.String() != "content" {
		t.Fatalf("revision=%d output=%q error=%v", revision, output.String(), err)
	}
}
