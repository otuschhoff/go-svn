package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/otuschhoff/go-svn/auth"
	"github.com/otuschhoff/go-svn/config"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

const (
	sslNotYetValid = 1 << iota
	sslExpired
	sslCNMismatch
	sslUnknownCA
	sslOther
)

type globalOptions struct {
	configDir      string
	configOptions  []string
	username       string
	password       string
	nonInteractive bool
	noAuthCache    bool
	trustFailures  string
}

func parseGlobalOptions(args []string) (globalOptions, []string, error) {
	var options globalOptions
	remaining := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		name, inlineValue, hasInline := strings.Cut(argument, "=")
		requireValue := func() (string, error) {
			if hasInline {
				return inlineValue, nil
			}
			index++
			if index >= len(args) {
				return "", fmt.Errorf("%s requires a value", name)
			}
			return args[index], nil
		}
		switch name {
		case "--config-dir":
			value, err := requireValue()
			if err != nil {
				return options, nil, err
			}
			options.configDir = value
		case "--config-option":
			value, err := requireValue()
			if err != nil {
				return options, nil, err
			}
			options.configOptions = append(options.configOptions, value)
		case "--username":
			value, err := requireValue()
			if err != nil {
				return options, nil, err
			}
			options.username = value
		case "--password":
			value, err := requireValue()
			if err != nil {
				return options, nil, err
			}
			options.password = value
		case "--non-interactive":
			options.nonInteractive = true
		case "--no-auth-cache":
			options.noAuthCache = true
		case "--trust-server-cert-failures":
			value, err := requireValue()
			if err != nil {
				return options, nil, err
			}
			options.trustFailures = value
		default:
			remaining = append(remaining, argument)
		}
	}
	return options, remaining, nil
}

func makeCallbacks(options globalOptions, stdin io.Reader, stdout, stderr io.Writer) (*ra.Callbacks, error) {
	configuration, err := config.Load(options.configDir)
	if err != nil {
		return nil, err
	}
	for _, override := range options.configOptions {
		if err := configuration.ApplyOverride(override); err != nil {
			return nil, err
		}
	}
	baton := auth.Baton{StorePasswords: !options.noAuthCache, StorePlaintext: auth.PlaintextAsk}
	if options.username != "" || options.password != "" {
		baton.Providers = append(baton.Providers, staticProvider{username: options.username, password: options.password})
	}
	if !options.noAuthCache {
		configDir := options.configDir
		if configDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			configDir = filepath.Join(home, ".subversion")
		}
		baton.Providers = append(baton.Providers, auth.NewDiskProvider(filepath.Join(configDir, "auth")))
	}
	if !options.nonInteractive {
		baton.Prompt.Simple = simplePrompt(stdin, stderr)
		baton.Prompt.Username = usernamePrompt(stdin, stderr)
	}
	if options.trustFailures != "" {
		acceptedFailures, err := parseTrustFailures(options.trustFailures)
		if err != nil {
			return nil, err
		}
		baton.Prompt.SSLServerTrust = func(_ context.Context, _ string, failures uint32, _ string, _ bool) (*auth.Credentials, error) {
			if failures&^acceptedFailures != 0 {
				return nil, fmt.Errorf("%w: server certificate has unaccepted failures", svn.ErrAuthnCredsUnavailable)
			}
			return &auth.Credentials{Failures: failures, MaySave: !options.noAuthCache}, nil
		}
	}
	return &ra.Callbacks{Auth: baton, Config: configuration}, nil
}

type staticProvider struct {
	username string
	password string
}

func (provider staticProvider) Get(_ context.Context, kind auth.Kind, realm, preferredUsername string) (*auth.Credentials, error) {
	username := provider.username
	if username == "" {
		username = preferredUsername
	}
	switch kind {
	case auth.Simple:
		return &auth.Credentials{Kind: kind, Realm: realm, Username: username, Password: provider.password}, nil
	case auth.Username:
		return &auth.Credentials{Kind: kind, Realm: realm, Username: username}, nil
	default:
		return nil, fmt.Errorf("%w: %s", svn.ErrAuthnCredsUnavailable, kind)
	}
}

func (staticProvider) Save(context.Context, *auth.Credentials) error {
	return fmt.Errorf("%w: static credentials cannot be saved", svn.ErrAuthnCredsNotSaved)
}

func parseTrustFailures(value string) (uint32, error) {
	known := map[string]uint32{
		"not-yet-valid": sslNotYetValid,
		"expired":       sslExpired,
		"cn-mismatch":   sslCNMismatch,
		"unknown-ca":    sslUnknownCA,
		"other":         sslOther,
	}
	var failures uint32
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		failure, ok := known[name]
		if !ok {
			return 0, fmt.Errorf("invalid certificate failure %q", name)
		}
		failures |= failure
	}
	return failures, nil
}

func simplePrompt(stdin io.Reader, stderr io.Writer) func(context.Context, string, string, bool) (*auth.Credentials, error) {
	return func(ctx context.Context, realm, username string, maySave bool) (*auth.Credentials, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if username == "" {
			value, err := readPromptLine(stdin, stderr, "Username: ")
			if err != nil {
				return nil, err
			}
			username = value
		}
		fmt.Fprintf(stderr, "Authentication realm: %s\nPassword for '%s': ", realm, username)
		password, err := readPassword(stdin)
		fmt.Fprintln(stderr)
		if err != nil {
			return nil, err
		}
		return &auth.Credentials{Username: username, Password: password, MaySave: maySave}, nil
	}
}

func usernamePrompt(stdin io.Reader, stderr io.Writer) func(context.Context, string, string, bool) (*auth.Credentials, error) {
	return func(ctx context.Context, _ string, username string, maySave bool) (*auth.Credentials, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if username == "" {
			value, err := readPromptLine(stdin, stderr, "Username: ")
			if err != nil {
				return nil, err
			}
			username = value
		}
		return &auth.Credentials{Username: username, MaySave: maySave}, nil
	}
}

func readPromptLine(input io.Reader, output io.Writer, prompt string) (string, error) {
	fmt.Fprint(output, prompt)
	line, err := bufio.NewReader(input).ReadString('\n')
	return strings.TrimSpace(line), err
}

func readPassword(input io.Reader) (string, error) {
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		value, err := term.ReadPassword(int(file.Fd()))
		return string(value), err
	}
	line, err := bufio.NewReader(input).ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}
