package radav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

func TestOpenDiscoversRedirectedRepository(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/repo" {
			http.Redirect(writer, request, "/repo/", http.StatusMovedPermanently)
			return
		}
		if request.Method != "OPTIONS" || request.Header.Get("User-Agent") != userAgent {
			t.Fatalf("request=%s user-agent=%q", request.Method, request.Header.Get("User-Agent"))
		}
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), "activity-collection-set") {
			t.Fatalf("body=%q", body)
		}
		writer.Header().Set("SVN-Repository-Root", "/repo/")
		writer.Header().Set("SVN-Repository-UUID", "uuid")
		writer.Header().Set("SVN-Youngest-Rev", "12")
		writer.Header().Set("SVN-Me-Resource", "/repo/!svn/me")
		writer.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
		writer.Header().Set("DAV", "1, 2, http://subversion.tigris.org/xmlns/dav/svn/depth, http://subversion.tigris.org/xmlns/dav/svn/mergeinfo, http://subversion.tigris.org/xmlns/dav/svn/svndiff2")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	session, corrected, err := openSession(context.Background(), mustURL(t, server.URL+"/repo"), &ra.Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if corrected != server.URL+"/repo/" || session.URL() != corrected {
		t.Fatalf("corrected=%q session=%q", corrected, session.URL())
	}
	if root, _ := session.RepositoryRoot(context.Background()); root != server.URL+"/repo/" {
		t.Fatalf("root=%q", root)
	}
	if uuid, _ := session.UUID(context.Background()); uuid != "uuid" {
		t.Fatalf("uuid=%q", uuid)
	}
	if revision, _ := session.LatestRevision(context.Background()); revision != 12 {
		t.Fatalf("revision=%d", revision)
	}
	if capable, _ := session.HasCapability(context.Background(), ra.CapabilityDepth); !capable {
		t.Fatal("depth capability not discovered")
	}
	if capable, _ := session.HasCapability(context.Background(), ra.CapabilitySvndiff2); !capable {
		t.Fatal("svndiff2 capability not discovered")
	}
}

func TestDiscoveryRejectsMalformedYoungestRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("SVN-Youngest-Rev", "bad")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	_, _, err := openSession(context.Background(), mustURL(t, server.URL), &ra.Callbacks{})
	if !errors.Is(err, svn.ErrRADAVResponseHeaderBadness) {
		t.Fatalf("error=%v", err)
	}
}

func TestResponseErrorUsesSVNCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(writer, `<D:error xmlns:D="DAV:" xmlns:S="svn:"><S:human-readable errcode="160013">missing path</S:human-readable></D:error>`)
	}))
	defer server.Close()
	transport, err := newTransport(nil, "example.test")
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.request(context.Background(), "GET", server.URL, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = responseError(response)
	drainAndClose(response.Body)
	if !errors.Is(err, svn.Code(160013)) || !strings.Contains(err.Error(), "missing path") {
		t.Fatalf("error=%v", err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
