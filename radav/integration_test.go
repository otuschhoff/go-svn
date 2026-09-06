//go:build integration

package radav_test

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oliver-tuschhoff/go-svn/internal/testutil"
	"github.com/oliver-tuschhoff/go-svn/internal/testutil/servers"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/ra/conformance"
	_ "github.com/oliver-tuschhoff/go-svn/radav"
)

func TestReadSessionAgainstHTTPD(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	for _, bulk := range []servers.BulkUpdates{servers.BulkUpdatesOn, servers.BulkUpdatesOff, servers.BulkUpdatesPrefer} {
		t.Run(string(bulk), func(t *testing.T) {
			svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
			root := t.TempDir()
			repository := filepath.Join(root, "repo")
			if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
				t.Fatalf("svnadmin create: %v:\n%s", err, output)
			}
			dump, err := os.Open("../testdata/repos/basic.dump")
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(svnadmin, "load", "--quiet", repository)
			command.Stdin = dump
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("svnadmin load: %v:\n%s", err, output)
			}
			if err := dump.Close(); err != nil {
				t.Fatal(err)
			}
			server := servers.StartHTTPD(t, root, servers.HTTPDOptions{BulkUpdates: bulk})
			defer server.Close()
			rootURL := strings.TrimSuffix(server.URL, "/") + "/repo"
			client := &http.Client{Transport: basicTransport{username: server.Username, password: server.Password, next: http.DefaultTransport}}
			open := func(t *testing.T) ra.Session {
				session, _, err := ra.Open(context.Background(), rootURL, &ra.Callbacks{HTTP: client})
				if err != nil {
					t.Fatal(err)
				}
				return session
			}
			session := open(t)
			uuid, err := session.UUID(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			_ = session.Close()
			conformance.Run(t, conformance.Fixture{
				Open: open, RootURL: rootURL, UUID: uuid, Latest: 4,
				FilePath: "trunk/README.txt", OldContent: "go-svn fixture\nsecond line\n", Content: "go-svn fixture\nsecond line\n", Deleted: "trunk/run.sh",
				LatestTime: time.Date(2020, 1, 5, 0, 0, 0, 0, time.UTC), RevProp: "svn:log", RevValue: "delete executable",
			})
		})
	}
}

type basicTransport struct {
	username string
	password string
	next     http.RoundTripper
}

func (transport basicTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.SetBasicAuth(transport.username, transport.password)
	return transport.next.RoundTrip(copy)
}
