package servers

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otuschhoff/go-svn/internal/testutil"
)

type DockerOptions struct {
	Username       string
	Password       string
	StartupTimeout time.Duration
}

type DockerServers struct {
	SvnURL   string
	HTTPURL  string
	Username string
	Password string
	close    func()
	once     sync.Once
}

func StartDockerServers(t testing.TB, repositoriesRoot string, options DockerOptions) *DockerServers {
	t.Helper()
	docker := testutil.RequireTool(t, "docker", "GOSVN_DOCKER_BIN")
	if options.Username == "" {
		options.Username = "fixture"
	}
	if options.Password == "" {
		options.Password = "fixture"
	}
	if options.StartupTimeout <= 0 {
		options.StartupTimeout = 2 * time.Minute
	}
	repositoriesRoot = absoluteDirectory(t, repositoriesRoot)
	composePath := dockerComposePath(t)

	if output, err := exec.Command(docker, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		t.Skipf("Docker daemon is unavailable: %v: %s", err, output)
	}
	_, svnPort, err := reserveAddress("127.0.0.1")
	if err != nil {
		t.Fatalf("reserve Docker svnserve port: %v", err)
	}
	_, httpPort, err := reserveAddress("127.0.0.1")
	if err != nil {
		t.Fatalf("reserve Docker httpd port: %v", err)
	}
	project := "gosvn-" + strconv.Itoa(os.Getpid()) + "-" + svnPort
	environment := append(os.Environ(),
		"GOSVN_REPOS_ROOT="+repositoriesRoot,
		"GOSVN_SVNSERVE_PORT="+svnPort,
		"GOSVN_HTTPD_PORT="+httpPort,
		"GOSVN_USERNAME="+options.Username,
		"GOSVN_PASSWORD="+options.Password,
	)
	composeArgs := []string{"compose", "-f", composePath, "-p", project}
	up := exec.Command(docker, append(composeArgs, "up", "-d", "--build")...)
	up.Env = environment
	if output, err := up.CombinedOutput(); err != nil {
		t.Fatalf("start Docker integration servers: %v:\n%s", err, output)
	}

	closeServers := func() {
		down := exec.Command(docker, append(composeArgs, "down", "--volumes", "--remove-orphans")...)
		down.Env = environment
		_, _ = down.CombinedOutput()
	}
	servers := &DockerServers{
		SvnURL:   "svn://127.0.0.1:" + svnPort + "/",
		HTTPURL:  "http://127.0.0.1:" + httpPort + "/svn/",
		Username: options.Username,
		Password: options.Password,
		close:    closeServers,
	}
	t.Cleanup(servers.Close)

	ctx, cancel := context.WithTimeout(context.Background(), options.StartupTimeout)
	defer cancel()
	for name, address := range map[string]string{
		"svnserve": net.JoinHostPort("127.0.0.1", svnPort),
		"httpd":    net.JoinHostPort("127.0.0.1", httpPort),
	} {
		if err := waitForAddress(ctx, address); err != nil {
			servers.Close()
			t.Fatalf("wait for Docker %s: %v", name, err)
		}
	}
	return servers
}

func (s *DockerServers) Close() {
	if s != nil && s.close != nil {
		s.once.Do(s.close)
	}
}

func dockerComposePath(t testing.TB) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate docker.go")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "..", "testdata", "docker", "compose.yaml"))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("locate Docker Compose file: %v", err)
	}
	return path
}

func waitForAddress(ctx context.Context, address string) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", address, ctx.Err())
		case <-ticker.C:
		}
	}
}

func dockerRequested() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("GOSVN_DOCKER")))
	return value == "1" || value == "true" || value == "yes"
}
