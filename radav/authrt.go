package radav

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/oliver-tuschhoff/go-svn/auth"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

const maxAuthAttempts = 8

type authChallenge struct {
	scheme     string
	parameters map[string]string
}

type authState struct {
	challenge   authChallenge
	credentials *auth.Credentials
	nonceCount  uint32
}

type authRoundTripper struct {
	transport   http.RoundTripper
	auth        *auth.Baton
	random      io.Reader
	mutex       sync.Mutex
	states      map[string]*authState
	originLocks map[string]*sync.Mutex
}

func newAuthRoundTripper(transport http.RoundTripper, baton *auth.Baton) http.RoundTripper {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &authRoundTripper{
		transport: transport, auth: baton, random: rand.Reader,
		states: make(map[string]*authState), originLocks: make(map[string]*sync.Mutex),
	}
}

func (roundTripper *authRoundTripper) RoundTrip(original *http.Request) (*http.Response, error) {
	originKey := authOriginKey(original.URL.Scheme, original.URL.Host)
	originLock := roundTripper.originLock(originKey)
	originLock.Lock()
	defer originLock.Unlock()
	if original.Body != nil {
		defer original.Body.Close()
	}
	request, err := cloneAuthRequest(original)
	if err != nil {
		return nil, err
	}
	proxyKey := "proxy|" + originKey
	originState := roundTripper.state(originKey)
	proxyState := roundTripper.state(proxyKey)
	if originState != nil {
		if err := roundTripper.authorize(request, originState, false); err != nil {
			return nil, err
		}
	}
	if proxyState != nil {
		if err := roundTripper.authorize(request, proxyState, true); err != nil {
			return nil, err
		}
	}
	rejected := map[bool][]*auth.Credentials{false: nil, true: nil}
	for attempt := 0; attempt < maxAuthAttempts; attempt++ {
		response, err := roundTripper.transport.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		proxy := response.StatusCode == http.StatusProxyAuthRequired
		if response.StatusCode != http.StatusUnauthorized && !proxy {
			if originState != nil {
				roundTripper.applyNextNonce(response, originState, false)
				roundTripper.remember(originKey, originState)
			}
			if proxyState != nil {
				roundTripper.applyNextNonce(response, proxyState, true)
				roundTripper.remember(proxyKey, proxyState)
			}
			return response, nil
		}
		header := "WWW-Authenticate"
		state := originState
		if proxy {
			header = "Proxy-Authenticate"
			state = proxyState
		}
		challenge, ok := selectAuthChallenge(parseAuthChallenges(response.Header.Values(header)))
		if !ok {
			return response, nil
		}
		stale := strings.EqualFold(challenge.parameters["stale"], "true")
		reuse := stale && state != nil && state.challenge.scheme == challenge.scheme && state.challenge.parameters["realm"] == challenge.parameters["realm"]
		if !reuse {
			if state != nil && state.credentials != nil {
				rejected[proxy] = append(rejected[proxy], state.credentials)
			}
			credentials, credentialErr := roundTripper.credentials(request.Context(), request, challenge, rejected[proxy])
			if credentialErr != nil {
				if errors.Is(credentialErr, svn.ErrAuthnProvidersExhausted) || errors.Is(credentialErr, svn.ErrAuthnNoProvider) || errors.Is(credentialErr, svn.ErrAuthnCredsUnavailable) {
					drainAndClose(response.Body)
					return nil, fmt.Errorf("%w: HTTP credentials exhausted for %s", svn.ErrRANotAuthorized, request.URL.Host)
				}
				drainAndClose(response.Body)
				return nil, credentialErr
			}
			state = &authState{challenge: challenge, credentials: credentials}
		} else {
			state.challenge = challenge
			state.nonceCount = 0
		}
		if proxy {
			proxyState = state
		} else {
			originState = state
		}
		drainAndClose(response.Body)
		request, err = cloneAuthRequest(original)
		if err != nil {
			return nil, err
		}
		if originState != nil {
			if err := roundTripper.authorize(request, originState, false); err != nil {
				return nil, err
			}
		}
		if proxyState != nil {
			if err := roundTripper.authorize(request, proxyState, true); err != nil {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("%w: too many HTTP authentication attempts", svn.ErrRANotAuthorized)
}

func (roundTripper *authRoundTripper) originLock(key string) *sync.Mutex {
	roundTripper.mutex.Lock()
	defer roundTripper.mutex.Unlock()
	lock := roundTripper.originLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		roundTripper.originLocks[key] = lock
	}
	return lock
}

func (roundTripper *authRoundTripper) credentials(ctx context.Context, request *http.Request, challenge authChallenge, rejected []*auth.Credentials) (*auth.Credentials, error) {
	if roundTripper.auth == nil {
		return nil, svn.ErrAuthnNoProvider
	}
	realm := "<" + request.URL.Scheme + "://" + request.URL.Host + "> " + challenge.parameters["realm"]
	return roundTripper.auth.GetCredentialsAfter(ctx, auth.Simple, realm, "", rejected...)
}

func (roundTripper *authRoundTripper) authorize(request *http.Request, state *authState, proxy bool) error {
	header := "Authorization"
	if proxy {
		header = "Proxy-Authorization"
	}
	switch state.challenge.scheme {
	case "basic":
		request.Header.Set(header, "Basic "+base64.StdEncoding.EncodeToString([]byte(state.credentials.Username+":"+state.credentials.Password)))
	case "digest":
		state.nonceCount++
		cnonceBytes := make([]byte, 16)
		if _, err := io.ReadFull(roundTripper.random, cnonceBytes); err != nil {
			return err
		}
		cnonce := base64.RawStdEncoding.EncodeToString(cnonceBytes)
		value, err := digestAuthorization(state.challenge, state.credentials, request.Method, request.URL.RequestURI(), cnonce, state.nonceCount)
		if err != nil {
			return err
		}
		request.Header.Set(header, value)
	default:
		return fmt.Errorf("%w: unsupported HTTP authentication scheme %q", svn.ErrRANotAuthorized, state.challenge.scheme)
	}
	return nil
}

func (roundTripper *authRoundTripper) state(key string) *authState {
	roundTripper.mutex.Lock()
	defer roundTripper.mutex.Unlock()
	stored := roundTripper.states[key]
	if stored == nil {
		return nil
	}
	copy := *stored
	copy.challenge.parameters = cloneAuthParameters(stored.challenge.parameters)
	return &copy
}

func (roundTripper *authRoundTripper) remember(key string, state *authState) {
	roundTripper.mutex.Lock()
	defer roundTripper.mutex.Unlock()
	copy := *state
	copy.challenge.parameters = cloneAuthParameters(state.challenge.parameters)
	roundTripper.states[key] = &copy
}

func cloneAuthParameters(parameters map[string]string) map[string]string {
	copy := make(map[string]string, len(parameters))
	for name, value := range parameters {
		copy[name] = value
	}
	return copy
}

func (roundTripper *authRoundTripper) applyNextNonce(response *http.Response, state *authState, proxy bool) {
	header := "Authentication-Info"
	if proxy {
		header = "Proxy-Authentication-Info"
	}
	parameters := parseAuthParameters(response.Header.Get(header))
	if nonce := parameters["nextnonce"]; nonce != "" {
		state.challenge.parameters["nonce"] = nonce
		state.nonceCount = 0
	}
}

func cloneAuthRequest(request *http.Request) (*http.Request, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	if request.Body != nil {
		if request.GetBody == nil {
			return nil, fmt.Errorf("%w: authenticated request body is not replayable", svn.ErrRADAVCreatingRequest)
		}
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		clone.Body = body
	}
	return clone, nil
}

func authOriginKey(scheme, host string) string {
	return strings.ToLower(scheme + "://" + host)
}

func parseAuthChallenges(values []string) []authChallenge {
	var challenges []authChallenge
	for _, value := range values {
		for len(strings.TrimSpace(value)) > 0 {
			value = strings.TrimSpace(value)
			space := strings.IndexAny(value, " \t")
			if space < 0 {
				challenges = append(challenges, authChallenge{scheme: strings.ToLower(value), parameters: map[string]string{}})
				break
			}
			scheme := strings.ToLower(value[:space])
			rest := strings.TrimSpace(value[space:])
			end := nextChallenge(rest)
			parameters := rest
			if end >= 0 {
				parameters, value = rest[:end], rest[end+1:]
			} else {
				value = ""
			}
			challenges = append(challenges, authChallenge{scheme: scheme, parameters: parseAuthParameters(parameters)})
		}
	}
	return challenges
}

func nextChallenge(value string) int {
	quoted, escaped := false, false
	for index, character := range value {
		switch {
		case escaped:
			escaped = false
		case character == '\\' && quoted:
			escaped = true
		case character == '"':
			quoted = !quoted
		case character == ',' && !quoted:
			remainder := strings.TrimSpace(value[index+1:])
			tokenEnd := strings.IndexAny(remainder, " \t=,")
			if tokenEnd > 0 && strings.TrimSpace(remainder[tokenEnd:]) != "" && !strings.HasPrefix(strings.TrimSpace(remainder[tokenEnd:]), "=") {
				return index
			}
		}
	}
	return -1
}

func parseAuthParameters(value string) map[string]string {
	parameters := make(map[string]string)
	for _, part := range splitAuthParameters(value) {
		name, raw, found := strings.Cut(part, "=")
		if !found {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		raw = strings.TrimSpace(raw)
		if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
			raw = strings.ReplaceAll(strings.ReplaceAll(raw[1:len(raw)-1], `\"`, `"`), `\\`, `\`)
		}
		parameters[name] = raw
	}
	return parameters
}

func splitAuthParameters(value string) []string {
	var result []string
	start := 0
	quoted, escaped := false, false
	for index, character := range value {
		switch {
		case escaped:
			escaped = false
		case character == '\\' && quoted:
			escaped = true
		case character == '"':
			quoted = !quoted
		case character == ',' && !quoted:
			result = append(result, strings.TrimSpace(value[start:index]))
			start = index + 1
		}
	}
	return append(result, strings.TrimSpace(value[start:]))
}

func selectAuthChallenge(challenges []authChallenge) (authChallenge, bool) {
	sort.SliceStable(challenges, func(first, second int) bool {
		return challengeRank(challenges[first]) > challengeRank(challenges[second])
	})
	for _, challenge := range challenges {
		if challenge.parameters["realm"] == "" {
			continue
		}
		if challenge.scheme == "basic" {
			return challenge, true
		}
		if challenge.scheme != "digest" || strings.EqualFold(challenge.parameters["userhash"], "true") {
			continue
		}
		algorithm := strings.ToUpper(challenge.parameters["algorithm"])
		if algorithm == "" {
			algorithm = "MD5"
		}
		if algorithm != "MD5" && algorithm != "MD5-SESS" && algorithm != "SHA-256" && algorithm != "SHA-256-SESS" {
			continue
		}
		qop := challenge.parameters["qop"]
		if qop != "" && !containsAuthToken(qop, "auth") {
			continue
		}
		return challenge, true
	}
	return authChallenge{}, false
}

func challengeRank(challenge authChallenge) int {
	if challenge.scheme == "digest" {
		if strings.HasPrefix(strings.ToUpper(challenge.parameters["algorithm"]), "SHA-256") {
			return 3
		}
		return 2
	}
	if challenge.scheme == "basic" {
		return 1
	}
	return 0
}

func containsAuthToken(value, want string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), want) {
			return true
		}
	}
	return false
}

func digestAuthorization(challenge authChallenge, credentials *auth.Credentials, method, uri, cnonce string, nonceCount uint32) (string, error) {
	algorithm := strings.ToUpper(challenge.parameters["algorithm"])
	if algorithm == "" {
		algorithm = "MD5"
	}
	hashName := strings.TrimSuffix(algorithm, "-SESS")
	hash := func(value string) string {
		switch hashName {
		case "SHA-256":
			digest := sha256.Sum256([]byte(value))
			return hex.EncodeToString(digest[:])
		default:
			digest := md5.Sum([]byte(value))
			return hex.EncodeToString(digest[:])
		}
	}
	realm, nonce := challenge.parameters["realm"], challenge.parameters["nonce"]
	if nonce == "" {
		return "", fmt.Errorf("%w: Digest challenge has no nonce", svn.ErrRANotAuthorized)
	}
	ha1 := hash(credentials.Username + ":" + realm + ":" + credentials.Password)
	if strings.HasSuffix(algorithm, "-SESS") {
		ha1 = hash(ha1 + ":" + nonce + ":" + cnonce)
	}
	ha2 := hash(method + ":" + uri)
	qop := ""
	if containsAuthToken(challenge.parameters["qop"], "auth") {
		qop = "auth"
	}
	nc := fmt.Sprintf("%08x", nonceCount)
	responseInput := ha1 + ":" + nonce + ":"
	if qop != "" {
		responseInput += nc + ":" + cnonce + ":" + qop + ":"
	}
	response := hash(responseInput + ha2)
	fields := []string{
		`username="` + quoteAuth(credentials.Username) + `"`,
		`realm="` + quoteAuth(realm) + `"`,
		`nonce="` + quoteAuth(nonce) + `"`,
		`uri="` + quoteAuth(uri) + `"`,
		`response="` + response + `"`,
		"algorithm=" + algorithm,
	}
	if opaque := challenge.parameters["opaque"]; opaque != "" {
		fields = append(fields, `opaque="`+quoteAuth(opaque)+`"`)
	}
	if qop != "" {
		fields = append(fields, "qop="+qop, "nc="+nc, `cnonce="`+quoteAuth(cnonce)+`"`)
	}
	return "Digest " + strings.Join(fields, ", "), nil
}

func quoteAuth(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func parseNonceCount(value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 16, 32)
	return uint32(parsed), err
}
