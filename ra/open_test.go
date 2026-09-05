package ra

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

type stubSession struct {
	Session
	url string
}

func (session *stubSession) URL() string  { return session.url }
func (session *stubSession) Close() error { return nil }

func TestOpenUsesRegisteredScheme(t *testing.T) {
	const scheme = "ra-test"
	Register(scheme, func(_ context.Context, parsed *url.URL, callbacks *Callbacks) (Session, string, error) {
		if callbacks == nil {
			t.Fatal("nil callbacks")
		}
		return &stubSession{url: parsed.String()}, "ra-test://example.com/corrected", nil
	})
	session, corrected, err := Open(context.Background(), "RA-TEST://example.com/repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.URL() != "ra-test://example.com/repo" {
		t.Fatalf("URL = %q", session.URL())
	}
	if corrected != "ra-test://example.com/corrected" {
		t.Fatalf("corrected URL = %q", corrected)
	}
}

func TestOpenRejectsInvalidAndUnsupportedURLs(t *testing.T) {
	for _, value := range []string{"relative/path", "unknown://example.com/repo"} {
		_, _, err := Open(context.Background(), value, nil)
		if !errors.Is(err, svn.ErrRAIllegalURL) {
			t.Errorf("Open(%q) error = %v", value, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := Open(ctx, "unknown://example.com", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}
