package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func main() {
	var svnPath string
	var svnservePath string
	var repository string
	var output string
	flag.StringVar(&svnPath, "svn", "svn", "path to the svn client")
	flag.StringVar(&svnservePath, "svnserve", "svnserve", "path to svnserve")
	flag.StringVar(&repository, "repository", "", "repository to query")
	flag.StringVar(&output, "output", "", "transcript output path")
	flag.Parse()

	if repository == "" || output == "" {
		fatalf("-repository and -output are required")
	}
	if err := record(svnPath, svnservePath, repository, output); err != nil {
		fatalf("record transcript: %v", err)
	}
}

func record(svnPath, svnservePath, repository, output string) error {
	repository, err := filepath.Abs(repository)
	if err != nil {
		return err
	}

	serverAddress, err := unusedAddress()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var serverLog bytes.Buffer
	server := exec.CommandContext(ctx, svnservePath,
		"--foreground", "-d",
		"--listen-host", "127.0.0.1",
		"--listen-port", portOf(serverAddress),
		"-r", filepath.Dir(repository),
	)
	server.Stdout = &serverLog
	server.Stderr = &serverLog
	if err := server.Start(); err != nil {
		return fmt.Errorf("start svnserve: %w", err)
	}
	defer stopProcess(server)
	if err := waitForTCP(ctx, serverAddress); err != nil {
		return fmt.Errorf("wait for svnserve: %w; output: %s", err, serverLog.String())
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()

	capture := make(chan capturedStreams, 1)
	go proxyOne(listener, serverAddress, capture)

	repositoryURL := fmt.Sprintf("svn://%s/%s", listener.Addr().String(), filepath.Base(repository))
	configDir, err := os.MkdirTemp("", "go-svn-transcript-config-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(configDir)

	client := exec.CommandContext(ctx, svnPath,
		"--non-interactive", "--no-auth-cache", "--config-dir", configDir,
		"info", repositoryURL,
	)
	clientOutput, clientErr := client.CombinedOutput()
	if clientErr != nil {
		return fmt.Errorf("svn info: %w: %s", clientErr, clientOutput)
	}

	var streams capturedStreams
	select {
	case streams = <-capture:
		if streams.err != nil {
			return streams.err
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	streams.client = normalizeClientIdentity(streams.client)
	proxyPort := portOf(listener.Addr().String())
	portMarker := strings.Repeat("0", len(proxyPort))
	streams.client = bytes.ReplaceAll(streams.client, []byte(":"+proxyPort), []byte(":"+portMarker))
	streams.server = bytes.ReplaceAll(streams.server, []byte(":"+proxyPort), []byte(":"+portMarker))
	transcript := fmt.Sprintf(
		"# go-svn ra_svn transcript v1\n# generator: reference svn\n# operation: info /%s\n# endpoint port normalized to %s\n# client identity normalized to SVN/fixture (go-svn)\nclient-base64: %s\nserver-base64: %s\n",
		filepath.Base(repository),
		portMarker,
		base64.StdEncoding.EncodeToString(streams.client),
		base64.StdEncoding.EncodeToString(streams.server),
	)
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	return os.WriteFile(output, []byte(transcript), 0o644)
}

func normalizeClientIdentity(stream []byte) []byte {
	marker := []byte(":SVN/")
	colon := bytes.Index(stream, marker)
	if colon < 1 {
		return stream
	}
	start := colon - 1
	for start >= 0 && stream[start] >= '0' && stream[start] <= '9' {
		start--
	}
	start++
	length, err := strconv.Atoi(string(stream[start:colon]))
	if err != nil || length < 0 || colon+1+length > len(stream) {
		return stream
	}
	replacement := []byte("SVN/fixture (go-svn)")
	result := make([]byte, 0, len(stream)-length+len(replacement))
	result = append(result, stream[:start]...)
	result = strconv.AppendInt(result, int64(len(replacement)), 10)
	result = append(result, ':')
	result = append(result, replacement...)
	result = append(result, stream[colon+1+length:]...)
	return result
}

type capturedStreams struct {
	client []byte
	server []byte
	err    error
}

func proxyOne(listener net.Listener, target string, result chan<- capturedStreams) {
	client, err := listener.Accept()
	if err != nil {
		result <- capturedStreams{err: err}
		return
	}
	defer client.Close()
	server, err := net.Dial("tcp", target)
	if err != nil {
		result <- capturedStreams{err: err}
		return
	}
	defer server.Close()

	var clientBytes bytes.Buffer
	var serverBytes bytes.Buffer
	var wait sync.WaitGroup
	wait.Add(2)
	go copyStream(&wait, server, io.TeeReader(client, &clientBytes))
	go copyStream(&wait, client, io.TeeReader(server, &serverBytes))
	wait.Wait()
	result <- capturedStreams{client: clientBytes.Bytes(), server: serverBytes.Bytes()}
}

func copyStream(wait *sync.WaitGroup, destination net.Conn, source io.Reader) {
	defer wait.Done()
	_, _ = io.Copy(destination, source)
	if connection, ok := destination.(*net.TCPConn); ok {
		_ = connection.CloseWrite()
	}
}

func unusedAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return "", err
	}
	return address, nil
}

func portOf(address string) string {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		panic(err)
	}
	return port
}

func waitForTCP(ctx context.Context, address string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func stopProcess(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = command.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-done
	}
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", values...)
	os.Exit(1)
}
