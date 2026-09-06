package radav

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/ra"
)

func TestFileRevsStreamsLargeDelta(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789abcdef"), (11<<20)/16+1)
	content = content[:11<<20]
	var svndiff bytes.Buffer
	writer := delta.NewSvndiffWriter(&svndiff, 1, zlib.BestSpeed)
	if _, err := delta.SendContents(content, writer); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == "OPTIONS" {
			response.Header().Set("SVN-Repository-Root", "/repo")
			response.Header().Set("SVN-Repository-UUID", "uuid")
			response.Header().Set("SVN-Youngest-Rev", "1")
			response.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
			return
		}
		_, _ = io.WriteString(response, `<S:file-revs-report xmlns:S="svn:"><S:file-rev path="/large" rev="1"><S:txdelta>`)
		encoder := base64.NewEncoder(base64.StdEncoding, response)
		_, _ = encoder.Write(svndiff.Bytes())
		_ = encoder.Close()
		_, _ = io.WriteString(response, `</S:txdelta></S:file-rev></S:file-revs-report>`)
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	var output bytes.Buffer
	err := session.GetFileRevs(context.Background(), "large", 1, 1, false, func(_ context.Context, revision ra.FileRevision) error {
		if revision.Delta == nil {
			t.Fatal("missing delta")
		}
		_, err := delta.Apply(nil, &output, revision.Delta, nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), content) {
		t.Fatalf("content length=%d, want %d", output.Len(), len(content))
	}
}

func FuzzMultistatusXML(fuzzer *testing.F) {
	fuzzer.Add([]byte(multistatusXML(propResponseXML("/repo", `<D:version-name>1</D:version-name>`))))
	fuzzer.Add([]byte(`<D:multistatus xmlns:D="DAV:"><D:response>`))
	fuzzer.Fuzz(func(t *testing.T, data []byte) {
		var response multistatus
		_ = xml.Unmarshal(data, &response)
		for _, item := range response.Responses {
			_, _ = versionedProps(successfulProperties(item))
		}
	})
}
