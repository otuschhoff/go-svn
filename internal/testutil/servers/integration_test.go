//go:build integration

package servers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oliver-tuschhoff/go-svn/internal/testutil"
)

func TestStartSvnserve(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	repository := createRepository(t, t.TempDir(), "repo")
	server := StartSvnserve(t, repository, SvnserveOptions{})
	runSVNInfo(t, server.URL, "", "", false)
	server.Close()
}

func TestTunnelScript(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	repository := createRepository(t, t.TempDir(), "repo")
	tunnel := TunnelScript(t, repository)

	svn := testutil.RequireTool(t, "svn", "GOSVN_SVN")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, svn,
		"--non-interactive", "--no-auth-cache", "--config-dir", t.TempDir(),
		"info", "svn+ssh://example.invalid/",
	)
	command.Env = append(os.Environ(), "SVN_SSH="+tunnel)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("svn info through tunnel: %v:\n%s", err, output)
	}
}

func TestStartHTTPD(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	tests := []struct {
		name string
		auth HTTPAuth
		tls  bool
		bulk BulkUpdates
	}{
		{name: "basic-prefer", auth: HTTPAuthBasic, bulk: BulkUpdatesPrefer},
		{name: "digest-off", auth: HTTPAuthDigest, bulk: BulkUpdatesOff},
		{name: "tls-on", auth: HTTPAuthBasic, tls: true, bulk: BulkUpdatesOn},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			createRepository(t, parent, "repo")
			server := StartHTTPD(t, parent, HTTPDOptions{Auth: test.auth, TLS: test.tls, BulkUpdates: test.bulk})
			runSVNInfo(t, server.URL+"repo", server.Username, server.Password, test.tls)
			server.Close()
		})
	}
}

func TestDockerFallback(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	if !dockerRequested() {
		t.Skip("Docker fallback requires GOSVN_DOCKER=1")
	}
	parent := t.TempDir()
	createRepository(t, parent, "repo")
	servers := StartDockerServers(t, parent, DockerOptions{})
	runSVNInfo(t, servers.SvnURL+"repo", "", "", false)
	runSVNInfo(t, servers.HTTPURL+"repo", servers.Username, servers.Password, false)
}

func createRepository(t testing.TB, parent, name string) string {
	t.Helper()
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	repository := filepath.Join(parent, name)
	command := exec.Command(svnadmin, "create", "--fs-type", "fsfs", repository)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	return repository
}

func runSVNInfo(t testing.TB, repositoryURL, username, password string, trustCertificate bool) {
	t.Helper()
	svn := testutil.RequireTool(t, "svn", "GOSVN_SVN")
	arguments := []string{"--non-interactive", "--no-auth-cache", "--config-dir", t.TempDir()}
	if username != "" {
		arguments = append(arguments, "--username", username, "--password", password)
	}
	if trustCertificate {
		arguments = append(arguments, "--trust-server-cert-failures", "unknown-ca,cn-mismatch,expired,not-yet-valid,other")
	}
	arguments = append(arguments, "info", repositoryURL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, svn, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("svn info %s: %v:\n%s", repositoryURL, err, output)
	}
	if !strings.Contains(string(output), "Repository UUID:") {
		t.Fatalf("svn info %s returned unexpected output:\n%s", repositoryURL, output)
	}
	if ctx.Err() != nil {
		t.Fatal(fmt.Errorf("svn info timeout: %w", ctx.Err()))
	}
}
