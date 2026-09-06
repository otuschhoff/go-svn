package radav

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestPropfindAndGetReads(t *testing.T) {
	content := []byte("hello dav\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case "OPTIONS":
			writer.Header().Set("SVN-Repository-Root", "/repo")
			writer.Header().Set("SVN-Repository-UUID", "uuid")
			writer.Header().Set("SVN-Youngest-Rev", "3")
			writer.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
			writer.Header().Set("SVN-Rev-Stub", "/repo/!svn/rev")
		case "PROPFIND":
			writer.Header().Set("Content-Type", "application/xml")
			writer.WriteHeader(http.StatusMultiStatus)
			if request.URL.Path == "/repo/!svn/rev/3" {
				_, _ = io.WriteString(writer, multistatusXML(propResponseXML(request.URL.Path, `<D:resourcetype/><D:creator-displayname>alice</D:creator-displayname><D:creationdate>2020-01-01T00:00:00Z</D:creationdate><D:comment>message</D:comment><S:custom>value</S:custom>`)))
				return
			}
			root := propResponseXML("/repo/!svn/rvr/3/trunk", `<D:resourcetype><D:collection/></D:resourcetype><D:version-name>3</D:version-name><V:deadprop-count>1</V:deadprop-count><C:project>yes</C:project>`)
			file := propResponseXML("/repo/!svn/rvr/3/trunk/file.txt", `<D:resourcetype/><D:getcontentlength>10</D:getcontentlength><D:version-name>2</D:version-name><D:creationdate>2020-01-02T00:00:00Z</D:creationdate><D:creator-displayname>bob</D:creator-displayname><V:deadprop-count>1</V:deadprop-count><S:eol-style>LF</S:eol-style>`)
			if request.Header.Get("Depth") == "0" {
				_, _ = io.WriteString(writer, multistatusXML(file))
			} else {
				_, _ = io.WriteString(writer, multistatusXML(root+file))
			}
		case "GET":
			sum := md5.Sum(content)
			writer.Header().Set("Content-MD5", base64.StdEncoding.EncodeToString(sum[:]))
			_, _ = writer.Write(content)
		default:
			t.Fatalf("method=%s", request.Method)
		}
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")

	entries, revision, props, err := session.GetDir(context.Background(), "trunk", 3, svn.DirentAll)
	if err != nil || revision != 3 || len(entries) != 1 || entries[0].Path != "file.txt" || entries[0].Kind != svn.NodeFile || entries[0].Size != 10 || entries[0].CreatedRev != 2 || entries[0].LastAuthor != "bob" || string(props["project"]) != "yes" {
		t.Fatalf("entries=%#v revision=%d props=%v error=%v", entries, revision, props, err)
	}
	var output bytes.Buffer
	fileRevision, fileProps, err := session.GetFile(context.Background(), "trunk/file.txt", 3, &output, true)
	if err != nil || fileRevision != 3 || !bytes.Equal(output.Bytes(), content) || string(fileProps["svn:eol-style"]) != "LF" {
		t.Fatalf("revision=%d content=%q props=%v error=%v", fileRevision, output.Bytes(), fileProps, err)
	}
	revisionProps, err := session.RevProps(context.Background(), 3)
	if err != nil || string(revisionProps["svn:author"]) != "alice" || string(revisionProps["svn:log"]) != "message" || string(revisionProps["svn:custom"]) != "value" {
		t.Fatalf("revision props=%v error=%v", revisionProps, err)
	}
}

func openTestSession(t *testing.T, rawURL string) *Session {
	t.Helper()
	value, _, err := openSession(context.Background(), mustURL(t, rawURL), nil)
	if err != nil {
		t.Fatal(err)
	}
	return value.(*Session)
}

func multistatusXML(responses string) string {
	return `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:" xmlns:S="http://subversion.tigris.org/xmlns/svn/" xmlns:C="http://subversion.tigris.org/xmlns/custom/" xmlns:V="http://subversion.tigris.org/xmlns/dav/">` + responses + `</D:multistatus>`
}

func propResponseXML(href, properties string) string {
	return fmt.Sprintf(`<D:response><D:href>%s</D:href><D:propstat><D:prop>%s</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`, href, properties)
}
