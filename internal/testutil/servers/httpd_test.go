package servers

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPDOptions(t *testing.T) {
	var options HTTPDOptions
	options.applyDefaults()
	if options.Auth != HTTPAuthBasic || options.BulkUpdates != BulkUpdatesPrefer {
		t.Fatalf("unexpected defaults: %+v", options)
	}
	if err := options.validate(); err != nil {
		t.Fatal(err)
	}
	options.Auth = "unknown"
	if err := options.validate(); err == nil {
		t.Fatal("invalid auth type was accepted")
	}
}

func TestWriteHTTPPasswordFileBasic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")
	writeHTTPPasswordFile(t, path, HTTPDOptions{Auth: HTTPAuthBasic, Username: "alice", Password: "secret"})
	if got := readTestFile(t, path); !strings.HasPrefix(got, "alice:{SHA}") {
		t.Fatalf("unexpected Basic password file: %q", got)
	}
}

func TestWriteHTTPPasswordFileDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "passwd")
	options := HTTPDOptions{Auth: HTTPAuthDigest, Username: "alice", Password: "secret", Realm: "example"}
	writeHTTPPasswordFile(t, path, options)
	digest := md5.Sum([]byte("alice:example:secret"))
	want := "alice:example:" + hex.EncodeToString(digest[:]) + "\n"
	if got := readTestFile(t, path); got != want {
		t.Fatalf("Digest password file = %q, want %q", got, want)
	}
}

func TestFindFirstModule(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	path := filepath.Join(second, "mod_dav_svn.so")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, found := findFirstModule([]string{first, second}, []string{"mod_dav_svn.so"})
	if !found || got != path {
		t.Fatalf("findFirstModule() = %q, %t; want %q, true", got, found, path)
	}
}

func TestParseHTTPDDefine(t *testing.T) {
	output := "Server version\n -D HTTPD_ROOT=\"/usr/local/apache\"\n"
	if got := parseHTTPDDefine(output, "HTTPD_ROOT"); got != "/usr/local/apache" {
		t.Fatalf("parseHTTPDDefine() = %q", got)
	}
}

func TestContainsStaticModuleMatchesWholeLine(t *testing.T) {
	output := "Compiled in modules:\n  mod_dav_svn.c\n"
	if containsStaticModule(output, "mod_dav") {
		t.Fatal("mod_dav_svn.c was mistaken for mod_dav.c")
	}
	if !containsStaticModule(output, "mod_dav_svn") {
		t.Fatal("mod_dav_svn.c was not detected")
	}
}

func TestDirectiveQuoteEscapesControlCharacters(t *testing.T) {
	if got, want := directiveQuote("a\n\"b"), `"a\n\"b"`; got != want {
		t.Fatalf("directiveQuote() = %q, want %q", got, want)
	}
}

func TestRenderHTTPDConfig(t *testing.T) {
	options := HTTPDOptions{Host: "127.0.0.1", Auth: HTTPAuthDigest, Realm: "realm", BulkUpdates: BulkUpdatesOff}
	config := renderHTTPDConfig("/tmp/root", "/tmp/repos", "127.0.0.1:1234", "/tmp/passwd", "", "", []string{"LoadModule dav_module \"/tmp/mod_dav.so\""}, options)
	for _, value := range []string{"Listen 127.0.0.1:1234", "SVNParentPath \"/tmp/repos\"", "SVNAllowBulkUpdates Off", "AuthType Digest"} {
		if !strings.Contains(config, value) {
			t.Fatalf("config does not contain %q:\n%s", value, config)
		}
	}
}

func TestWriteTestCertificate(t *testing.T) {
	certificate, key := writeTestCertificate(t, t.TempDir(), "127.0.0.1")
	for _, path := range []string{certificate, key} {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			t.Fatalf("certificate artifact %q invalid: %v", path, err)
		}
	}
}
