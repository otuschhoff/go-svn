package radav

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/auth"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestAuthRoundTripperBasicCachesByOrigin(t *testing.T) {
	var challenged atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Basic dXNlcjpwYXNz" {
			challenged.Add(1)
			writer.Header().Set("WWW-Authenticate", `Basic realm="repository"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var prompted atomic.Int32
	baton := &auth.Baton{Prompt: auth.Prompt{Simple: func(context.Context, string, string, bool) (*auth.Credentials, error) {
		prompted.Add(1)
		return &auth.Credentials{Username: "user", Password: "pass"}, nil
	}}}
	client := &http.Client{Transport: newAuthRoundTripper(http.DefaultTransport, baton)}
	for range 2 {
		request, err := http.NewRequest(http.MethodOptions, server.URL, strings.NewReader("body"))
		if err != nil {
			t.Fatal(err)
		}
		request.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("body")), nil }
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		drainAndClose(response.Body)
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("status=%d", response.StatusCode)
		}
	}
	if challenged.Load() != 1 || prompted.Load() != 1 {
		t.Fatalf("challenges=%d prompts=%d", challenged.Load(), prompted.Load())
	}
}

func TestDigestAuthorizationRFC7616MD5(t *testing.T) {
	challenge := authChallenge{scheme: "digest", parameters: map[string]string{
		"realm": "http-auth@example.org", "qop": "auth", "algorithm": "MD5",
		"nonce": "7ypf/xlj9XXwfDPEoM4URrv/xHm22rYvcr1A1S61UPQ=", "opaque": "F2jE1bS6D+UGGw4Gwz2g2T3A6Qq6Sg4e",
	}}
	credentials := &auth.Credentials{Username: "Mufasa", Password: "Circle of Life"}
	header, err := digestAuthorization(challenge, credentials, http.MethodGet, "/dir/index.html", "f2/wE4q74U7qMyq3m1lniA==", 1)
	if err != nil {
		t.Fatal(err)
	}
	wantResponse := md5Hex(md5Hex("Mufasa:http-auth@example.org:Circle of Life"), ":", challenge.parameters["nonce"], ":00000001:f2/wE4q74U7qMyq3m1lniA==:auth:", md5Hex("GET:/dir/index.html"))
	if !strings.Contains(header, `response="`+wantResponse+`"`) || !strings.Contains(header, "nc=00000001") {
		t.Fatalf("header=%s", header)
	}
}

func TestDigestAuthorizationAlgorithms(t *testing.T) {
	for _, algorithm := range []string{"MD5", "MD5-sess", "SHA-256", "SHA-256-sess"} {
		t.Run(algorithm, func(t *testing.T) {
			challenge := authChallenge{scheme: "digest", parameters: map[string]string{
				"realm": "repository", "nonce": "nonce", "algorithm": algorithm, "qop": "auth",
			}}
			credentials := &auth.Credentials{Username: "user", Password: "pass"}
			header, err := digestAuthorization(challenge, credentials, http.MethodPut, "/repo/file", "cnonce", 7)
			if err != nil {
				t.Fatal(err)
			}
			hash := digestHash(algorithm)
			ha1 := hash("user:repository:pass")
			if strings.HasSuffix(strings.ToUpper(algorithm), "-SESS") {
				ha1 = hash(ha1 + ":nonce:cnonce")
			}
			want := hash(ha1 + ":nonce:00000007:cnonce:auth:" + hash("PUT:/repo/file"))
			if !strings.Contains(header, `response="`+want+`"`) {
				t.Fatalf("header=%s", header)
			}
		})
	}
}

func TestAuthRoundTripperDoesNotLeakAcrossRedirect(t *testing.T) {
	var leaked string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		leaked = request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "" {
			writer.Header().Set("WWW-Authenticate", `Basic realm="repository"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	baton := &auth.Baton{Prompt: auth.Prompt{Simple: func(context.Context, string, string, bool) (*auth.Credentials, error) {
		return &auth.Credentials{Username: "user", Password: "pass"}, nil
	}}}
	client := &http.Client{Transport: newAuthRoundTripper(http.DefaultTransport, baton)}
	response, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	drainAndClose(response.Body)
	if leaked != "" {
		t.Fatalf("redirect leaked Authorization=%q", leaked)
	}
}

func TestAuthRoundTripperRejectsCredentialAndPromptsNext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Basic Z29vZDpwYXNz" {
			writer.Header().Set("WWW-Authenticate", `Basic realm="repository"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var prompts atomic.Int32
	baton := &auth.Baton{
		Providers: []auth.Provider{fixedAuthProvider{credentials: &auth.Credentials{Kind: auth.Simple, Username: "bad", Password: "pass"}}},
		Prompt: auth.Prompt{Simple: func(context.Context, string, string, bool) (*auth.Credentials, error) {
			prompts.Add(1)
			return &auth.Credentials{Username: "good", Password: "pass"}, nil
		}},
	}
	client := &http.Client{Transport: newAuthRoundTripper(http.DefaultTransport, baton)}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	drainAndClose(response.Body)
	if response.StatusCode != http.StatusNoContent || prompts.Load() != 1 {
		t.Fatalf("status=%d prompts=%d", response.StatusCode, prompts.Load())
	}
}

func TestAuthRoundTripperDigestStaleAndSHA256Preference(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempt := attempts.Add(1)
		authorization := request.Header.Get("Authorization")
		switch attempt {
		case 1:
			writer.Header().Add("WWW-Authenticate", `Basic realm="repository"`)
			writer.Header().Add("WWW-Authenticate", `Digest realm="repository", nonce="old", algorithm=SHA-256, qop="auth"`)
			writer.WriteHeader(http.StatusUnauthorized)
		case 2:
			if !strings.HasPrefix(authorization, "Digest ") || !strings.Contains(authorization, "algorithm=SHA-256") || !strings.Contains(authorization, `nonce="old"`) {
				t.Errorf("authorization=%s", authorization)
			}
			writer.Header().Set("WWW-Authenticate", `Digest realm="repository", nonce="new", algorithm=SHA-256, qop="auth", stale=true`)
			writer.WriteHeader(http.StatusUnauthorized)
		default:
			if !strings.Contains(authorization, `nonce="new"`) || !strings.Contains(authorization, "nc=00000001") {
				t.Errorf("stale authorization=%s", authorization)
			}
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	var prompts atomic.Int32
	baton := &auth.Baton{Prompt: auth.Prompt{Simple: func(context.Context, string, string, bool) (*auth.Credentials, error) {
		prompts.Add(1)
		return &auth.Credentials{Username: "user", Password: "pass"}, nil
	}}}
	client := &http.Client{Transport: newAuthRoundTripper(http.DefaultTransport, baton)}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	drainAndClose(response.Body)
	if response.StatusCode != http.StatusNoContent || prompts.Load() != 1 {
		t.Fatalf("status=%d prompts=%d", response.StatusCode, prompts.Load())
	}
}

func TestAuthRoundTripperCachesProxyCredentials(t *testing.T) {
	var challenges atomic.Int32
	next := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusNoContent
		header := make(http.Header)
		if request.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			challenges.Add(1)
			status = http.StatusProxyAuthRequired
			header.Set("Proxy-Authenticate", `Basic realm="proxy"`)
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: header, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	baton := &auth.Baton{Prompt: auth.Prompt{Simple: func(context.Context, string, string, bool) (*auth.Credentials, error) {
		return &auth.Credentials{Username: "user", Password: "pass"}, nil
	}}}
	client := &http.Client{Transport: newAuthRoundTripper(next, baton)}
	for range 2 {
		response, err := client.Get("http://repository.example/repo")
		if err != nil {
			t.Fatal(err)
		}
		drainAndClose(response.Body)
	}
	if challenges.Load() != 1 {
		t.Fatalf("proxy challenges=%d", challenges.Load())
	}
}

func TestAuthRoundTripperAppliesNextNonce(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempt := requests.Add(1)
		authorization := request.Header.Get("Authorization")
		if authorization == "" {
			writer.Header().Set("WWW-Authenticate", `Digest realm="repository", nonce="first", algorithm=MD5, qop="auth"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if attempt == 2 {
			if !strings.Contains(authorization, `nonce="first"`) {
				t.Errorf("first authorization=%s", authorization)
			}
			writer.Header().Set("Authentication-Info", `nextnonce="second"`)
		} else if !strings.Contains(authorization, `nonce="second"`) || !strings.Contains(authorization, "nc=00000001") {
			t.Errorf("next authorization=%s", authorization)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	baton := &auth.Baton{Prompt: auth.Prompt{Simple: func(context.Context, string, string, bool) (*auth.Credentials, error) {
		return &auth.Credentials{Username: "user", Password: "pass"}, nil
	}}}
	client := &http.Client{Transport: newAuthRoundTripper(http.DefaultTransport, baton)}
	for range 2 {
		response, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		drainAndClose(response.Body)
	}
}

func TestAuthRoundTripperReportsCredentialExhaustion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("WWW-Authenticate", `Basic realm="repository"`)
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client := &http.Client{Transport: newAuthRoundTripper(http.DefaultTransport, &auth.Baton{})}
	_, err := client.Get(server.URL)
	if !errors.Is(err, svn.ErrRANotAuthorized) {
		t.Fatalf("error=%v", err)
	}
}

type fixedAuthProvider struct{ credentials *auth.Credentials }

func (provider fixedAuthProvider) Get(context.Context, auth.Kind, string, string) (*auth.Credentials, error) {
	copy := *provider.credentials
	return &copy, nil
}

func (fixedAuthProvider) Save(context.Context, *auth.Credentials) error {
	return errors.New("not saved")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func md5Hex(parts ...string) string {
	digest := md5.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(digest[:])
}

func digestHash(algorithm string) func(string) string {
	if strings.HasPrefix(strings.ToUpper(algorithm), "SHA-256") {
		return func(value string) string {
			digest := sha256.Sum256([]byte(value))
			return hex.EncodeToString(digest[:])
		}
	}
	return func(value string) string {
		digest := md5.Sum([]byte(value))
		return hex.EncodeToString(digest[:])
	}
}
