package rasvn

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/oliver-tuschhoff/go-svn/auth"
	"github.com/oliver-tuschhoff/go-svn/ra"
)

func TestAnonymousHandshake(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer clientStream.Close()
	defer serverStream.Close()
	serverDone := make(chan error, 1)
	go func() {
		reader, writer := NewReader(serverStream), NewWriter(serverStream)
		if err := writeTestItem(writer, response("success", Number(2), Number(2), List(), List(Word("edit-pipeline")))); err != nil {
			serverDone <- err
			return
		}
		clientGreeting, err := reader.Decode()
		if err != nil {
			serverDone <- err
			return
		}
		if clientGreeting.Kind != ListKind || clientGreeting.List[0].Number != 2 {
			serverDone <- malformed("unexpected client greeting")
			return
		}
		if err := writeTestItem(writer, response("success", List(Word("ANONYMOUS")), String([]byte("realm")))); err != nil {
			serverDone <- err
			return
		}
		authResponse, err := reader.Decode()
		if err != nil {
			serverDone <- err
			return
		}
		if authResponse.List[0].Word != "ANONYMOUS" {
			serverDone <- malformed("unexpected mechanism")
			return
		}
		if err := writeTestItem(writer, List(Word("success"), List())); err != nil {
			serverDone <- err
			return
		}
		serverDone <- writeTestItem(writer, response("success", String([]byte("uuid")), String([]byte("svn://host/repo")), List(Word("mergeinfo"))))
	}()

	conn := newConnection(clientStream, &ra.Callbacks{}, "", "", false)
	info, err := conn.handshake(context.Background(), "svn://host/repo/trunk")
	if err != nil {
		t.Fatal(err)
	}
	if info.uuid != "uuid" || info.repositoryURL != "svn://host/repo" || !info.capabilities["mergeinfo"] {
		t.Fatalf("handshake info = %#v", info)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestCRAMMD5RFC2195Vector(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer clientStream.Close()
	defer serverStream.Close()
	serverDone := make(chan error, 1)
	challenge := "<1896.697170952@postoffice.reston.mci.net>"
	go func() {
		reader, writer := NewReader(serverStream), NewWriter(serverStream)
		response, err := reader.Decode()
		if err != nil {
			serverDone <- err
			return
		}
		if response.List[0].Word != "CRAM-MD5" {
			serverDone <- malformed("unexpected mechanism")
			return
		}
		if err := writeTestItem(writer, List(Word("step"), List(String([]byte(challenge))))); err != nil {
			serverDone <- err
			return
		}
		token, err := reader.Decode()
		if err != nil {
			serverDone <- err
			return
		}
		if got, want := string(token.String), "tim b913a602c7eda7a495b4e6e7334d3890"; got != want {
			serverDone <- malformed("CRAM response %q, want %q", got, want)
			return
		}
		serverDone <- writeTestItem(writer, List(Word("success"), List()))
	}()
	conn := newConnection(clientStream, &ra.Callbacks{}, "tim", "tanstaaftanstaaf", false)
	if err := conn.runSASL(context.Background(), []string{"CRAM-MD5"}, "realm"); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestCRAMMD5RetriesNextCredential(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer clientStream.Close()
	defer serverStream.Close()
	serverDone := make(chan error, 1)
	challenge := "<retry@example>"
	go func() {
		reader, writer := NewReader(serverStream), NewWriter(serverStream)
		for attempt := 0; attempt < 2; attempt++ {
			response, err := reader.Decode()
			if err != nil {
				serverDone <- err
				return
			}
			if response.List[0].Word != "CRAM-MD5" {
				serverDone <- malformed("unexpected mechanism")
				return
			}
			if err := writeTestItem(writer, List(Word("step"), List(String([]byte(challenge))))); err != nil {
				serverDone <- err
				return
			}
			token, err := reader.Decode()
			if err != nil {
				serverDone <- err
				return
			}
			if attempt == 0 {
				if err := writeTestItem(writer, List(Word("failure"), List(String([]byte("bad password"))))); err != nil {
					serverDone <- err
					return
				}
				continue
			}
			mac := hmac.New(md5.New, []byte("correct"))
			_, _ = mac.Write([]byte(challenge))
			want := "user " + hex.EncodeToString(mac.Sum(nil))
			if string(token.String) != want {
				serverDone <- malformed("retry response %q, want %q", token.String, want)
				return
			}
			serverDone <- writeTestItem(writer, List(Word("success"), List()))
		}
	}()
	prompted := 0
	callbacks := &ra.Callbacks{Auth: auth.Baton{Prompt: auth.Prompt{Simple: func(context.Context, string, string, bool) (*auth.Credentials, error) {
		prompted++
		return &auth.Credentials{Username: "user", Password: "correct"}, nil
	}}}}
	conn := newConnection(clientStream, callbacks, "user", "wrong", false)
	if err := conn.runSASL(context.Background(), []string{"CRAM-MD5"}, "realm"); err != nil {
		t.Fatal(err)
	}
	if prompted != 1 {
		t.Fatalf("prompt calls = %d, want 1", prompted)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestSASLReturnsCredentialProviderError(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer clientStream.Close()
	defer serverStream.Close()
	go func() {
		reader, writer := NewReader(serverStream), NewWriter(serverStream)
		_, _ = reader.Decode()
		_ = writeTestItem(writer, List(Word("step"), List(String([]byte("challenge")))))
		_, _ = reader.Decode()
		_ = writeTestItem(writer, List(Word("failure"), List(String([]byte("bad password")))))
	}()
	callbacks := &ra.Callbacks{Auth: auth.Baton{Prompt: auth.Prompt{Simple: func(context.Context, string, string, bool) (*auth.Credentials, error) {
		return nil, context.Canceled
	}}}}
	conn := newConnection(clientStream, callbacks, "user", "wrong", false)
	if err := conn.runSASL(context.Background(), []string{"CRAM-MD5"}, "realm"); !errors.Is(err, context.Canceled) {
		t.Fatalf("SASL error = %v", err)
	}
}

func TestHandshakeCancellationInterruptsRead(t *testing.T) {
	clientStream, serverStream := net.Pipe()
	defer serverStream.Close()
	conn := newConnection(clientStream, &ra.Callbacks{}, "", "", false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := conn.handshake(ctx, "svn://host/repo")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("handshake error = %v", err)
	}
}

func response(status string, items ...Item) Item { return List(Word(status), List(items...)) }

func writeTestItem(writer *Writer, item Item) error {
	if err := writer.Encode(item); err != nil {
		return err
	}
	return writer.Flush()
}
