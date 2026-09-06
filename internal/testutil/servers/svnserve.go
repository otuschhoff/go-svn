package servers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otuschhoff/go-svn/internal/testutil"
)

type Access string

const (
	AccessNone  Access = "none"
	AccessRead  Access = "read"
	AccessWrite Access = "write"
)

type SvnserveOptions struct {
	Path                string
	Host                string
	Username            string
	Password            string
	Realm               string
	AnonymousAccess     Access
	AuthenticatedAccess Access
	StartupTimeout      time.Duration
}

type Svnserve struct {
	URL      string
	Username string
	Password string
	Address  string
	process  *process
}

func StartSvnserve(t testing.TB, repositoryRoot string, options SvnserveOptions) *Svnserve {
	t.Helper()
	if options.Path == "" {
		options.Path = testutil.RequireTool(t, "svnserve", "GOSVN_SVNSERVE")
	}
	options.applyDefaults()
	if err := validateAccess(options.AnonymousAccess); err != nil {
		t.Fatal(err)
	}
	if err := validateAccess(options.AuthenticatedAccess); err != nil {
		t.Fatal(err)
	}
	repositoryRoot = absoluteDirectory(t, repositoryRoot)
	writeSvnserveConfig(t, repositoryRoot, options)

	address, port, err := reserveAddress(options.Host)
	if err != nil {
		t.Fatalf("reserve svnserve address: %v", err)
	}
	serverProcess := startProcess(t, options.Path,
		"--foreground", "-d",
		"--listen-host", options.Host,
		"--listen-port", port,
		"-r", repositoryRoot,
	)
	if err := serverProcess.waitForTCP(address, options.StartupTimeout); err != nil {
		serverProcess.stop()
		t.Fatal(err)
	}

	return &Svnserve{
		URL:      "svn://" + address + "/",
		Username: options.Username,
		Password: options.Password,
		Address:  address,
		process:  serverProcess,
	}
}

func (s *Svnserve) Close() {
	if s != nil && s.process != nil {
		s.process.stop()
	}
}

func (s *Svnserve) Output() string {
	if s == nil || s.process == nil {
		return ""
	}
	return s.process.output.String()
}

func (options *SvnserveOptions) applyDefaults() {
	if options.Host == "" {
		options.Host = "127.0.0.1"
	}
	if options.Username == "" {
		options.Username = "fixture"
	}
	if options.Password == "" {
		options.Password = "fixture"
	}
	if options.Realm == "" {
		options.Realm = "go-svn integration"
	}
	if options.AnonymousAccess == "" {
		options.AnonymousAccess = AccessRead
	}
	if options.AuthenticatedAccess == "" {
		options.AuthenticatedAccess = AccessWrite
	}
}

func validateAccess(access Access) error {
	switch access {
	case AccessNone, AccessRead, AccessWrite:
		return nil
	default:
		return fmt.Errorf("invalid svnserve access %q", access)
	}
}

func writeSvnserveConfig(t testing.TB, repositoryRoot string, options SvnserveOptions) {
	t.Helper()
	if strings.ContainsAny(options.Username+options.Password+options.Realm, "\r\n") {
		t.Fatal("svnserve credentials and realm must not contain newlines")
	}
	confDir := filepath.Join(repositoryRoot, "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("create svnserve config directory: %v", err)
	}
	config := fmt.Sprintf("[general]\nanon-access = %s\nauth-access = %s\npassword-db = passwd\nrealm = %s\n", options.AnonymousAccess, options.AuthenticatedAccess, options.Realm)
	if err := os.WriteFile(filepath.Join(confDir, "svnserve.conf"), []byte(config), 0o600); err != nil {
		t.Fatalf("write svnserve.conf: %v", err)
	}
	passwords := fmt.Sprintf("[users]\n%s = %s\n", options.Username, options.Password)
	if err := os.WriteFile(filepath.Join(confDir, "passwd"), []byte(passwords), 0o600); err != nil {
		t.Fatalf("write svnserve passwd: %v", err)
	}
}

func absoluteDirectory(t testing.TB, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("make path absolute: %v", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		t.Fatalf("stat %s: %v", absolute, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", absolute)
	}
	return absolute
}
