package ra

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/otuschhoff/go-svn/svn"
)

type Factory func(context.Context, *url.URL, *Callbacks) (Session, string, error)

var registry = struct {
	sync.RWMutex
	factories map[string]Factory
}{factories: make(map[string]Factory)}

func Register(scheme string, factory Factory) {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme == "" {
		panic("ra: empty scheme")
	}
	if factory == nil {
		panic("ra: nil factory for " + scheme)
	}
	registry.Lock()
	defer registry.Unlock()
	if _, exists := registry.factories[scheme]; exists {
		panic("ra: duplicate scheme " + scheme)
	}
	registry.factories[scheme] = factory
}

func Open(ctx context.Context, rawURL string, callbacks *Callbacks) (Session, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" {
		return nil, "", fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, rawURL)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	registry.RLock()
	factory := registry.factories[parsed.Scheme]
	if factory == nil && strings.HasPrefix(parsed.Scheme, "svn+") {
		factory = registry.factories["svn+*"]
	}
	registry.RUnlock()
	if factory == nil {
		return nil, "", fmt.Errorf("%w: unsupported repository access scheme %q", svn.ErrRAIllegalURL, parsed.Scheme)
	}
	if callbacks == nil {
		callbacks = &Callbacks{}
	}
	session, corrected, err := factory(ctx, parsed, callbacks)
	if err != nil {
		return nil, corrected, err
	}
	if session == nil {
		return nil, corrected, fmt.Errorf("%w: %s factory returned a nil session", svn.ErrRACannotCreateSession, parsed.Scheme)
	}
	return session, corrected, nil
}
