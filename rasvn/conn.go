package rasvn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

const protocolVersion = 2

var clientCapabilities = []string{
	"edit-pipeline", "svndiff1", "accepts-svndiff2", "absent-entries",
	"depth", "mergeinfo", "log-revprops",
}

type connection struct {
	stream    io.ReadWriteCloser
	reader    *Reader
	writer    *Writer
	callbacks *ra.Callbacks
	username  string
	password  string
	secure    bool
	mu        sync.Mutex
	closed    bool
}

type handshakeInfo struct {
	uuid          string
	repositoryURL string
	capabilities  map[string]bool
}

func dialConnection(ctx context.Context, parsed *url.URL, callbacks *ra.Callbacks) (*connection, error) {
	host := parsed.Host
	if parsed.Port() == "" {
		host = net.JoinHostPort(parsed.Hostname(), "3690")
	}
	stream, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	username, password := "", ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	return newConnection(stream, callbacks, username, password, false), nil
}

func newConnection(stream io.ReadWriteCloser, callbacks *ra.Callbacks, username, password string, secure bool) *connection {
	if callbacks == nil {
		callbacks = &ra.Callbacks{}
	}
	return &connection{
		stream: stream, reader: NewReader(stream), writer: NewWriter(stream), callbacks: callbacks,
		username: username, password: password, secure: secure,
	}
}

func (conn *connection) handshake(ctx context.Context, rawURL string) (info *handshakeInfo, resultErr error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	defer func() { resultErr = normalizeContextError(ctx, resultErr) }()
	stop, err := conn.interruptOnCancel(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()

	greeting, err := conn.readResponse()
	if err != nil {
		return nil, err
	}
	if len(greeting) < 4 || greeting[0].Kind != NumberKind || greeting[1].Kind != NumberKind ||
		greeting[0].Number > protocolVersion || greeting[1].Number < protocolVersion {
		return nil, malformed("server does not support protocol version %d", protocolVersion)
	}
	negotiated := make(map[string]bool)
	if greeting[3].Kind != ListKind {
		return nil, malformed("server capabilities are not a list")
	}
	for _, item := range greeting[3].List {
		if item.Kind != WordKind {
			return nil, malformed("server capability is not a word")
		}
		negotiated[item.Word] = true
	}
	capabilities := make([]Item, 0, len(clientCapabilities))
	for _, capability := range clientCapabilities {
		capabilities = append(capabilities, Word(capability))
	}
	response := List(Number(protocolVersion), List(capabilities...), String([]byte(rawURL)), String([]byte("go-svn/0")), List())
	if err := conn.write(response); err != nil {
		return nil, err
	}
	if err := conn.authenticate(ctx); err != nil {
		return nil, err
	}
	repository, err := conn.readResponse()
	if err != nil {
		return nil, err
	}
	if len(repository) < 3 || repository[0].Kind != StringKind || repository[1].Kind != StringKind || repository[2].Kind != ListKind {
		return nil, malformed("invalid repository information")
	}
	info = &handshakeInfo{
		uuid: string(repository[0].String), repositoryURL: string(repository[1].String), capabilities: negotiated,
	}
	for _, item := range repository[2].List {
		if item.Kind != WordKind {
			return nil, malformed("repository capability is not a word")
		}
		info.capabilities[item.Word] = true
	}
	return info, nil
}

func (conn *connection) command(ctx context.Context, name string, parameters ...Item) (result []Item, resultErr error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	defer func() { resultErr = normalizeContextError(ctx, resultErr) }()
	stop, err := conn.interruptOnCancel(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()
	if err := conn.write(List(Word(name), List(parameters...))); err != nil {
		return nil, err
	}
	if err := conn.authenticate(ctx); err != nil {
		return nil, err
	}
	return conn.readResponse()
}

func (conn *connection) streamCommand(ctx context.Context, name string, body func() error, parameters ...Item) (resultErr error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	defer func() { resultErr = normalizeContextError(ctx, resultErr) }()
	stop, err := conn.interruptOnCancel(ctx)
	if err != nil {
		return err
	}
	defer stop()
	if err := conn.write(List(Word(name), List(parameters...))); err != nil {
		return err
	}
	if err := conn.authenticate(ctx); err != nil {
		return err
	}
	return body()
}

func (conn *connection) authenticate(ctx context.Context) error {
	request, err := conn.readResponse()
	if err != nil {
		return err
	}
	if len(request) < 2 || request[0].Kind != ListKind || request[1].Kind != StringKind {
		return malformed("invalid authentication request")
	}
	if len(request[0].List) == 0 {
		return nil
	}
	mechanisms := make([]string, 0, len(request[0].List))
	for _, item := range request[0].List {
		if item.Kind != WordKind {
			return malformed("authentication mechanism is not a word")
		}
		mechanisms = append(mechanisms, item.Word)
	}
	return conn.runSASL(ctx, mechanisms, string(request[1].String))
}

func (conn *connection) write(item Item) error {
	if err := conn.writer.Encode(item); err != nil {
		return err
	}
	return conn.writer.Flush()
}

func (conn *connection) readResponse() ([]Item, error) {
	item, err := conn.reader.Decode()
	if err != nil {
		return nil, err
	}
	if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != WordKind || item.List[1].Kind != ListKind {
		return nil, malformed("invalid command response")
	}
	switch item.List[0].Word {
	case "success":
		return item.List[1].List, nil
	case "failure":
		return nil, decodeFailure(item.List[1].List)
	default:
		return nil, malformed("unknown response status %q", item.List[0].Word)
	}
}

func decodeFailure(items []Item) error {
	var child error
	var commandErr error
	for index := len(items) - 1; index >= 0; index-- {
		fields := items[index]
		if fields.Kind != ListKind || len(fields.List) < 4 || fields.List[0].Kind != NumberKind || fields.List[1].Kind != StringKind {
			return malformed("invalid error response")
		}
		code := svn.Code(fields.List[0].Number)
		if code == svn.ErrRASvnCmdErr {
			commandErr = &svn.Error{Code: code, Message: string(fields.List[1].String)}
			continue
		}
		message := string(fields.List[1].String)
		child = &svn.Error{Code: code, Message: message, Child: child}
	}
	if child == nil {
		if commandErr != nil {
			return commandErr
		}
		return malformed("empty error response")
	}
	return child
}

func (conn *connection) interruptOnCancel(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if deadlineConn, ok := conn.stream.(interface{ SetDeadline(time.Time) error }); ok {
			if err := deadlineConn.SetDeadline(deadline); err != nil {
				return nil, err
			}
			stop := context.AfterFunc(ctx, func() { _ = deadlineConn.SetDeadline(time.Now()) })
			return func() {
				stop()
				_ = deadlineConn.SetDeadline(time.Time{})
			}, nil
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.stream.Close() })
	return func() { stop() }, nil
}

func (conn *connection) Close() error {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.closed {
		return nil
	}
	conn.closed = true
	return conn.stream.Close()
}

func normalizeContextError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	var networkError net.Error
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) && errors.As(err, &networkError) && networkError.Timeout() {
		return context.DeadlineExceeded
	}
	if errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("%w: %v", svn.ErrRASvnConnectionClosed, err)
	}
	return err
}
