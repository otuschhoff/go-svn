package ralocal

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
)

type Session struct {
	repository *repos.Repository
	rootURL    string
	url        string
	base       string
}

func init() { ra.Register("file", open) }

func open(ctx context.Context, parsed *url.URL, _ *ra.Callbacks) (ra.Session, string, error) {
	if parsed.Host != "" && (!strings.EqualFold(parsed.Hostname(), "localhost") || parsed.Port() != "") {
		return nil, "", fmt.Errorf("%w: file URL host %q", svn.ErrRAIllegalURL, parsed.Host)
	}
	decodedPath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return nil, "", fmt.Errorf("%w: invalid escaped file URL path", svn.ErrRAIllegalURL)
	}
	localPath := localFilePath(decodedPath, runtime.GOOS)
	repositoryPath, base, err := findRepository(localPath)
	if err != nil {
		return nil, "", err
	}
	repository, err := repos.Open(ctx, repositoryPath)
	if err != nil {
		return nil, "", err
	}
	rootURL := fileURL(repositoryPath)
	sessionURL := rootURL
	if base != "" {
		sessionURL += "/" + strings.Join(strings.Split(base, string(filepath.Separator)), "/")
	}
	return &Session{repository: repository, rootURL: rootURL, url: sessionURL, base: filepath.ToSlash(base)}, sessionURL, nil
}

func localFilePath(decodedPath, goos string) string {
	if goos == "windows" && len(decodedPath) >= 3 && decodedPath[0] == '/' && decodedPath[2] == ':' && (decodedPath[1] >= 'A' && decodedPath[1] <= 'Z' || decodedPath[1] >= 'a' && decodedPath[1] <= 'z') {
		decodedPath = decodedPath[1:]
	}
	return filepath.Clean(filepath.FromSlash(decodedPath))
}

func findRepository(start string) (string, string, error) {
	absolute, err := filepath.Abs(start)
	if err != nil {
		return "", "", err
	}
	for candidate := absolute; ; candidate = filepath.Dir(candidate) {
		if info, statErr := os.Stat(filepath.Join(candidate, "db", "fs-type")); statErr == nil && !info.IsDir() {
			base, err := filepath.Rel(candidate, absolute)
			if err != nil {
				return "", "", err
			}
			if base == "." {
				base = ""
			}
			return candidate, base, nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", "", fmt.Errorf("%w: %s is not inside a repository", svn.ErrRALocalReposNotFound, start)
		}
	}
}

func fileURL(localPath string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(localPath)}).String()
}

func (session *Session) URL() string { return session.url }

func (session *Session) Reparent(ctx context.Context, rawURL string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	base, ok := relativeURL(session.rootURL, rawURL)
	if !ok {
		return fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, rawURL)
	}
	session.url = strings.TrimSuffix(rawURL, "/")
	session.base = base
	return nil
}

func (session *Session) RepositoryRoot(context.Context) (string, error) { return session.rootURL, nil }
func (session *Session) UUID(ctx context.Context) (string, error) {
	return session.repository.UUID(ctx)
}

func (*Session) HasCapability(_ context.Context, capability ra.Capability) (bool, error) {
	switch capability {
	case ra.CapabilityDepth, ra.CapabilityLogRevprops, ra.CapabilityMergeinfo, ra.CapabilityPartialReplay, ra.CapabilityInheritedProps:
		return true, nil
	default:
		return false, nil
	}
}

func (session *Session) LatestRevision(ctx context.Context) (svn.Revnum, error) {
	return session.repository.Youngest(ctx)
}

func (session *Session) DatedRevision(ctx context.Context, date time.Time) (svn.Revnum, error) {
	return session.repository.DatedRevision(ctx, date)
}

func (session *Session) RevProps(ctx context.Context, revision svn.Revnum) (svn.Props, error) {
	resolved, err := session.repository.ResolveRevision(ctx, revision)
	if err != nil {
		return nil, err
	}
	return session.repository.RevisionProps(ctx, resolved)
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
	resolved, err := session.repository.ResolveRevision(ctx, revision)
	if err != nil {
		return err
	}
	return session.repository.ChangeRevisionProp(ctx, resolved, name, value, oldValue, dontCare)
}

func (session *Session) CheckPath(ctx context.Context, name string, revision svn.Revnum) (svn.NodeKind, error) {
	root, _, err := session.repository.Root(ctx, revision)
	if err != nil {
		return svn.NodeUnknown, err
	}
	return root.CheckPath(ctx, session.join(name))
}

func (session *Session) Stat(ctx context.Context, name string, revision svn.Revnum) (*svn.Dirent, error) {
	root, _, err := session.repository.Root(ctx, revision)
	if err != nil {
		return nil, err
	}
	entry, err := session.repository.Dirent(ctx, root, session.join(name), svn.DirentAll)
	if entry != nil {
		entry.Path = path.Base(clean(name))
	}
	return entry, err
}

func (session *Session) GetFile(ctx context.Context, name string, revision svn.Revnum, destination io.Writer, wantProps bool) (svn.Revnum, svn.Props, error) {
	root, resolved, err := session.repository.Root(ctx, revision)
	if err != nil {
		return svn.InvalidRevnum, nil, err
	}
	fullPath := session.join(name)
	kind, err := root.CheckPath(ctx, fullPath)
	if err != nil {
		return svn.InvalidRevnum, nil, err
	}
	if kind != svn.NodeFile {
		return svn.InvalidRevnum, nil, svn.NewError(svn.ErrFSNotFound, name)
	}
	if destination != nil {
		if err := root.FileContents(ctx, fullPath, destination); err != nil {
			return svn.InvalidRevnum, nil, err
		}
	}
	var props svn.Props
	if wantProps {
		props, err = root.NodeProps(ctx, fullPath)
	}
	return resolved, props, err
}

func (session *Session) GetDir(ctx context.Context, name string, revision svn.Revnum, fields svn.DirentFields) ([]svn.Dirent, svn.Revnum, svn.Props, error) {
	root, resolved, err := session.repository.Root(ctx, revision)
	if err != nil {
		return nil, svn.InvalidRevnum, nil, err
	}
	fullPath := session.join(name)
	kind, err := root.CheckPath(ctx, fullPath)
	if err != nil || kind != svn.NodeDir {
		if err == nil {
			err = svn.NewError(svn.ErrFSNotFound, name)
		}
		return nil, svn.InvalidRevnum, nil, err
	}
	children, err := root.DirEntries(ctx, fullPath)
	if err != nil {
		return nil, svn.InvalidRevnum, nil, err
	}
	entries := make([]svn.Dirent, 0, len(children))
	for _, child := range children {
		entry, err := session.repository.Dirent(ctx, root, path.Join(fullPath, child.Name), fields)
		if err != nil {
			return nil, svn.InvalidRevnum, nil, err
		}
		entry.Path = child.Name
		entries = append(entries, *entry)
	}
	props, err := root.NodeProps(ctx, fullPath)
	return entries, resolved, props, err
}

func (session *Session) List(ctx context.Context, name string, revision svn.Revnum, patterns []string, depth svn.Depth, fields svn.DirentFields, handler func(string, *svn.Dirent) error) error {
	root, _, err := session.repository.Root(ctx, revision)
	if err != nil {
		return err
	}
	start := session.join(name)
	kind, err := root.CheckPath(ctx, start)
	if err != nil {
		return err
	}
	if kind == svn.NodeNone {
		return svn.NewError(svn.ErrFSNotFound, name)
	}
	var walk func(string, string, int) error
	walk = func(fullPath, relative string, level int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, err := session.repository.Dirent(ctx, root, fullPath, fields)
		if err != nil {
			return err
		}
		if relative != "" && matches(patterns, relative) {
			entry.Path = relative
			if err := handler(relative, entry); err != nil {
				return err
			}
		}
		if entry.Kind != svn.NodeDir && fields&svn.DirentKind != 0 || depth == svn.DepthEmpty {
			return nil
		}
		children, err := root.DirEntries(ctx, fullPath)
		if err != nil {
			return nil
		}
		for _, child := range children {
			if depth == svn.DepthFiles && child.Kind == svn.NodeDir {
				continue
			}
			if level > 0 && depth != svn.DepthInfinity && depth != svn.DepthUnknown {
				continue
			}
			childRelative := strings.TrimPrefix(path.Join(relative, child.Name), "./")
			if err := walk(path.Join(fullPath, child.Name), childRelative, level+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(start, "", 0)
}

func (session *Session) Log(ctx context.Context, options ra.LogOptions, handler func(*svn.LogEntry) error) error {
	options.Paths = session.paths(options.Paths)
	return session.repository.Log(ctx, options, handler)
}

func (session *Session) GetLocations(ctx context.Context, name string, peg svn.Revnum, revisions []svn.Revnum) (map[svn.Revnum]string, error) {
	return session.repository.GetLocations(ctx, session.join(name), peg, revisions)
}

func (session *Session) GetLocationSegments(ctx context.Context, name string, peg, start, end svn.Revnum, handler func(ra.LocationSegment) error) error {
	latest, err := session.LatestRevision(ctx)
	if err != nil {
		return err
	}
	start = resolve(start, resolve(peg, latest))
	end = resolve(end, 0)
	return session.repository.GetLocationSegments(ctx, session.join(name), resolve(peg, latest), start, end, handler)
}

func (session *Session) GetFileRevs(ctx context.Context, name string, start, end svn.Revnum, includeMerged bool, handler ra.FileRevHandler) error {
	latest, err := session.LatestRevision(ctx)
	if err != nil {
		return err
	}
	start, end = resolve(start, 0), resolve(end, latest)
	if includeMerged && start > end {
		return fmt.Errorf("%w: reverse merged file revisions", svn.ErrUnsupportedFeature)
	}
	points, err := session.interestingFileRevisions(ctx, session.join(name), start, end, false)
	if err != nil {
		return err
	}
	if includeMerged {
		points, err = session.addMergedFileRevisions(ctx, points, start, end)
		if err != nil {
			return err
		}
	}
	return session.sendFileRevisions(ctx, points, handler)
}

type fileRevisionPoint struct {
	path     string
	revision svn.Revnum
	merged   bool
}

func (session *Session) interestingFileRevisions(ctx context.Context, nodePath string, start, end svn.Revnum, merged bool) ([]fileRevisionPoint, error) {
	high, low := max(start, end), min(start, end)
	root, _, err := session.repository.Root(ctx, high)
	if err != nil {
		return nil, err
	}
	kind, err := root.CheckPath(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	if kind != svn.NodeFile {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotFile, nodePath)
	}
	history, err := root.NodeHistory(ctx, nodePath, true)
	if err != nil {
		return nil, err
	}
	points := make([]fileRevisionPoint, 0, len(history))
	baselineAdded := false
	for _, item := range history {
		if item.Revision > high {
			continue
		}
		if item.Revision < low {
			if baselineAdded {
				break
			}
			baselineAdded = true
		}
		points = append(points, fileRevisionPoint{path: item.Path, revision: item.Revision, merged: merged})
		if item.Revision <= low {
			baselineAdded = true
		}
	}
	sort.SliceStable(points, func(first, second int) bool {
		if start > end {
			return points[first].revision > points[second].revision
		}
		return points[first].revision < points[second].revision
	})
	return points, nil
}

func (session *Session) addMergedFileRevisions(ctx context.Context, mainline []fileRevisionPoint, start, end svn.Revnum) ([]fileRevisionPoint, error) {
	result := append([]fileRevisionPoint(nil), mainline...)
	seen := make(map[string]bool)
	for _, point := range mainline {
		seen[fmt.Sprintf("%s:%d", point.path, point.revision)] = true
	}
	for offset := 0; offset < len(result); offset++ {
		point := result[offset]
		changes, err := session.repository.MergeinfoChanges(ctx, point.revision, []string{point.path})
		if err != nil {
			return nil, err
		}
		for source, ranges := range changes {
			for _, item := range ranges {
				if item.End < start {
					continue
				}
				from := max(start, item.Start)
				to := min(end, item.End)
				if to < from {
					continue
				}
				merged, err := session.interestingFileRevisions(ctx, source, from, to, true)
				if err != nil {
					return nil, err
				}
				for _, candidate := range merged {
					key := fmt.Sprintf("%s:%d", candidate.path, candidate.revision)
					if !seen[key] {
						seen[key] = true
						result = append(result, candidate)
					}
				}
			}
		}
	}
	sort.SliceStable(result, func(first, second int) bool {
		if result[first].revision == result[second].revision {
			return !result[first].merged && result[second].merged
		}
		return result[first].revision < result[second].revision
	})
	return result, nil
}

func (session *Session) sendFileRevisions(ctx context.Context, points []fileRevisionPoint, handler ra.FileRevHandler) error {
	var previousChecksum *svn.Checksum
	previousProps := make(svn.Props)
	var previousFile *os.File
	defer func() { closeTemporaryFile(previousFile) }()
	for _, point := range points {
		root, _, err := session.repository.Root(ctx, point.revision)
		if err != nil {
			return err
		}
		checksum, err := root.FileChecksum(ctx, point.path, svn.ChecksumMD5)
		if err != nil {
			return err
		}
		props, err := root.NodeProps(ctx, point.path)
		if err != nil {
			return err
		}
		textChanged := previousChecksum == nil || !previousChecksum.Equal(*checksum)
		var currentFile *os.File
		var deltaReader delta.WindowReader
		if textChanged {
			currentFile, err = rootToTemporaryFile(ctx, root, point.path)
			if err != nil {
				return err
			}
			if previousFile != nil {
				_, err = previousFile.Seek(0, io.SeekStart)
			}
			if err == nil {
				var source io.Reader
				if previousFile != nil {
					source = previousFile
				}
				deltaReader = delta.NewTxDeltaStream(source, currentFile)
			}
			if err != nil {
				closeTemporaryFile(currentFile)
				return err
			}
		}
		revprops, err := session.repository.RevisionProps(ctx, point.revision)
		if err != nil {
			closeTemporaryFile(currentFile)
			return err
		}
		fileRevision := ra.FileRevision{Path: point.path, Revision: point.revision, RevProps: revprops, PropDiffs: propertyDiff(previousProps, props), Merged: point.merged, Delta: deltaReader}
		if err := handler(ctx, fileRevision); err != nil {
			closeTemporaryFile(currentFile)
			return err
		}
		if textChanged {
			closeTemporaryFile(previousFile)
			previousFile = currentFile
			copy := *checksum
			previousChecksum = &copy
		}
		previousProps = props.Clone()
	}
	return nil
}

func rootToTemporaryFile(ctx context.Context, root fs.Root, nodePath string) (*os.File, error) {
	file, err := os.CreateTemp("", "go-svn-file-rev-*")
	if err != nil {
		return nil, err
	}
	if err := root.FileContents(ctx, nodePath, file); err != nil {
		closeTemporaryFile(file)
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		closeTemporaryFile(file)
		return nil, err
	}
	return file, nil
}

func closeTemporaryFile(file *os.File) {
	if file != nil {
		name := file.Name()
		file.Close()
		os.Remove(name)
	}
}

func propertyDiff(previous, current svn.Props) svn.Props {
	result := make(svn.Props)
	for name, value := range current {
		if !bytes.Equal(previous[name], value) {
			result[name] = append([]byte(nil), value...)
		}
	}
	for name := range previous {
		if _, found := current[name]; !found {
			result[name] = nil
		}
	}
	return result
}

func propertiesEqual(left, right svn.Props) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if !bytes.Equal(value, right[name]) {
			return false
		}
	}
	return true
}

func absRevision(value svn.Revnum) int {
	if value < 0 {
		return int(-value)
	}
	return int(value)
}

func (session *Session) GetMergeinfo(ctx context.Context, paths []string, revision svn.Revnum, inheritance mergeinfo.Inheritance, descendants bool) (map[string]mergeinfo.Mergeinfo, error) {
	return session.repository.GetMergeinfo(ctx, session.paths(paths), revision, inheritance, descendants)
}

func (session *Session) GetInheritedProps(ctx context.Context, name string, revision svn.Revnum) ([]ra.InheritedProps, error) {
	return session.repository.GetInheritedProps(ctx, session.join(name), revision)
}

func (session *Session) GetDeletedRev(ctx context.Context, name string, peg, end svn.Revnum) (svn.Revnum, error) {
	return session.repository.GetDeletedRev(ctx, session.join(name), peg, end)
}

func (session *Session) GetCommitEditor(ctx context.Context, properties svn.Props, lockTokens map[string]string, keepLocks bool, callback func(*ra.CommitInfo) error) (delta.Editor, error) {
	tokens := make(map[string]string, len(lockTokens))
	for name, token := range lockTokens {
		tokens["/"+session.join(name)] = token
	}
	return session.repository.GetCommitEditor(ctx, repos.CommitOptions{BasePath: session.base, RepositoryURL: session.rootURL, Properties: properties, LockTokens: tokens, KeepLocks: keepLocks, Callback: callback})
}

func (session *Session) Lock(ctx context.Context, pathRevisions map[string]svn.Revnum, comment string, steal bool, callback ra.LockCallback) error {
	names := make([]string, 0, len(pathRevisions))
	for name := range pathRevisions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fullPath := session.join(name)
		root, _, itemErr := session.repository.Root(ctx, svn.InvalidRevnum)
		if itemErr == nil {
			var kind svn.NodeKind
			kind, itemErr = root.CheckPath(ctx, fullPath)
			if itemErr == nil && kind != svn.NodeFile {
				itemErr = fmt.Errorf("%w: %s", svn.ErrFSNotFile, name)
			}
			if itemErr == nil && pathRevisions[name].IsValid() {
				var created svn.Revnum
				created, itemErr = root.NodeCreatedRevision(ctx, fullPath)
				if itemErr == nil && created > pathRevisions[name] {
					itemErr = fmt.Errorf("%w: %s", svn.ErrFSOutOfDate, name)
				}
			}
		}
		var lock *svn.Lock
		if itemErr == nil {
			lock, itemErr = session.repository.Lock(ctx, fullPath, newLockToken(), "local", comment, time.Time{}, steal)
		}
		if callback != nil {
			if err := callback(name, lock, itemErr); err != nil {
				return err
			}
		} else if itemErr != nil {
			return itemErr
		}
	}
	return nil
}

func (session *Session) Unlock(ctx context.Context, pathTokens map[string]string, breakLock bool, callback ra.LockCallback) error {
	names := make([]string, 0, len(pathTokens))
	for name := range pathTokens {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		itemErr := session.repository.Unlock(ctx, session.join(name), pathTokens[name], breakLock)
		if callback != nil {
			if err := callback(name, nil, itemErr); err != nil {
				return err
			}
		} else if itemErr != nil {
			return itemErr
		}
	}
	return nil
}

func newLockToken() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("opaquelocktoken:local-%d", time.Now().UnixNano())
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("opaquelocktoken:%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

func (session *Session) GetLock(ctx context.Context, name string) (*svn.Lock, error) {
	return session.repository.GetLock(ctx, session.join(name))
}

func (session *Session) GetLocks(ctx context.Context, name string, depth svn.Depth) (map[string]*svn.Lock, error) {
	return session.repository.GetLocks(ctx, session.join(name), depth)
}

func (*Session) Close() error { return nil }

func (session *Session) join(name string) string { return clean(path.Join(session.base, name)) }

func (session *Session) paths(values []string) []string {
	if values == nil {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = session.join(value)
	}
	return result
}

func clean(value string) string {
	return strings.Trim(strings.TrimPrefix(path.Clean("/"+value), "/"), "/")
}

func resolve(value, fallback svn.Revnum) svn.Revnum {
	if value.IsValid() {
		return value
	}
	return fallback
}

func relativeURL(root, raw string) (string, bool) {
	root = strings.TrimSuffix(root, "/")
	raw = strings.TrimSuffix(raw, "/")
	if raw == root {
		return "", true
	}
	if strings.HasPrefix(raw, root+"/") {
		return clean(strings.TrimPrefix(raw, root+"/")), true
	}
	return "", false
}

func matches(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
		if matched, _ := path.Match(pattern, path.Base(name)); matched {
			return true
		}
	}
	return false
}

func fullDelta(content []byte) delta.WindowReader {
	if len(content) == 0 {
		return delta.Windows()
	}
	return delta.Windows(delta.Window{TargetLength: len(content), Ops: []delta.Op{{Kind: delta.OpNew, Length: len(content)}}, NewData: append([]byte(nil), content...)})
}

func notImplemented(operation string) error {
	return fmt.Errorf("%w: read-only file session does not support %s", svn.ErrRANotImplemented, operation)
}

var _ ra.Session = (*Session)(nil)
