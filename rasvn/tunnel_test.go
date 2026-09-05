package rasvn

import (
	"io"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestSplitTunnelCommand(t *testing.T) {
	got, err := splitCommand(`ssh -i "key with spaces" 'host alias' escaped\ value`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "-i", "key with spaces", "host alias", "escaped value"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments = %#v, want %#v", got, want)
	}
	if _, err := splitCommand(`ssh "unterminated`); err == nil {
		t.Fatal("expected unterminated quote error")
	}
}

func TestProcessStreamCloseKillsUnresponsiveChild(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestTunnelUnresponsiveHelper")
	command.Env = append(os.Environ(), "GOSVN_UNRESPONSIVE_TUNNEL=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	stream := &processStream{Reader: stdout, Writer: stdin, command: command}
	started := time.Now()
	_ = stream.Close()
	elapsed := time.Since(started)
	if elapsed < 900*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("close took %v", elapsed)
	}
	if command.ProcessState == nil {
		t.Fatalf("child process was not reaped: %v", command.ProcessState)
	}
}

func TestTunnelUnresponsiveHelper(t *testing.T) {
	if os.Getenv("GOSVN_UNRESPONSIVE_TUNNEL") == "" {
		return
	}
	_, _ = io.Copy(io.Discard, blockingReader{})
}

type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) {
	time.Sleep(30 * time.Second)
	return 0, nil
}

func TestResolveTunnelCommandEnvironmentOverride(t *testing.T) {
	t.Setenv("GOSVN_TEST_TUNNEL", `custom --flag`)
	if got, err := resolveTunnelCommand(`$GOSVN_TEST_TUNNEL fallback -q`); err != nil || got != `custom --flag` {
		t.Fatalf("override = %q, error=%v", got, err)
	}
	t.Setenv("GOSVN_TEST_TUNNEL", "")
	if got, err := resolveTunnelCommand(`$GOSVN_TEST_TUNNEL fallback -q`); err != nil || got != `fallback -q` {
		t.Fatalf("fallback = %q, error=%v", got, err)
	}
	if _, err := resolveTunnelCommand(`$GOSVN_TEST_TUNNEL`); err == nil {
		t.Fatal("expected missing override error")
	}
}

func TestTunnelHostInfoPreservesPort(t *testing.T) {
	parsed, err := url.Parse("svn+ssh://alice@example.invalid:2222/repo")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tunnelHostInfo(parsed); err != nil || got != "alice@example.invalid:2222" {
		t.Fatalf("hostinfo=%q error=%v", got, err)
	}
	invalid, _ := url.Parse("svn+ssh://bad%20user@example.invalid/repo")
	if _, err := tunnelHostInfo(invalid); err == nil {
		t.Fatal("expected invalid hostinfo error")
	}
}
