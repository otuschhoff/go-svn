//go:build integration

package wc

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/auth"
	"github.com/otuschhoff/go-svn/config"
	"github.com/otuschhoff/go-svn/internal/testutil"
	"github.com/otuschhoff/go-svn/internal/testutil/servers"
	"github.com/otuschhoff/go-svn/ra"
	_ "github.com/otuschhoff/go-svn/radav"
	_ "github.com/otuschhoff/go-svn/rasvn"
)

func TestProtocolWorkingCopyMatrices(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	t.Run("svn", func(t *testing.T) {
		t.Run("depth", func(t *testing.T) {
			repository := filepath.Join(t.TempDir(), "repository")
			seedProtocolDepthRepositoryAt(t, repository)
			server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{AnonymousAccess: servers.AccessWrite})
			defer server.Close()
			open := func(t *testing.T) ra.Session {
				return openProtocolSession(t, server.URL+"trunk/", protocolCallbacks(server.Username, server.Password))
			}
			runCheckoutDepthMatrix(t, open)
			runSetDepthMatrix(t, open)
			runUpdateDepthMatrix(t, open)
		})
		t.Run("commit", func(t *testing.T) {
			repository := seedProtocolCommitRepository(t)
			server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{AnonymousAccess: servers.AccessWrite})
			defer server.Close()
			session := openProtocolSession(t, server.URL+"trunk/", protocolCallbacks(server.Username, server.Password))
			defer session.Close()
			runCommitDepthMatrix(t, session, 1)
		})
		t.Run("switch", func(t *testing.T) {
			repository := seedProtocolSwitchRepository(t)
			server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{AnonymousAccess: servers.AccessWrite})
			defer server.Close()
			open := func(t *testing.T) ra.Session {
				return openProtocolSession(t, server.URL+"trunk/", protocolCallbacks(server.Username, server.Password))
			}
			rootURL := strings.TrimSuffix(server.URL, "/")
			runSwitchDepthMatrix(t, rootURL, 3, open)
			runSwitchSetDepthMatrix(t, rootURL, 3, open)
			runSwitchedUpdateDepthMatrix(t, rootURL, 3, 4, open)
		})
	})
	t.Run("dav", func(t *testing.T) {
		root := t.TempDir()
		seedProtocolDepthRepositoryAt(t, filepath.Join(root, "depth"))
		seedProtocolCommitRepositoryAt(t, filepath.Join(root, "commit"))
		seedProtocolSwitchRepositoryAt(t, filepath.Join(root, "switch"))
		serverURL, username, password, closeServer := startProtocolDAV(t, root)
		defer closeServer()
		callbacks := protocolCallbacks(username, password)
		t.Run("depth", func(t *testing.T) {
			open := func(t *testing.T) ra.Session {
				return openProtocolSession(t, serverURL+"depth/trunk", callbacks)
			}
			runCheckoutDepthMatrix(t, open)
			runSetDepthMatrix(t, open)
			runUpdateDepthMatrix(t, open)
		})
		t.Run("commit", func(t *testing.T) {
			session := openProtocolSession(t, serverURL+"commit/trunk", callbacks)
			defer session.Close()
			runCommitDepthMatrix(t, session, 1)
		})
		t.Run("switch", func(t *testing.T) {
			open := func(t *testing.T) ra.Session { return openProtocolSession(t, serverURL+"switch/trunk", callbacks) }
			rootURL := strings.TrimSuffix(serverURL, "/") + "/switch"
			runSwitchDepthMatrix(t, rootURL, 3, open)
			runSwitchSetDepthMatrix(t, rootURL, 3, open)
			runSwitchedUpdateDepthMatrix(t, rootURL, 3, 4, open)
		})
	})
}

func startProtocolDAV(t *testing.T, repositoriesRoot string) (string, string, string, func()) {
	t.Helper()
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOSVN_DOCKER"))) {
	case "1", "true", "yes":
		server := servers.StartDockerServers(t, repositoriesRoot, servers.DockerOptions{})
		closeServer := func() {
			if t.Failed() {
				t.Logf("Docker integration server output:\n%s", server.Output())
			}
			server.Close()
		}
		return server.HTTPURL, server.Username, server.Password, closeServer
	default:
		server := servers.StartHTTPD(t, repositoriesRoot, servers.HTTPDOptions{Auth: servers.HTTPAuthBasic})
		return server.URL, server.Username, server.Password, server.Close
	}
}

func seedProtocolDepthRepositoryAt(t *testing.T, repository string) {
	t.Helper()
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	svnTool := testutil.RequireTool(t, "svn", "GOSVN_SVN")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(filepath.Join(seed, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"root-file": "root one\n", "root-deleted": "delete root\n",
		"dir/child": "child one\n", "dir/child-deleted": "delete child\n",
	} {
		if err := os.WriteFile(filepath.Join(seed, filepath.FromSlash(name)), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repositoryURL := fileURL(t, repository)
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "seed", seed, repositoryURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v\n%s", err, output)
	}
	author := filepath.Join(t.TempDir(), "author")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL+"/trunk", author).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, output)
	}
	for name, contents := range map[string]string{
		"root-file": "root two\n", "root-added": "add root\n",
		"dir/child": "child two\n", "dir/child-added": "add child\n",
	} {
		if err := os.WriteFile(filepath.Join(author, filepath.FromSlash(name)), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if output, err := exec.Command(svnTool, "add", "-q", filepath.Join(author, "root-added"), filepath.Join(author, "dir", "child-added")).CombinedOutput(); err != nil {
		t.Fatalf("svn add: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "delete", "-q", filepath.Join(author, "root-deleted"), filepath.Join(author, "dir", "child-deleted")).CombinedOutput(); err != nil {
		t.Fatalf("svn delete: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "update", author).CombinedOutput(); err != nil {
		t.Fatalf("svn commit: %v\n%s", err, output)
	}
}

func seedProtocolCommitRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	seedProtocolCommitRepositoryAt(t, repository)
	return repository
}

func seedProtocolCommitRepositoryAt(t *testing.T, repository string) {
	t.Helper()
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	svnTool := testutil.RequireTool(t, "svn", "GOSVN_SVN")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(filepath.Join(seed, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"root-file": "root base\n", "selected": "selected base\n", "excluded": "excluded base\n", "dir/child": "child base\n",
	} {
		if err := os.WriteFile(filepath.Join(seed, filepath.FromSlash(name)), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repositoryURL := fileURL(t, repository)
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "seed", seed, repositoryURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v\n%s", err, output)
	}
}

func seedProtocolSwitchRepository(t *testing.T) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	seedProtocolSwitchRepositoryAt(t, repository)
	return repository
}

func seedProtocolSwitchRepositoryAt(t *testing.T, repository string) {
	t.Helper()
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	svnTool := testutil.RequireTool(t, "svn", "GOSVN_SVN")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	base := filepath.Join(seed, "trunk", "target")
	if err := os.MkdirAll(filepath.Join(base, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "root-file"), []byte("trunk root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "dir", "child"), []byte("trunk child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repositoryURL := fileURL(t, repository)
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "seed", seed, repositoryURL).CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "copy", "-q", "--parents", "-m", "branch", repositoryURL+"/trunk/target", repositoryURL+"/branches/other").CombinedOutput(); err != nil {
		t.Fatalf("svn copy: %v\n%s", err, output)
	}
	author := filepath.Join(t.TempDir(), "author")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL+"/branches/other", author).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, output)
	}
	for index, prefix := range []string{"branch", "branch two"} {
		if err := os.WriteFile(filepath.Join(author, "root-file"), []byte(prefix+" root\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(author, "dir", "child"), []byte(prefix+" child\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(svnTool, "commit", "-q", "-m", "revision", author).CombinedOutput(); err != nil {
			t.Fatalf("svn commit r%d: %v\n%s", index+3, err, output)
		}
	}
}

func protocolCallbacks(username, password string) *ra.Callbacks {
	callbacks := &ra.Callbacks{Config: config.New()}
	callbacks.Auth.Prompt.Simple = func(context.Context, string, string, bool) (*auth.Credentials, error) {
		return &auth.Credentials{Username: username, Password: password}, nil
	}
	return callbacks
}

func openProtocolSession(t *testing.T, rawURL string, callbacks *ra.Callbacks) ra.Session {
	t.Helper()
	session, _, err := ra.Open(context.Background(), rawURL, callbacks)
	if err != nil {
		t.Fatal(err)
	}
	return session
}
