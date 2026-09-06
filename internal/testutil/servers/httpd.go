package servers

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otuschhoff/go-svn/internal/testutil"
)

type HTTPAuth string

const (
	HTTPAuthBasic  HTTPAuth = "Basic"
	HTTPAuthDigest HTTPAuth = "Digest"
)

type BulkUpdates string

const (
	BulkUpdatesOn     BulkUpdates = "On"
	BulkUpdatesOff    BulkUpdates = "Off"
	BulkUpdatesPrefer BulkUpdates = "Prefer"
)

type HTTPDOptions struct {
	Path           string
	ModuleDir      string
	Host           string
	Username       string
	Password       string
	Realm          string
	Auth           HTTPAuth
	TLS            bool
	BulkUpdates    BulkUpdates
	StartupTimeout time.Duration
}

type HTTPD struct {
	URL             string
	Username        string
	Password        string
	Address         string
	CertificatePath string
	ConfigPath      string
	process         *process
}

func StartHTTPD(t testing.TB, repositoriesRoot string, options HTTPDOptions) *HTTPD {
	t.Helper()
	if options.Path == "" {
		options.Path = testutil.RequireTool(t, "httpd", "GOSVN_HTTPD")
	}
	options.applyDefaults()
	if err := options.validate(); err != nil {
		t.Fatal(err)
	}
	repositoriesRoot = absoluteDirectory(t, repositoriesRoot)

	modules, reason := discoverHTTPDModules(options.Path, options.ModuleDir, options.Auth, options.TLS)
	if reason != "" {
		t.Skip(reason)
	}

	address, _, err := reserveAddress(options.Host)
	if err != nil {
		t.Fatalf("reserve httpd address: %v", err)
	}
	serverRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(serverRoot, "logs"), 0o755); err != nil {
		t.Fatalf("create httpd log directory: %v", err)
	}
	passwordPath := filepath.Join(serverRoot, "passwd")
	writeHTTPPasswordFile(t, passwordPath, options)

	certificatePath := ""
	keyPath := ""
	if options.TLS {
		certificatePath, keyPath = writeTestCertificate(t, serverRoot, options.Host)
	}
	configPath := filepath.Join(serverRoot, "httpd.conf")
	config := renderHTTPDConfig(serverRoot, repositoriesRoot, address, passwordPath, certificatePath, keyPath, modules, options)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write httpd config: %v", err)
	}

	serverProcess := startProcess(t, options.Path, "-f", configPath, "-DFOREGROUND")
	if err := serverProcess.waitForTCP(address, options.StartupTimeout); err != nil {
		serverProcess.stop()
		t.Fatal(err)
	}

	scheme := "http"
	if options.TLS {
		scheme = "https"
	}
	endpoint := (&url.URL{Scheme: scheme, Host: address, Path: "/svn/"}).String()
	return &HTTPD{
		URL:             endpoint,
		Username:        options.Username,
		Password:        options.Password,
		Address:         address,
		CertificatePath: certificatePath,
		ConfigPath:      configPath,
		process:         serverProcess,
	}
}

func (s *HTTPD) Close() {
	if s != nil && s.process != nil {
		s.process.stop()
	}
}

func (s *HTTPD) Output() string {
	if s == nil || s.process == nil {
		return ""
	}
	return s.process.output.String()
}

func (options *HTTPDOptions) applyDefaults() {
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
	if options.Auth == "" {
		options.Auth = HTTPAuthBasic
	}
	if options.BulkUpdates == "" {
		options.BulkUpdates = BulkUpdatesPrefer
	}
}

func (options HTTPDOptions) validate() error {
	if strings.ContainsAny(options.Username+options.Password+options.Realm, "\r\n") {
		return fmt.Errorf("HTTP credentials and realm must not contain newlines")
	}
	if options.Auth != HTTPAuthBasic && options.Auth != HTTPAuthDigest {
		return fmt.Errorf("unsupported HTTP auth type %q", options.Auth)
	}
	switch options.BulkUpdates {
	case BulkUpdatesOn, BulkUpdatesOff, BulkUpdatesPrefer:
		return nil
	default:
		return fmt.Errorf("unsupported SVNAllowBulkUpdates value %q", options.BulkUpdates)
	}
}

type apacheModule struct {
	identifier string
	filenames  []string
}

func discoverHTTPDModules(httpdPath, overrideDir string, auth HTTPAuth, tlsEnabled bool) ([]string, string) {
	staticOutput, _ := inspectHTTPD(httpdPath, "-l")
	static := string(staticOutput)
	versionOutput, err := inspectHTTPD(httpdPath, "-V")
	if err != nil {
		return nil, fmt.Sprintf("inspect httpd: %v: %s", err, versionOutput)
	}
	directories := apacheModuleDirectories(httpdPath, overrideDir, string(versionOutput))

	required := []apacheModule{
		{identifier: "authn_core_module", filenames: []string{"mod_authn_core.so"}},
		{identifier: "authz_core_module", filenames: []string{"mod_authz_core.so"}},
		{identifier: "authn_file_module", filenames: []string{"mod_authn_file.so"}},
		{identifier: "authz_user_module", filenames: []string{"mod_authz_user.so"}},
		{identifier: "dav_module", filenames: []string{"mod_dav.so"}},
		{identifier: "dav_svn_module", filenames: []string{"mod_dav_svn.so"}},
	}
	if auth == HTTPAuthDigest {
		required = append(required, apacheModule{identifier: "auth_digest_module", filenames: []string{"mod_auth_digest.so"}})
	} else {
		required = append(required, apacheModule{identifier: "auth_basic_module", filenames: []string{"mod_auth_basic.so"}})
	}
	if tlsEnabled {
		required = append(required, apacheModule{identifier: "ssl_module", filenames: []string{"mod_ssl.so"}})
	}

	var loadDirectives []string
	if !strings.Contains(static, "mpm_") {
		mpm, found := findFirstModule(directories, []string{"mod_mpm_event.so", "mod_mpm_worker.so", "mod_mpm_prefork.so"})
		if !found {
			return nil, "httpd has no discoverable static or dynamic MPM module"
		}
		identifier := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(mpm), "mod_"), ".so") + "_module"
		loadDirectives = append(loadDirectives, fmt.Sprintf("LoadModule %s %s", identifier, directiveQuote(mpm)))
	}

	for _, module := range required {
		if containsStaticModule(static, strings.TrimSuffix(module.filenames[0], ".so")) {
			continue
		}
		path, found := findFirstModule(directories, module.filenames)
		if !found {
			return nil, fmt.Sprintf("httpd module %s was not found; set GOSVN_HTTPD_MODULE_DIR or use GOSVN_DOCKER=1", module.filenames[0])
		}
		loadDirectives = append(loadDirectives, fmt.Sprintf("LoadModule %s %s", module.identifier, directiveQuote(path)))
	}
	return loadDirectives, ""
}

func inspectHTTPD(httpdPath, argument string) ([]byte, error) {
	command := exec.Command(httpdPath, argument)
	command.Env = append(os.Environ(),
		"APACHE_RUN_DIR="+os.TempDir(),
		"APACHE_LOCK_DIR="+os.TempDir(),
		"APACHE_LOG_DIR="+os.TempDir(),
		"APACHE_PID_FILE="+filepath.Join(os.TempDir(), "go-svn-apache2.pid"),
		"APACHE_RUN_USER=www-data",
		"APACHE_RUN_GROUP=www-data",
	)
	return command.CombinedOutput()
}

func apacheModuleDirectories(httpdPath, overrideDir, versionOutput string) []string {
	var directories []string
	if overrideDir != "" {
		directories = append(directories, overrideDir)
	}
	if environment := os.Getenv("GOSVN_HTTPD_MODULE_DIR"); environment != "" {
		directories = append(directories, environment)
	}
	root := parseHTTPDDefine(versionOutput, "HTTPD_ROOT")
	if root != "" {
		directories = append(directories,
			filepath.Join(root, "modules"),
			filepath.Join(root, "libexec", "apache2"),
			filepath.Join(root, "lib", "apache2", "modules"),
		)
	}
	directories = append(directories,
		filepath.Clean(filepath.Join(filepath.Dir(httpdPath), "..", "lib", "httpd", "modules")),
		"/usr/lib/apache2/modules",
		"/usr/lib64/httpd/modules",
		"/usr/libexec/apache2",
		"/opt/homebrew/lib/httpd/modules",
	)
	return uniqueStrings(directories)
}

func parseHTTPDDefine(output, name string) string {
	prefix := "-D " + name + "=\""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, "\"") {
			return strings.TrimSuffix(strings.TrimPrefix(line, prefix), "\"")
		}
	}
	return ""
}

func containsStaticModule(output, name string) bool {
	wantSource := name + ".c"
	wantIdentifier := name + "_module"
	for _, line := range strings.Split(output, "\n") {
		module := strings.TrimSpace(line)
		if module == wantSource || module == wantIdentifier {
			return true
		}
	}
	return false
}

func findFirstModule(directories, filenames []string) (string, bool) {
	for _, directory := range directories {
		for _, filename := range filenames {
			path := filepath.Join(directory, filename)
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				return path, true
			}
		}
	}
	return "", false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func renderHTTPDConfig(serverRoot, repositoriesRoot, address, passwordPath, certificatePath, keyPath string, modules []string, options HTTPDOptions) string {
	var config strings.Builder
	fmt.Fprintf(&config, "ServerRoot %s\n", directiveQuote(serverRoot))
	fmt.Fprintf(&config, "ServerName %s\n", options.Host)
	fmt.Fprintf(&config, "Listen %s\n", address)
	fmt.Fprintf(&config, "PidFile %s\n", directiveQuote(filepath.Join(serverRoot, "httpd.pid")))
	fmt.Fprintf(&config, "ErrorLog %s\n", directiveQuote(filepath.Join(serverRoot, "error.log")))
	config.WriteString("LogLevel warn\n")
	for _, module := range modules {
		config.WriteString(module)
		config.WriteByte('\n')
	}
	if options.TLS {
		config.WriteString("SSLEngine On\nSSLSessionCache none\n")
		fmt.Fprintf(&config, "SSLCertificateFile %s\n", directiveQuote(certificatePath))
		fmt.Fprintf(&config, "SSLCertificateKeyFile %s\n", directiveQuote(keyPath))
	}
	config.WriteString("<Location /svn>\n")
	config.WriteString("  DAV svn\n")
	fmt.Fprintf(&config, "  SVNParentPath %s\n", directiveQuote(repositoriesRoot))
	config.WriteString("  SVNListParentPath On\n")
	fmt.Fprintf(&config, "  SVNAllowBulkUpdates %s\n", options.BulkUpdates)
	fmt.Fprintf(&config, "  AuthType %s\n", options.Auth)
	fmt.Fprintf(&config, "  AuthName %s\n", directiveQuote(options.Realm))
	fmt.Fprintf(&config, "  AuthUserFile %s\n", directiveQuote(passwordPath))
	config.WriteString("  Require valid-user\n")
	config.WriteString("</Location>\n")
	return config.String()
}

func writeHTTPPasswordFile(t testing.TB, path string, options HTTPDOptions) {
	t.Helper()
	var line string
	if options.Auth == HTTPAuthDigest {
		digest := md5.Sum([]byte(options.Username + ":" + options.Realm + ":" + options.Password))
		line = options.Username + ":" + options.Realm + ":" + hex.EncodeToString(digest[:]) + "\n"
	} else {
		digest := sha1.Sum([]byte(options.Password))
		line = options.Username + ":{SHA}" + base64.StdEncoding.EncodeToString(digest[:]) + "\n"
	}
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("write HTTP password file: %v", err)
	}
}

func writeTestCertificate(t testing.TB, directory, host string) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate TLS key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
	}
	if address := net.ParseIP(host); address != nil {
		template.IPAddresses = []net.IP{address}
	} else {
		template.DNSNames = append(template.DNSNames, host)
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create TLS certificate: %v", err)
	}
	certificatePath := filepath.Join(directory, "server-cert.pem")
	keyPath := filepath.Join(directory, "server-key.pem")
	writePEM(t, certificatePath, "CERTIFICATE", certificateDER, 0o644)
	writePEM(t, keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), 0o600)
	return certificatePath, keyPath
}

func writePEM(t testing.TB, path, blockType string, data []byte, mode os.FileMode) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := pem.Encode(file, &pem.Block{Type: blockType, Bytes: data}); err != nil {
		_ = file.Close()
		t.Fatalf("write %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func directiveQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", `\r`, "\n", `\n`).Replace(value) + `"`
}
