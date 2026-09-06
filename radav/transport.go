package radav

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oliver-tuschhoff/go-svn/auth"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type transport struct {
	client *http.Client
}

func newTransport(callbacks *ra.Callbacks, host string) (*transport, error) {
	if callbacks != nil && callbacks.HTTP != nil {
		copy := *callbacks.HTTP
		installRedirectPolicy(&copy)
		if hasHTTPAuthProvider(callbacks) {
			copy.Transport = newAuthRoundTripper(copy.Transport, &callbacks.Auth)
		}
		return &transport{client: &copy}, nil
	}
	httpTransport := http.DefaultTransport.(*http.Transport).Clone()
	client := &http.Client{Transport: httpTransport}
	installRedirectPolicy(client)
	if callbacks == nil || callbacks.Config == nil {
		if hasHTTPAuthProvider(callbacks) {
			client.Transport = newAuthRoundTripper(client.Transport, &callbacks.Auth)
		}
		return &transport{client: client}, nil
	}
	servers := callbacks.Config
	proxyHost := servers.ServerOption(host, "http-proxy-host", "")
	if proxyHost == "" {
		proxyHost = servers.ServerOption(host, "proxy-host", "")
	}
	if proxyHost != "" {
		proxyPort := servers.ServerOption(host, "http-proxy-port", servers.ServerOption(host, "proxy-port", ""))
		proxyURL := &url.URL{Scheme: "http", Host: proxyHost}
		if proxyPort != "" {
			proxyURL.Host = proxyHost + ":" + proxyPort
		}
		username := servers.ServerOption(host, "http-proxy-username", servers.ServerOption(host, "proxy-username", ""))
		password := servers.ServerOption(host, "http-proxy-password", servers.ServerOption(host, "proxy-password", ""))
		if username != "" {
			proxyURL.User = url.UserPassword(username, password)
		}
		if !proxyExcluded(host, servers.ServerOption(host, "http-proxy-exceptions", "")) {
			httpTransport.Proxy = http.ProxyURL(proxyURL)
		}
	}
	if raw := servers.ServerOption(host, "http-timeout", ""); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 {
			return nil, fmt.Errorf("%w: invalid http-timeout %q", svn.ErrRADAVInvalidConfigValue, raw)
		}
		client.Timeout = time.Duration(seconds) * time.Second
	}
	if raw := servers.ServerOption(host, "http-max-connections", ""); raw != "" {
		count, err := strconv.Atoi(raw)
		if err != nil || count < 1 {
			return nil, fmt.Errorf("%w: invalid http-max-connections %q", svn.ErrRADAVInvalidConfigValue, raw)
		}
		httpTransport.MaxConnsPerHost = count
		httpTransport.MaxIdleConnsPerHost = count
	}
	trustDefault, err := parseConfigBool(servers.ServerOption(host, "ssl-trust-default-ca", "yes"))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrRADAVInvalidConfigValue, err)
	}
	var roots *x509.CertPool
	if trustDefault {
		roots, _ = x509.SystemCertPool()
	}
	if roots == nil {
		roots = x509.NewCertPool()
	}
	for _, filename := range splitAuthorityFiles(servers.ServerOption(host, "ssl-authority-files", "")) {
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("%w: no certificates in %s", svn.ErrRADAVInvalidConfigValue, filename)
		}
	}
	httpTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if hasSSLTrustProvider(callbacks) {
		verifier := &certificateVerifier{roots: roots, auth: &callbacks.Auth, accepted: make(map[string]bool)}
		httpTransport.TLSClientConfig.InsecureSkipVerify = true
		httpTransport.TLSClientConfig.VerifyConnection = verifier.verifyConnection
		client.Transport = &trustRoundTripper{transport: httpTransport, verifier: verifier}
	}
	if hasHTTPAuthProvider(callbacks) {
		client.Transport = newAuthRoundTripper(client.Transport, &callbacks.Auth)
	}
	return &transport{client: client}, nil
}

func hasHTTPAuthProvider(callbacks *ra.Callbacks) bool {
	return callbacks != nil && (len(callbacks.Auth.Providers) > 0 || callbacks.Auth.Prompt.Simple != nil)
}

const (
	sslNotYetValid = 1 << iota
	sslExpired
	sslCNMismatch
	sslUnknownCA
	sslOther
)

type certificateTrustError struct {
	cause       error
	certificate *x509.Certificate
	failures    uint32
	host        string
}

func (err *certificateTrustError) Error() string { return err.cause.Error() }
func (err *certificateTrustError) Unwrap() error { return err.cause }

type certificateVerifier struct {
	roots    *x509.CertPool
	auth     *auth.Baton
	mutex    sync.RWMutex
	accepted map[string]bool
}

type trustRoundTripper struct {
	transport *http.Transport
	verifier  *certificateVerifier
}

func hasSSLTrustProvider(callbacks *ra.Callbacks) bool {
	return callbacks != nil && (len(callbacks.Auth.Providers) > 0 || callbacks.Auth.Prompt.SSLServerTrust != nil)
}

func (verifier *certificateVerifier) verifyConnection(state tls.ConnectionState) error {
	if len(state.PeerCertificates) == 0 {
		return &certificateTrustError{cause: errors.New("TLS peer sent no certificate"), failures: sslOther, host: state.ServerName}
	}
	leaf := state.PeerCertificates[0]
	key := certificateKey(state.ServerName, leaf)
	verifier.mutex.RLock()
	accepted := verifier.accepted[key]
	verifier.mutex.RUnlock()
	if accepted {
		return nil
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range state.PeerCertificates[1:] {
		intermediates.AddCert(certificate)
	}
	_, err := leaf.Verify(x509.VerifyOptions{DNSName: state.ServerName, Roots: verifier.roots, Intermediates: intermediates})
	if err == nil {
		return nil
	}
	return &certificateTrustError{cause: err, certificate: leaf, failures: certificateFailures(err), host: state.ServerName}
}

func (roundTripper *trustRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := roundTripper.transport.RoundTrip(request)
	var trustErr *certificateTrustError
	if err == nil || !errors.As(err, &trustErr) || trustErr.certificate == nil {
		return response, err
	}
	certificate := base64.StdEncoding.EncodeToString(trustErr.certificate.Raw)
	realm := request.URL.Scheme + "://" + request.URL.Host
	credentials, credentialErr := roundTripper.credentials(request.Context(), realm, certificate, trustErr.failures)
	if credentialErr != nil || credentials == nil || credentials.Failures&trustErr.failures != trustErr.failures {
		return nil, fmt.Errorf("%w: %v", svn.ErrRASerfSSLCertUntrusted, err)
	}
	key := certificateKey(trustErr.host, trustErr.certificate)
	roundTripper.verifier.mutex.Lock()
	roundTripper.verifier.accepted[key] = true
	roundTripper.verifier.mutex.Unlock()
	if credentials.MaySave {
		credentials.Kind = auth.SSLServerTrust
		credentials.Realm = realm
		credentials.Certificate = certificate
		_ = roundTripper.verifier.auth.SaveCredentials(request.Context(), credentials)
	}
	retry := request.Clone(request.Context())
	if request.GetBody != nil {
		retry.Body, err = request.GetBody()
		if err != nil {
			return nil, err
		}
	}
	return roundTripper.transport.RoundTrip(retry)
}

func (roundTripper *trustRoundTripper) credentials(ctx context.Context, realm, certificate string, failures uint32) (*auth.Credentials, error) {
	for _, provider := range roundTripper.verifier.auth.Providers {
		credentials, err := provider.Get(ctx, auth.SSLServerTrust, realm, "")
		if err == nil && credentials != nil && credentials.Certificate == certificate {
			return credentials, nil
		}
		if err != nil && !errors.Is(err, svn.ErrAuthnCredsUnavailable) {
			return nil, err
		}
	}
	if prompt := roundTripper.verifier.auth.Prompt.SSLServerTrust; prompt != nil {
		return prompt(ctx, realm, failures, certificate, roundTripper.verifier.auth.StorePasswords)
	}
	return nil, svn.ErrAuthnCredsUnavailable
}

func certificateKey(host string, certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.Raw)
	return strings.ToLower(host) + ":" + base64.RawStdEncoding.EncodeToString(digest[:])
}

func certificateFailures(err error) uint32 {
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return sslCNMismatch
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return sslUnknownCA
	}
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) {
		switch invalid.Reason {
		case x509.Expired:
			now := time.Now()
			if now.Before(invalid.Cert.NotBefore) {
				return sslNotYetValid
			}
			return sslExpired
		}
	}
	return sslOther
}

func parseConfigBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yes", "true", "on", "1":
		return true, nil
	case "no", "false", "off", "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", value)
	}
}

func proxyExcluded(host, patterns string) bool {
	for _, pattern := range strings.FieldsFunc(patterns, func(value rune) bool { return value == ',' || value == ' ' || value == '\t' }) {
		matched, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(host))
		if err == nil && matched {
			return true
		}
	}
	return false
}

func installRedirectPolicy(client *http.Client) {
	policy := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		previous := via[len(via)-1]
		request.Method = previous.Method
		request.ContentLength = previous.ContentLength
		if previous.GetBody != nil {
			body, err := previous.GetBody()
			if err != nil {
				return err
			}
			request.Body = body
			request.GetBody = previous.GetBody
		}
		if policy != nil {
			return policy(request, via)
		}
		return nil
	}
}

func splitAuthorityFiles(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ':' || r == ';' })
}

func (transport *transport) request(ctx context.Context, method, rawURL string, body []byte, headers http.Header) (*http.Response, error) {
	getBody := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return transport.requestBody(ctx, method, rawURL, getBody, int64(len(body)), headers)
}

func (transport *transport) requestFile(ctx context.Context, method, rawURL, filename string, headers http.Header) (*http.Response, error) {
	info, err := os.Stat(filename)
	if err != nil {
		return nil, err
	}
	getBody := func() (io.ReadCloser, error) { return os.Open(filename) }
	return transport.requestBody(ctx, method, rawURL, getBody, info.Size(), headers)
}

func (transport *transport) requestBody(ctx context.Context, method, rawURL string, getBody func() (io.ReadCloser, error), length int64, headers http.Header) (*http.Response, error) {
	body, err := getBody()
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		body.Close()
		return nil, fmt.Errorf("%w: %v", svn.ErrRADAVCreatingRequest, err)
	}
	request.ContentLength = length
	request.GetBody = getBody
	request.Header.Set("User-Agent", userAgent)
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := transport.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", svn.ErrRADAVRequestFailed, err)
	}
	return response, nil
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}
