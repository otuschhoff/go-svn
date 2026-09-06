package radav

import (
	"bytes"
	"context"
	"github.com/otuschhoff/go-svn/auth"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otuschhoff/go-svn/config"
	"github.com/otuschhoff/go-svn/ra"
)

func TestTLSTrustPromptIsCachedForSession(t *testing.T) {
	var prompts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Connection", "close")
		if request.Method == "OPTIONS" {
			writer.Header().Set("SVN-Repository-Root", "/repo")
			writer.Header().Set("SVN-Repository-UUID", "uuid")
			writer.Header().Set("SVN-Youngest-Rev", "1")
			writer.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
			return
		}
		_, _ = writer.Write([]byte("trusted"))
	}))
	defer server.Close()
	callbacks := &ra.Callbacks{Config: config.New()}
	callbacks.Auth.Prompt.SSLServerTrust = func(_ context.Context, _ string, failures uint32, certificate string, _ bool) (*auth.Credentials, error) {
		prompts.Add(1)
		if failures&sslUnknownCA == 0 || certificate == "" {
			t.Fatalf("failures=%d certificate=%q", failures, certificate)
		}
		return &auth.Credentials{Failures: failures}, nil
	}
	value, _, err := openSession(context.Background(), mustURL(t, server.URL+"/repo"), callbacks)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, _, err := value.GetFile(context.Background(), "file", 1, &output, false); err != nil {
		t.Fatal(err)
	}
	if output.String() != "trusted" || prompts.Load() != 1 {
		t.Fatalf("output=%q prompts=%d", output.String(), prompts.Load())
	}
}

func TestTransportUsesHostServerGroupAndProxyException(t *testing.T) {
	settings := config.New()
	for _, override := range []string{
		"servers:groups:local=*.example.test",
		"servers:local:http-timeout=17",
		"servers:local:http-max-connections=3",
		"servers:local:http-proxy-host=proxy.example.test",
		"servers:local:http-proxy-port=8080",
		"servers:local:http-proxy-exceptions=api.example.test",
		"servers:local:ssl-trust-default-ca=no",
	} {
		if err := settings.ApplyOverride(override); err != nil {
			t.Fatal(err)
		}
	}
	transport, err := newTransport(&ra.Callbacks{Config: settings}, "api.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if transport.client.Timeout != 17*time.Second {
		t.Fatalf("timeout=%v", transport.client.Timeout)
	}
	httpTransport := transport.client.Transport.(*http.Transport)
	if httpTransport.MaxConnsPerHost != 3 {
		t.Fatalf("max connections=%d", httpTransport.MaxConnsPerHost)
	}
	request := &http.Request{URL: &url.URL{Scheme: "http", Host: "api.example.test"}}
	proxy, err := httpTransport.Proxy(request)
	if err != nil || proxy != nil {
		t.Fatalf("proxy=%v error=%v", proxy, err)
	}
}

func TestTransportRejectsInvalidServersBoolean(t *testing.T) {
	settings := config.New()
	if err := settings.ApplyOverride("servers:global:ssl-trust-default-ca=perhaps"); err != nil {
		t.Fatal(err)
	}
	if _, err := newTransport(&ra.Callbacks{Config: settings}, "example.test"); err == nil {
		t.Fatal("expected invalid boolean error")
	}
}

func TestAppendURLPathEscapesReservedCharacters(t *testing.T) {
	got := appendURLPath("https://example.test/repo", "dir/file #1?.txt")
	if got != "https://example.test/repo/dir/file%20%231%3F.txt" {
		t.Fatalf("URL=%q", got)
	}
}
