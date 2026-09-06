package radav

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type Session struct {
	transport *transport
	url       string
	info      ServerInfo
}

func init() {
	ra.Register("http", openSession)
	ra.Register("https", openSession)
}

func openSession(ctx context.Context, parsed *url.URL, callbacks *ra.Callbacks) (ra.Session, string, error) {
	parsed.User = nil
	transport, err := newTransport(callbacks, parsed.Hostname())
	if err != nil {
		return nil, parsed.String(), err
	}
	info, corrected, err := discover(ctx, transport, parsed.String())
	if err != nil {
		return nil, corrected, err
	}
	session := &Session{transport: transport, url: corrected, info: info}
	if info.RevRootStub == "" {
		if err := session.completeLegacyIdentity(ctx); err != nil {
			return nil, corrected, err
		}
	}
	return session, corrected, nil
}

func (session *Session) completeLegacyIdentity(ctx context.Context) error {
	responses, err := session.propfind(ctx, session.url, "0")
	if err != nil {
		return err
	}
	if len(responses) == 0 {
		return malformed("legacy identity PROPFIND returned no responses")
	}
	properties := successfulProperties(responses[0])
	if session.info.UUID == "" {
		if value, ok := findProperty(properties, nsDAVSVN, "repository-uuid"); ok {
			session.info.UUID = strings.TrimSpace(value.Text)
		}
	}
	if value, ok := findProperty(properties, nsDAVSVN, "baseline-relative-path"); ok {
		parsed, parseErr := url.Parse(session.url)
		if parseErr != nil {
			return parseErr
		}
		relative := strings.Trim(strings.TrimSpace(value.Text), "/")
		rootPath := strings.TrimSuffix(path.Clean(parsed.Path), "/")
		if relative != "" {
			suffix := "/" + relative
			if !strings.HasSuffix(rootPath, suffix) {
				return malformed("baseline-relative-path %q does not match session URL", relative)
			}
			rootPath = strings.TrimSuffix(rootPath, suffix)
		}
		parsed.Path = rootPath
		parsed.RawPath = ""
		session.info.RepositoryRoot = parsed.String()
	}
	return nil
}

func (session *Session) URL() string { return session.url }
func (session *Session) Reparent(_ context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	root, rootErr := url.Parse(session.info.RepositoryRoot)
	rootPath := strings.TrimSuffix(path.Clean(root.Path), "/")
	parsedPath := path.Clean(parsed.Path)
	if err != nil || rootErr != nil || parsed.Scheme != root.Scheme || parsed.Host != root.Host || parsed.User != nil || parsedPath != rootPath && !strings.HasPrefix(parsedPath, rootPath+"/") {
		return fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, rawURL)
	}
	session.url = rawURL
	return nil
}
func (session *Session) RepositoryRoot(context.Context) (string, error) {
	return session.info.RepositoryRoot, nil
}
func (session *Session) UUID(context.Context) (string, error) { return session.info.UUID, nil }
func (session *Session) HasCapability(_ context.Context, capability ra.Capability) (bool, error) {
	return session.info.Capabilities[capability], nil
}
func (session *Session) LatestRevision(ctx context.Context) (svn.Revnum, error) {
	if !session.info.Youngest.IsValid() {
		return session.legacyLatestRevision(ctx)
	}
	return session.info.Youngest, nil
}
func (session *Session) RevProps(ctx context.Context, revision svn.Revnum) (svn.Props, error) {
	resource, err := session.revisionPropURL(ctx, revision)
	if err != nil {
		return nil, err
	}
	responses, err := session.propfind(ctx, resource, "0")
	if err != nil {
		return nil, err
	}
	if len(responses) != 1 {
		return nil, malformed("revision PROPFIND returned %d responses", len(responses))
	}
	properties := successfulProperties(responses[0])
	props, err := versionedProps(properties)
	if err != nil {
		return nil, err
	}
	for _, value := range properties {
		switch {
		case value.Name.Space == nsDAV && value.Name.Local == "creator-displayname":
			props["svn:author"] = []byte(value.Text)
		case value.Name.Space == nsDAV && value.Name.Local == "creationdate":
			props["svn:date"] = []byte(value.Text)
		case value.Name.Space == nsDAV && value.Name.Local == "comment":
			props["svn:log"] = []byte(value.Text)
		}
	}
	return props, nil
}
func (session *Session) RevProp(ctx context.Context, revision svn.Revnum, name string) ([]byte, bool, error) {
	props, err := session.RevProps(ctx, revision)
	if err != nil {
		return nil, false, err
	}
	value, found := props[name]
	return value, found, nil
}
func (session *Session) ChangeRevProp(ctx context.Context, revision svn.Revnum, name string, value, oldValue []byte, dontCare bool) error {
	return session.changeRevProp(ctx, revision, name, value, oldValue, dontCare)
}
func (session *Session) CheckPath(ctx context.Context, name string, revision svn.Revnum) (svn.NodeKind, error) {
	entry, err := session.Stat(ctx, name, revision)
	if errors.Is(err, svn.ErrRADAVPathNotFound) || errors.Is(err, svn.ErrFSNotFound) {
		return svn.NodeNone, nil
	}
	if err != nil {
		return svn.NodeUnknown, err
	}
	return entry.Kind, nil
}
func (session *Session) Stat(ctx context.Context, name string, revision svn.Revnum) (*svn.Dirent, error) {
	resource, _, err := session.revisionURL(ctx, revision, name)
	if err != nil {
		return nil, err
	}
	responses, err := session.propfind(ctx, resource, "0")
	if err != nil {
		return nil, err
	}
	if len(responses) == 0 {
		return nil, nil
	}
	return direntFromProperties(path.Base(strings.TrimSuffix(name, "/")), successfulProperties(responses[0]))
}
func (session *Session) GetDir(ctx context.Context, name string, revision svn.Revnum, fields svn.DirentFields) ([]svn.Dirent, svn.Revnum, svn.Props, error) {
	resource, resolved, err := session.revisionURL(ctx, revision, name)
	if err != nil {
		return nil, svn.InvalidRevnum, nil, err
	}
	responses, err := session.propfind(ctx, resource, "1")
	if err != nil {
		return nil, svn.InvalidRevnum, nil, err
	}
	if len(responses) == 0 {
		return nil, svn.InvalidRevnum, nil, fmt.Errorf("%w: %s", svn.ErrFSNotFound, name)
	}
	resourcePath := responsePath(resource)
	var entries []svn.Dirent
	var props svn.Props
	for _, response := range responses {
		properties := successfulProperties(response)
		if responsePath(response.Href) == resourcePath {
			props, err = versionedProps(properties)
			if err != nil {
				return nil, svn.InvalidRevnum, nil, err
			}
			continue
		}
		entry, parseErr := direntFromProperties(path.Base(responsePath(response.Href)), properties)
		if parseErr != nil {
			return nil, svn.InvalidRevnum, nil, parseErr
		}
		entries = append(entries, maskDirent(*entry, fields))
	}
	sortDirents(entries)
	return entries, resolved, props, nil
}
func (session *Session) List(ctx context.Context, name string, revision svn.Revnum, patterns []string, depth svn.Depth, fields svn.DirentFields, handler func(string, *svn.Dirent) error) error {
	if depth == svn.DepthEmpty {
		return nil
	}
	var walk func(string) error
	walk = func(current string) error {
		entries, _, _, err := session.GetDir(ctx, current, revision, svn.DirentAll)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			relative := strings.TrimPrefix(path.Join(strings.TrimPrefix(current, name), entry.Path), "/")
			if matches(patterns, relative) {
				masked := maskDirent(entry, fields)
				masked.Path = relative
				if err := handler(relative, &masked); err != nil {
					return err
				}
			}
			if entry.Kind == svn.NodeDir && (depth == svn.DepthInfinity || depth == svn.DepthUnknown) {
				if err := walk(path.Join(current, entry.Path)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(name)
}
func (session *Session) GetCommitEditor(_ context.Context, revprops svn.Props, lockTokens map[string]string, keepLocks bool, callback func(*ra.CommitInfo) error) (delta.Editor, error) {
	return session.startCommit(revprops, lockTokens, keepLocks, callback)
}
func (session *Session) Lock(ctx context.Context, pathRevisions map[string]svn.Revnum, comment string, steal bool, callback ra.LockCallback) error {
	return session.lock(ctx, pathRevisions, comment, steal, callback)
}
func (session *Session) Unlock(ctx context.Context, pathTokens map[string]string, breakLock bool, callback ra.LockCallback) error {
	return session.unlock(ctx, pathTokens, breakLock, callback)
}
func (*Session) Close() error { return nil }
