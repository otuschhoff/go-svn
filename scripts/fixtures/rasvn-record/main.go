package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/rasvn"
	"github.com/otuschhoff/go-svn/svn"
)

func main() {
	var svnPath string
	var svnservePath string
	var repository string
	var output string
	var operation string
	flag.StringVar(&svnPath, "svn", "svn", "path to the svn client")
	flag.StringVar(&svnservePath, "svnserve", "svnserve", "path to svnserve")
	flag.StringVar(&repository, "repository", "", "repository to query")
	flag.StringVar(&output, "output", "", "transcript output path")
	flag.StringVar(&operation, "operation", "info", "operation to record: info or read-matrix")
	flag.Parse()

	if repository == "" || output == "" {
		fatalf("-repository and -output are required")
	}
	if err := record(svnPath, svnservePath, repository, output, operation); err != nil {
		fatalf("record transcript: %v", err)
	}
}

func record(svnPath, svnservePath, repository, output, operation string) error {
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

	if operation == "info" {
		client := exec.CommandContext(ctx, svnPath,
			"--non-interactive", "--no-auth-cache", "--config-dir", configDir,
			"info", repositoryURL,
		)
		clientOutput, clientErr := client.CombinedOutput()
		if clientErr != nil {
			return fmt.Errorf("svn info: %w: %s", clientErr, clientOutput)
		}
	} else if operation == "read-matrix" {
		if err := runReadMatrix(ctx, repositoryURL); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("unknown operation %q", operation)
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
	if operation == "read-matrix" {
		streams.client, err = canonicalizeWire(streams.client)
		if err != nil {
			return fmt.Errorf("canonicalize client transcript: %w", err)
		}
		streams.server, err = canonicalizeWire(streams.server)
		if err != nil {
			return fmt.Errorf("canonicalize server transcript: %w", err)
		}
	}
	generator := "svnserve"
	clientIdentity := "normalized when present"
	if operation == "info" {
		generator = "reference svn"
		clientIdentity = "normalized to SVN/fixture (go-svn)"
	}
	transcript := fmt.Sprintf(
		"# go-svn ra_svn transcript v1\n# generator: %s\n# operation: %s /%s\n# endpoint port normalized to %s\n# client identity %s\nclient-base64: %s\nserver-base64: %s\n",
		generator,
		operation,
		filepath.Base(repository),
		portMarker,
		clientIdentity,
		base64.StdEncoding.EncodeToString(streams.client),
		base64.StdEncoding.EncodeToString(streams.server),
	)
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	return os.WriteFile(output, []byte(transcript), 0o644)
}

func canonicalizeWire(wire []byte) ([]byte, error) {
	reader := rasvn.NewReader(bytes.NewReader(wire))
	items := make([]rasvn.Item, 0)
	for {
		item, err := reader.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		canonicalizeItem(&item)
		items = append(items, item)
	}
	for start := 0; start < len(items); {
		if _, ok := propertyCommandKey(items[start]); !ok {
			start++
			continue
		}
		end := start + 1
		for end < len(items) {
			if _, ok := propertyCommandKey(items[end]); !ok {
				break
			}
			end++
		}
		sort.Slice(items[start:end], func(left, right int) bool {
			leftKey, _ := propertyCommandKey(items[start+left])
			rightKey, _ := propertyCommandKey(items[start+right])
			return leftKey < rightKey
		})
		start = end
	}
	var canonical bytes.Buffer
	writer := rasvn.NewWriter(&canonical)
	for _, item := range items {
		if err := writer.Encode(item); err != nil {
			return nil, err
		}
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return canonical.Bytes(), nil
}

func canonicalizeItem(item *rasvn.Item) {
	for index := range item.List {
		canonicalizeItem(&item.List[index])
	}
	if len(item.List) < 2 {
		return
	}
	for _, child := range item.List {
		if child.Kind != rasvn.ListKind || len(child.List) < 2 || child.List[0].Kind != rasvn.StringKind {
			return
		}
	}
	sort.Slice(item.List, func(left, right int) bool {
		return bytes.Compare(item.List[left].List[0].String, item.List[right].List[0].String) < 0
	})
}

func propertyCommandKey(item rasvn.Item) (string, bool) {
	if item.Kind != rasvn.ListKind || len(item.List) != 2 || item.List[0].Kind != rasvn.WordKind || item.List[1].Kind != rasvn.ListKind {
		return "", false
	}
	command := item.List[0].Word
	if command != "change-dir-prop" && command != "change-file-prop" {
		return "", false
	}
	arguments := item.List[1].List
	if len(arguments) < 2 || arguments[0].Kind != rasvn.StringKind || arguments[1].Kind != rasvn.StringKind {
		return "", false
	}
	return command + "\x00" + string(arguments[0].String) + "\x00" + string(arguments[1].String), true
}

func runReadMatrix(ctx context.Context, repositoryURL string) error {
	session, _, err := ra.Open(ctx, repositoryURL, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	latest, err := session.LatestRevision(ctx)
	if err != nil {
		return err
	}
	if _, err := session.DatedRevision(ctx, time.Date(2020, 1, 5, 0, 0, 0, 0, time.UTC)); err != nil {
		return err
	}
	if _, err := session.RevProps(ctx, latest); err != nil {
		return err
	}
	if _, _, err := session.RevProp(ctx, latest, "svn:log"); err != nil {
		return err
	}
	if _, err := session.CheckPath(ctx, "trunk/README.txt", latest); err != nil {
		return err
	}
	if _, err := session.Stat(ctx, "trunk/README.txt", latest); err != nil {
		return err
	}
	if _, _, err := session.GetFile(ctx, "trunk/README.txt", latest, io.Discard, true); err != nil {
		return err
	}
	if _, _, _, err := session.GetDir(ctx, "trunk", latest, svn.DirentAll); err != nil {
		return err
	}
	if err := session.List(ctx, "trunk", latest, []string{"*"}, svn.DepthInfinity, svn.DirentAll, nil); err != nil {
		return err
	}
	if err := session.Log(ctx, ra.LogOptions{Paths: []string{"trunk"}, Start: latest, End: 0, DiscoverChangedPaths: true}, nil); err != nil {
		return err
	}
	if _, err := session.GetLocations(ctx, "trunk/README.txt", latest, []svn.Revnum{2, latest}); err != nil {
		return err
	}
	if err := session.GetLocationSegments(ctx, "trunk/README.txt", latest, latest, 0, nil); err != nil {
		return err
	}
	if err := session.GetFileRevs(ctx, "trunk/README.txt", 1, latest, false, nil); err != nil {
		return err
	}
	if _, err := session.GetMergeinfo(ctx, []string{"trunk"}, latest, mergeinfo.InheritanceExplicit, false); err != nil {
		return err
	}
	if _, err := session.GetInheritedProps(ctx, "trunk/README.txt", latest); err != nil {
		return err
	}
	if _, err := session.GetDeletedRev(ctx, "trunk/run.sh", latest-1, latest); err != nil {
		return err
	}
	if _, err := session.GetLock(ctx, "trunk/README.txt"); err != nil {
		return err
	}
	if _, err := session.GetLocks(ctx, "", svn.DepthInfinity); err != nil {
		return err
	}
	operations := []func(delta.Editor) (ra.Reporter, error){
		func(editor delta.Editor) (ra.Reporter, error) {
			return session.DoUpdate(ctx, latest, "", svn.DepthInfinity, true, false, editor)
		},
		func(editor delta.Editor) (ra.Reporter, error) {
			return session.DoSwitch(ctx, latest, "", svn.DepthInfinity, repositoryURL, true, false, editor)
		},
		func(editor delta.Editor) (ra.Reporter, error) {
			return session.DoStatus(ctx, "", latest, svn.DepthInfinity, editor)
		},
		func(editor delta.Editor) (ra.Reporter, error) {
			return session.DoDiff(ctx, latest, "", svn.DepthInfinity, false, true, repositoryURL, editor)
		},
	}
	for _, start := range operations {
		reporter, err := start(discardEditor{})
		if err != nil {
			return err
		}
		if err := reporter.SetPath(ctx, "", 0, svn.DepthInfinity, true, ""); err != nil {
			return err
		}
		if err := reporter.FinishReport(ctx); err != nil {
			return err
		}
	}
	if err := session.Replay(ctx, 1, 0, true, discardEditor{}); err != nil {
		return err
	}
	if err := session.ReplayRange(ctx, 1, latest, 0, true,
		func(svn.Revnum, svn.Props) (delta.Editor, error) { return discardEditor{}, nil },
		func(svn.Revnum, svn.Props, delta.Editor) error { return nil }); err != nil {
		return err
	}
	return session.Reparent(ctx, repositoryURL+"/trunk")
}

type discardEditor struct{}

func (discardEditor) SetTargetRevision(context.Context, svn.Revnum) error { return nil }
func (discardEditor) OpenRoot(context.Context, svn.Revnum) (delta.DirEditor, error) {
	return discardDir{}, nil
}
func (discardEditor) CloseEdit(context.Context) error { return nil }
func (discardEditor) AbortEdit(context.Context) error { return nil }

type discardDir struct{}

func (discardDir) DeleteEntry(context.Context, string, svn.Revnum) error { return nil }
func (discardDir) AddDirectory(context.Context, string, *delta.CopySource) (delta.DirEditor, error) {
	return discardDir{}, nil
}
func (discardDir) OpenDirectory(context.Context, string, svn.Revnum) (delta.DirEditor, error) {
	return discardDir{}, nil
}
func (discardDir) ChangeProp(context.Context, string, []byte) error { return nil }
func (discardDir) AbsentDirectory(context.Context, string) error    { return nil }
func (discardDir) AddFile(context.Context, string, *delta.CopySource) (delta.FileEditor, error) {
	return discardFile{}, nil
}
func (discardDir) OpenFile(context.Context, string, svn.Revnum) (delta.FileEditor, error) {
	return discardFile{}, nil
}
func (discardDir) AbsentFile(context.Context, string) error { return nil }
func (discardDir) Close(context.Context) error              { return nil }

type discardFile struct{}

func (discardFile) ApplyTextDelta(context.Context, *svn.Checksum) (delta.WindowHandler, error) {
	return delta.WindowHandlerFunc(func(*delta.Window) error { return nil }), nil
}
func (discardFile) ChangeProp(context.Context, string, []byte) error { return nil }
func (discardFile) Close(context.Context, *svn.Checksum) error       { return nil }

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
