package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/svn"
	"github.com/oliver-tuschhoff/go-svn/svn/hashfile"
)

func TestDiskProviderRoundTrip(t *testing.T) {
	directory := t.TempDir()
	provider := NewDiskProvider(directory)
	ctx := context.Background()
	tests := []*Credentials{
		{Kind: Simple, Realm: "<https://example.com:443> repo", Username: "alice", Password: "secret"},
		{Kind: Username, Realm: "realm", Username: "bob"},
		{Kind: SSLServerTrust, Realm: "ssl", Certificate: "BASE64", Failures: 13},
	}
	for _, want := range tests {
		if err := provider.Save(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := provider.Get(ctx, want.Kind, want.Realm, want.Username)
		if err != nil {
			t.Fatal(err)
		}
		if got.Username != want.Username || got.Password != want.Password || got.Certificate != want.Certificate || got.Failures != want.Failures {
			t.Fatalf("credentials = %#v, want %#v", got, want)
		}
		info, err := os.Stat(filepath.Join(directory, string(want.Kind), CacheKey(want.Realm)))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("cache mode = %v, error %v", info.Mode().Perm(), err)
		}
	}
}

func TestDiskProviderReadsReferenceHashfile(t *testing.T) {
	directory := t.TempDir()
	realm := "reference realm"
	path := filepath.Join(directory, string(Simple), CacheKey(realm))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := hashfile.Write(&encoded, svn.Props{"svn:realmstring": []byte(realm), "username": []byte("user"), "password": []byte("pass"), "passtype": []byte("simple")}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewDiskProvider(directory).Get(context.Background(), Simple, realm, "user")
	if err != nil || got.Password != "pass" {
		t.Fatalf("credentials = %#v, error %v", got, err)
	}
}

func TestDiskProviderReadsSVN114KeychainFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/svn-1.14-keychain-simple")
	if err != nil {
		t.Fatal(err)
	}
	properties, err := hashfile.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	realm := string(properties["svn:realmstring"])
	directory := t.TempDir()
	path := filepath.Join(directory, string(Simple), CacheKey(realm))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = NewDiskProvider(directory).Get(context.Background(), Simple, realm, "alice")
	if !errors.Is(err, svn.ErrAuthnCredsUnavailable) {
		t.Fatalf("keychain cache error = %v", err)
	}
}

func TestSVNClientReadsDiskProviderCache(t *testing.T) {
	svnPath, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn executable is unavailable")
	}
	configDir := t.TempDir()
	realm := "<svn://example.invalid:3690> go-svn-fixture"
	provider := NewDiskProvider(filepath.Join(configDir, "auth"))
	if err := provider.Save(context.Background(), &Credentials{Kind: Simple, Realm: realm, Username: "alice", Password: "test-password"}); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(svnPath, "auth", "--show-passwords", "--config-dir", configDir).CombinedOutput()
	if err != nil {
		t.Fatalf("svn auth: %v\n%s", err, output)
	}
	text := string(output)
	for _, want := range []string{realm, "Username: alice", "Password: test-password"} {
		if !strings.Contains(text, want) {
			t.Fatalf("svn auth output missing %q:\n%s", want, text)
		}
	}
}

func TestDiskProviderRejectsNonSimplePassType(t *testing.T) {
	directory := t.TempDir()
	realm := "realm"
	path := filepath.Join(directory, string(Simple), CacheKey(realm))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := hashfile.Write(file, svn.Props{"svn:realmstring": []byte(realm), "username": []byte("user"), "passtype": []byte("keychain")}); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	_, err = NewDiskProvider(directory).Get(context.Background(), Simple, realm, "")
	if !errors.Is(err, svn.ErrAuthnCredsUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestBatonProviderPromptAndSavePolicy(t *testing.T) {
	provider := NewDiskProvider(t.TempDir())
	promptCalls := 0
	baton := &Baton{Providers: []Provider{provider}, StorePasswords: true, StorePlaintext: PlaintextAsk}
	baton.Prompt.Simple = func(_ context.Context, realm, username string, maySave bool) (*Credentials, error) {
		promptCalls++
		return &Credentials{Username: username, Password: "secret", MaySave: maySave}, nil
	}
	baton.Prompt.AllowPlaintext = func(context.Context, string) (bool, error) { return true, nil }
	got, err := baton.GetCredentials(context.Background(), Simple, "realm", "alice")
	if err != nil || got.Password != "secret" || promptCalls != 1 {
		t.Fatalf("credentials = %#v, calls %d, error %v", got, promptCalls, err)
	}
	got, err = baton.GetCredentials(context.Background(), Simple, "realm", "alice")
	if err != nil || got.Password != "secret" || promptCalls != 1 {
		t.Fatalf("cached credentials = %#v, calls %d, error %v", got, promptCalls, err)
	}
}

func TestBatonHonorsContextAndDeclinedPlaintext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	baton := &Baton{}
	if _, err := baton.GetCredentials(ctx, Simple, "realm", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	baton = &Baton{Providers: []Provider{NewDiskProvider(t.TempDir())}, StorePasswords: true, StorePlaintext: PlaintextNever}
	err := baton.SaveCredentials(context.Background(), &Credentials{Kind: Simple, Realm: "realm", Password: "secret"})
	if !errors.Is(err, svn.ErrAuthnCredsNotSaved) {
		t.Fatalf("error = %v", err)
	}
}

func TestDiskProviderRejectsNilCredentials(t *testing.T) {
	err := NewDiskProvider(t.TempDir()).Save(context.Background(), nil)
	if !errors.Is(err, svn.ErrAuthnCredsNotSaved) {
		t.Fatalf("error = %v", err)
	}
}
