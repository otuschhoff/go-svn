//go:build integration

package radav_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oliver-tuschhoff/go-svn/auth"
	"github.com/oliver-tuschhoff/go-svn/config"
	"github.com/oliver-tuschhoff/go-svn/internal/testutil"
	"github.com/oliver-tuschhoff/go-svn/internal/testutil/servers"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/ra/conformance"
	_ "github.com/oliver-tuschhoff/go-svn/radav"
)

func TestSessionAgainstHTTPD(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	for _, authentication := range []servers.HTTPAuth{servers.HTTPAuthBasic, servers.HTTPAuthDigest} {
		for _, tlsEnabled := range []bool{false, true} {
			for _, bulk := range []servers.BulkUpdates{servers.BulkUpdatesOn, servers.BulkUpdatesOff, servers.BulkUpdatesPrefer} {
				name := string(authentication) + "-" + string(bulk)
				if tlsEnabled {
					name += "-tls"
				}
				t.Run(name, func(t *testing.T) {
					svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
					root := t.TempDir()
					readRepository := filepath.Join(root, "read")
					if output, err := exec.Command(svnadmin, "create", readRepository).CombinedOutput(); err != nil {
						t.Fatalf("svnadmin create: %v:\n%s", err, output)
					}
					dump, err := os.Open("../testdata/repos/basic.dump")
					if err != nil {
						t.Fatal(err)
					}
					command := exec.Command(svnadmin, "load", "--quiet", readRepository)
					command.Stdin = dump
					if output, err := command.CombinedOutput(); err != nil {
						t.Fatalf("svnadmin load: %v:\n%s", err, output)
					}
					if err := dump.Close(); err != nil {
						t.Fatal(err)
					}
					writeRepository := filepath.Join(root, "write")
					if output, err := exec.Command(svnadmin, "create", writeRepository).CombinedOutput(); err != nil {
						t.Fatalf("svnadmin create: %v:\n%s", err, output)
					}
					hook := filepath.Join(writeRepository, "hooks", "pre-revprop-change")
					if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
						t.Fatal(err)
					}
					server := servers.StartHTTPD(t, root, servers.HTTPDOptions{Auth: authentication, TLS: tlsEnabled, BulkUpdates: bulk})
					defer server.Close()
					callbacks := &ra.Callbacks{Config: config.New()}
					callbacks.Auth.Prompt.Simple = func(context.Context, string, string, bool) (*auth.Credentials, error) {
						return &auth.Credentials{Username: server.Username, Password: server.Password}, nil
					}
					callbacks.Auth.Prompt.SSLServerTrust = func(_ context.Context, _ string, failures uint32, _ string, _ bool) (*auth.Credentials, error) {
						return &auth.Credentials{Failures: failures}, nil
					}
					open := func(rawURL string) func(*testing.T) ra.Session {
						return func(t *testing.T) ra.Session {
							session, _, err := ra.Open(context.Background(), rawURL, callbacks)
							if err != nil {
								t.Fatal(err)
							}
							return session
						}
					}
					readURL := strings.TrimSuffix(server.URL, "/") + "/read"
					openRead := open(readURL)
					session := openRead(t)
					uuid, err := session.UUID(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					_ = session.Close()
					conformance.Run(t, conformance.Fixture{
						Open: openRead, RootURL: readURL, UUID: uuid, Latest: 4,
						FilePath: "trunk/README.txt", OldContent: "go-svn fixture\nsecond line\n", Content: "go-svn fixture\nsecond line\n", Deleted: "trunk/run.sh",
						LatestTime: time.Date(2020, 1, 5, 0, 0, 0, 0, time.UTC), RevProp: "svn:log", RevValue: "delete executable",
					})
					writeURL := strings.TrimSuffix(server.URL, "/") + "/write"
					conformance.RunWrites(t, conformance.WriteFixture{Open: open(writeURL), RootURL: writeURL})
					if output, err := exec.Command(svnadmin, "verify", "--quiet", writeRepository).CombinedOutput(); err != nil {
						t.Fatalf("svnadmin verify: %v:\n%s", err, output)
					}
				})
			}
		}
	}
}
