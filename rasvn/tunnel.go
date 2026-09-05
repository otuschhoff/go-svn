package rasvn

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

func tunnelConnection(ctx context.Context, parsed *url.URL, callbacks *ra.Callbacks) (*connection, error) {
	tunnelName := strings.TrimPrefix(parsed.Scheme, "svn+")
	username := ""
	if parsed.User != nil {
		username = parsed.User.Username()
	}
	port := 0
	if parsed.Port() != "" {
		if _, err := fmt.Sscanf(parsed.Port(), "%d", &port); err != nil {
			return nil, fmt.Errorf("%w: invalid tunnel port %q", svn.ErrRAIllegalURL, parsed.Port())
		}
	}
	var stream io.ReadWriteCloser
	var err error
	if callbacks != nil && callbacks.Tunnel != nil {
		stream, err = callbacks.Tunnel(ctx, tunnelName, username, port, parsed.Hostname())
	} else {
		stream, err = openTunnelProcess(tunnelName, parsed, callbacks)
	}
	if err != nil {
		return nil, err
	}
	return newConnection(stream, callbacks, username, "", true), nil
}

func openTunnelProcess(tunnelName string, parsed *url.URL, callbacks *ra.Callbacks) (io.ReadWriteCloser, error) {
	commandLine := ""
	if callbacks != nil && callbacks.Config != nil {
		commandLine = callbacks.Config.Get("tunnels", tunnelName, "")
	}
	if tunnelName == "ssh" {
		if override := os.Getenv("SVN_SSH"); override != "" {
			commandLine = override
		}
		if commandLine == "" {
			commandLine = "ssh -q --"
		}
	}
	commandLine, err := resolveTunnelCommand(commandLine)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrRACannotCreateTunnel, err)
	}
	if commandLine == "" {
		return nil, fmt.Errorf("%w: undefined tunnel scheme %q", svn.ErrRACannotCreateTunnel, tunnelName)
	}
	arguments, err := splitCommand(commandLine)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrRACannotCreateTunnel, err)
	}
	if len(arguments) == 0 {
		return nil, fmt.Errorf("%w: empty tunnel command", svn.ErrRACannotCreateTunnel)
	}
	host, err := tunnelHostInfo(parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrRAIllegalURL, err)
	}
	arguments = append(arguments, host, "svnserve", "-t")
	command := exec.Command(arguments[0], arguments[1:]...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("%w: %v", svn.ErrRACannotCreateTunnel, err)
	}
	return &processStream{Reader: stdout, Writer: stdin, command: command}, nil
}

func tunnelHostInfo(parsed *url.URL) (string, error) {
	host := parsed.Host
	if parsed.User != nil && parsed.User.Username() != "" {
		host = parsed.User.Username() + "@" + host
	}
	if host == "" || host[0] == '-' {
		return "", fmt.Errorf("invalid tunnel host %q", host)
	}
	for _, value := range host {
		if (value < 'a' || value > 'z') && (value < 'A' || value > 'Z') && (value < '0' || value > '9') && !strings.ContainsRune(":.-_[]@", value) {
			return "", fmt.Errorf("invalid tunnel host %q", host)
		}
	}
	return host, nil
}

func resolveTunnelCommand(commandLine string) (string, error) {
	commandLine = strings.TrimSpace(commandLine)
	if !strings.HasPrefix(commandLine, "$") {
		return commandLine, nil
	}
	separator := strings.IndexAny(commandLine, " \t\n")
	variable, fallback := commandLine[1:], ""
	if separator >= 0 {
		variable = commandLine[1:separator]
		fallback = strings.TrimSpace(commandLine[separator:])
	}
	if variable == "" {
		return "", fmt.Errorf("empty tunnel environment variable")
	}
	if override := os.Getenv(variable); override != "" {
		return override, nil
	}
	if fallback == "" {
		return "", fmt.Errorf("tunnel environment variable %s is not defined", variable)
	}
	return fallback, nil
}

func splitCommand(command string) ([]string, error) {
	var arguments []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			arguments = append(arguments, current.String())
			current.Reset()
		}
	}
	for _, value := range command {
		if escaped {
			current.WriteRune(value)
			escaped = false
			continue
		}
		if value == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if value == quote {
				quote = 0
			} else {
				current.WriteRune(value)
			}
			continue
		}
		if value == '\'' || value == '"' {
			quote = value
			continue
		}
		if value == ' ' || value == '\t' || value == '\n' {
			flush()
			continue
		}
		current.WriteRune(value)
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape in tunnel command")
	}
	flush()
	return arguments, nil
}

type processStream struct {
	io.Reader
	io.Writer
	command *exec.Cmd
	once    sync.Once
	err     error
}

func (stream *processStream) Close() error {
	stream.once.Do(func() {
		if closer, ok := stream.Writer.(io.Closer); ok {
			_ = closer.Close()
		}
		if closer, ok := stream.Reader.(io.Closer); ok {
			_ = closer.Close()
		}
		done := make(chan error, 1)
		go func() { done <- stream.command.Wait() }()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case stream.err = <-done:
		case <-timer.C:
			if stream.command.Process != nil {
				_ = stream.command.Process.Kill()
			}
			<-done
		}
	})
	return stream.err
}
