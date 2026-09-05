package servers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessEarlyExitCanBeStopped(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	serverProcess := startProcess(t, executable, "-test.run=^TestProcessHelperExit$")
	address, _, err := reserveAddress("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := serverProcess.waitForTCP(address, time.Second); err == nil {
		t.Fatal("waitForTCP succeeded after the process exited")
	}
	serverProcess.stop()
}

func TestProcessHelperExit(t *testing.T) {}

func TestSvnserveOptionsDefaults(t *testing.T) {
	var options SvnserveOptions
	options.applyDefaults()
	if options.Host != "127.0.0.1" || options.Username == "" || options.Password == "" {
		t.Fatalf("defaults not applied: %+v", options)
	}
	if options.AnonymousAccess != AccessRead || options.AuthenticatedAccess != AccessWrite {
		t.Fatalf("access defaults not applied: %+v", options)
	}
}

func TestValidateAccess(t *testing.T) {
	for _, access := range []Access{AccessNone, AccessRead, AccessWrite} {
		if err := validateAccess(access); err != nil {
			t.Fatalf("validateAccess(%q): %v", access, err)
		}
	}
	if err := validateAccess("invalid"); err == nil {
		t.Fatal("validateAccess accepted an invalid value")
	}
}

func TestWriteSvnserveConfig(t *testing.T) {
	repository := t.TempDir()
	options := SvnserveOptions{
		Username:            "alice",
		Password:            "secret",
		Realm:               "example",
		AnonymousAccess:     AccessNone,
		AuthenticatedAccess: AccessWrite,
	}
	writeSvnserveConfig(t, repository, options)

	config := readTestFile(t, filepath.Join(repository, "conf", "svnserve.conf"))
	for _, line := range []string{"anon-access = none", "auth-access = write", "password-db = passwd", "realm = example"} {
		if !strings.Contains(config, line) {
			t.Fatalf("svnserve.conf does not contain %q:\n%s", line, config)
		}
	}
	passwords := readTestFile(t, filepath.Join(repository, "conf", "passwd"))
	if !strings.Contains(passwords, "alice = secret") {
		t.Fatalf("passwd has unexpected content:\n%s", passwords)
	}
}

func TestShellQuote(t *testing.T) {
	if got, want := shellQuote("a'b"), `'a'\''b'`; got != want {
		t.Fatalf("shellQuote() = %q, want %q", got, want)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
