package auth

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/oliver-tuschhoff/go-svn/svn"
	"github.com/oliver-tuschhoff/go-svn/svn/hashfile"
)

type Kind string

const (
	Simple                Kind = "svn.simple"
	Username              Kind = "svn.username"
	SSLServerTrust        Kind = "svn.ssl.server"
	SSLClientCert         Kind = "svn.ssl.client-cert"
	SSLClientCertPassword Kind = "svn.ssl.client-passphrase"
)

type Credentials struct {
	Kind        Kind
	Realm       string
	Username    string
	Password    string
	Certificate string
	Failures    uint32
	MaySave     bool
}

type Provider interface {
	Get(context.Context, Kind, string, string) (*Credentials, error)
	Save(context.Context, *Credentials) error
}

type Prompt struct {
	Simple                func(context.Context, string, string, bool) (*Credentials, error)
	Username              func(context.Context, string, string, bool) (*Credentials, error)
	SSLServerTrust        func(context.Context, string, uint32, string, bool) (*Credentials, error)
	SSLClientCert         func(context.Context, string, bool) (*Credentials, error)
	SSLClientCertPassword func(context.Context, string, bool) (*Credentials, error)
	AllowPlaintext        func(context.Context, string) (bool, error)
}

type PlaintextPolicy uint8

const (
	PlaintextNever PlaintextPolicy = iota
	PlaintextAsk
	PlaintextAlways
)

type Baton struct {
	Providers      []Provider
	Prompt         Prompt
	StorePasswords bool
	StorePlaintext PlaintextPolicy
}

func (baton *Baton) GetCredentials(ctx context.Context, kind Kind, realm, username string) (*Credentials, error) {
	return baton.GetCredentialsAfter(ctx, kind, realm, username)
}

// GetCredentialsAfter returns the next credential after rejected, skipping
// providers that return the same username and secret.
func (baton *Baton) GetCredentialsAfter(ctx context.Context, kind Kind, realm, username string, rejected ...*Credentials) (*Credentials, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, provider := range baton.Providers {
		credentials, err := provider.Get(ctx, kind, realm, username)
		if err == nil && credentials != nil && !containsCredentials(rejected, credentials) {
			return credentials, nil
		}
		if err != nil && !errors.Is(err, svn.ErrAuthnCredsUnavailable) {
			return nil, err
		}
	}
	credentials, err := baton.prompt(ctx, kind, realm, username)
	if err != nil {
		return nil, err
	}
	if credentials == nil {
		return nil, fmt.Errorf("%w: %s for %q", svn.ErrAuthnProvidersExhausted, kind, realm)
	}
	credentials.Kind, credentials.Realm = kind, realm
	if containsCredentials(rejected, credentials) {
		return nil, fmt.Errorf("%w: %s for %q", svn.ErrAuthnProvidersExhausted, kind, realm)
	}
	if credentials.MaySave && baton.StorePasswords {
		if saveErr := baton.save(ctx, credentials); saveErr != nil && !errors.Is(saveErr, svn.ErrAuthnCredsNotSaved) {
			return nil, saveErr
		}
	}
	return credentials, nil
}

func sameCredentials(first, second *Credentials) bool {
	return first != nil && second != nil && first.Kind == second.Kind && first.Username == second.Username && first.Password == second.Password && first.Certificate == second.Certificate
}

func containsCredentials(credentials []*Credentials, candidate *Credentials) bool {
	for _, item := range credentials {
		if sameCredentials(item, candidate) {
			return true
		}
	}
	return false
}

func (baton *Baton) SaveCredentials(ctx context.Context, credentials *Credentials) error {
	if credentials == nil {
		return fmt.Errorf("%w: nil credentials", svn.ErrAuthnCredsNotSaved)
	}
	return baton.save(ctx, credentials)
}

func (baton *Baton) save(ctx context.Context, credentials *Credentials) error {
	if credentials.Kind == Simple {
		switch baton.StorePlaintext {
		case PlaintextNever:
			return fmt.Errorf("%w: plaintext passwords disabled", svn.ErrAuthnCredsNotSaved)
		case PlaintextAsk:
			if baton.Prompt.AllowPlaintext == nil {
				return fmt.Errorf("%w: plaintext approval unavailable", svn.ErrAuthnCredsNotSaved)
			}
			allowed, err := baton.Prompt.AllowPlaintext(ctx, credentials.Realm)
			if err != nil {
				return err
			}
			if !allowed {
				return fmt.Errorf("%w: plaintext password declined", svn.ErrAuthnCredsNotSaved)
			}
		}
	}
	var saved bool
	for _, provider := range baton.Providers {
		err := provider.Save(ctx, credentials)
		if err == nil {
			saved = true
			continue
		}
		if !errors.Is(err, svn.ErrAuthnCredsNotSaved) && !errors.Is(err, svn.ErrAuthnCredsUnavailable) {
			return err
		}
	}
	if !saved {
		return fmt.Errorf("%w: no provider accepted %s", svn.ErrAuthnCredsNotSaved, credentials.Kind)
	}
	return nil
}

func (baton *Baton) prompt(ctx context.Context, kind Kind, realm, username string) (*Credentials, error) {
	maySave := baton.StorePasswords
	switch kind {
	case Simple:
		if baton.Prompt.Simple != nil {
			return baton.Prompt.Simple(ctx, realm, username, maySave)
		}
	case Username:
		if baton.Prompt.Username != nil {
			return baton.Prompt.Username(ctx, realm, username, maySave)
		}
	case SSLServerTrust:
		if baton.Prompt.SSLServerTrust != nil {
			return baton.Prompt.SSLServerTrust(ctx, realm, 0, "", maySave)
		}
	case SSLClientCert:
		if baton.Prompt.SSLClientCert != nil {
			return baton.Prompt.SSLClientCert(ctx, realm, maySave)
		}
	case SSLClientCertPassword:
		if baton.Prompt.SSLClientCertPassword != nil {
			return baton.Prompt.SSLClientCertPassword(ctx, realm, maySave)
		}
	}
	return nil, fmt.Errorf("%w: %s", svn.ErrAuthnNoProvider, kind)
}

type DiskProvider struct{ Directory string }

func NewDiskProvider(directory string) *DiskProvider { return &DiskProvider{Directory: directory} }

func (provider *DiskProvider) Get(ctx context.Context, kind Kind, realm, preferredUsername string) (*Credentials, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := provider.cachePath(kind, realm)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, unavailable(kind, realm)
	}
	if err != nil {
		return nil, err
	}
	properties, readErr := hashfile.Read(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if string(properties["svn:realmstring"]) != realm {
		return nil, unavailable(kind, realm)
	}
	credentials := &Credentials{Kind: kind, Realm: realm, Username: string(properties["username"]), MaySave: true}
	switch kind {
	case Simple:
		if string(properties["passtype"]) != "simple" {
			return nil, unavailable(kind, realm)
		}
		credentials.Password = string(properties["password"])
		if preferredUsername != "" && credentials.Username != preferredUsername {
			return nil, unavailable(kind, realm)
		}
	case Username:
		if preferredUsername != "" && credentials.Username != preferredUsername {
			return nil, unavailable(kind, realm)
		}
	case SSLServerTrust:
		credentials.Certificate = string(properties["ascii_cert"])
		failures, parseErr := strconv.ParseUint(string(properties["failures"]), 10, 32)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid SSL failure mask: %w", parseErr)
		}
		credentials.Failures = uint32(failures)
	case SSLClientCert:
		credentials.Certificate = string(properties["cert"])
	case SSLClientCertPassword:
		if string(properties["passtype"]) != "simple" {
			return nil, unavailable(kind, realm)
		}
		credentials.Password = string(properties["password"])
	default:
		return nil, unavailable(kind, realm)
	}
	return credentials, nil
}

func (provider *DiskProvider) Save(ctx context.Context, credentials *Credentials) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if credentials == nil {
		return fmt.Errorf("%w: nil credentials", svn.ErrAuthnCredsNotSaved)
	}
	path, err := provider.cachePath(credentials.Kind, credentials.Realm)
	if err != nil {
		return err
	}
	properties := svn.Props{"svn:realmstring": []byte(credentials.Realm)}
	switch credentials.Kind {
	case Simple:
		properties["username"] = []byte(credentials.Username)
		properties["password"] = []byte(credentials.Password)
		properties["passtype"] = []byte("simple")
	case Username:
		properties["username"] = []byte(credentials.Username)
	case SSLServerTrust:
		properties["ascii_cert"] = []byte(credentials.Certificate)
		properties["failures"] = []byte(strconv.FormatUint(uint64(credentials.Failures), 10))
	case SSLClientCert:
		properties["cert"] = []byte(credentials.Certificate)
	case SSLClientCertPassword:
		properties["password"] = []byte(credentials.Password)
		properties["passtype"] = []byte("simple")
	default:
		return unavailable(credentials.Kind, credentials.Realm)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".auth-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if err := hashfile.Write(temporary, properties); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func (provider *DiskProvider) Clear(kind Kind, realm string) error {
	path, err := provider.cachePath(kind, realm)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func CacheKey(realm string) string { sum := md5.Sum([]byte(realm)); return hex.EncodeToString(sum[:]) }

func (provider *DiskProvider) cachePath(kind Kind, realm string) (string, error) {
	switch kind {
	case Simple, Username, SSLServerTrust, SSLClientCert, SSLClientCertPassword:
		return filepath.Join(provider.Directory, string(kind), CacheKey(realm)), nil
	default:
		return "", unavailable(kind, realm)
	}
}

func unavailable(kind Kind, realm string) error {
	return fmt.Errorf("%w: %s for %q", svn.ErrAuthnCredsUnavailable, kind, realm)
}
