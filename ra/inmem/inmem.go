// Package inmem provides an in-memory reference implementation of ra.Session.
package inmem

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/mergeinfo"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type Node struct {
	Kind       svn.NodeKind
	Props      svn.Props
	Content    []byte
	Children   map[string]*Node
	CreatedRev svn.Revnum
	Time       time.Time
	Author     string
}

type Revision struct {
	Root    *Node
	Props   svn.Props
	Time    time.Time
	Changes []svn.ChangedPath
}

type Repository struct {
	mu        sync.RWMutex
	rootURL   string
	uuid      string
	revisions []Revision
	locks     map[string]*svn.Lock
}

func NewRepository(rootURL, uuid string) *Repository {
	root := Directory()
	return &Repository{rootURL: strings.TrimSuffix(rootURL, "/"), uuid: uuid, revisions: []Revision{{Root: root, Props: make(svn.Props)}}, locks: make(map[string]*svn.Lock)}
}

func Directory(children ...map[string]*Node) *Node {
	node := &Node{Kind: svn.NodeDir, Props: make(svn.Props), Children: make(map[string]*Node), CreatedRev: svn.InvalidRevnum}
	for _, entries := range children {
		for name, child := range entries {
			node.Children[name] = child
		}
	}
	return node
}

func File(content []byte) *Node {
	return &Node{Kind: svn.NodeFile, Props: make(svn.Props), Content: append([]byte(nil), content...), CreatedRev: svn.InvalidRevnum}
}

func (repository *Repository) AddRevision(revision Revision) svn.Revnum {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	number := svn.Revnum(len(repository.revisions))
	copy := cloneRevision(revision)
	stampMetadata(copy.Root, number, copy.Time, string(copy.Props["svn:author"]))
	repository.revisions = append(repository.revisions, copy)
	return number
}

func (repository *Repository) Open(rawURL string) (ra.Session, error) {
	base, ok := relativeURL(repository.rootURL, rawURL)
	if !ok {
		return nil, fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, rawURL)
	}
	return &Session{repository: repository, url: strings.TrimSuffix(rawURL, "/"), base: base}, nil
}

type Session struct {
	repository *Repository
	url        string
	base       string
}

func (session *Session) URL() string { return session.url }
func (session *Session) Reparent(_ context.Context, rawURL string) error {
	base, ok := relativeURL(session.repository.rootURL, rawURL)
	if !ok {
		return fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, rawURL)
	}
	session.url, session.base = strings.TrimSuffix(rawURL, "/"), base
	return nil
}
func (session *Session) RepositoryRoot(context.Context) (string, error) {
	return session.repository.rootURL, nil
}
func (session *Session) UUID(context.Context) (string, error)               { return session.repository.uuid, nil }
func (*Session) HasCapability(context.Context, ra.Capability) (bool, error) { return true, nil }

func (session *Session) LatestRevision(context.Context) (svn.Revnum, error) {
	session.repository.mu.RLock()
	defer session.repository.mu.RUnlock()
	return svn.Revnum(len(session.repository.revisions) - 1), nil
}

func (session *Session) DatedRevision(_ context.Context, date time.Time) (svn.Revnum, error) {
	session.repository.mu.RLock()
	defer session.repository.mu.RUnlock()
	result := svn.Revnum(0)
	for index, revision := range session.repository.revisions {
		if revision.Time.After(date) {
			break
		}
		result = svn.Revnum(index)
	}
	return result, nil
}

func (session *Session) RevProps(_ context.Context, revision svn.Revnum) (svn.Props, error) {
	value, err := session.revision(revision)
	if err != nil {
		return nil, err
	}
	return value.Props.Clone(), nil
}
func (session *Session) RevProp(ctx context.Context, revision svn.Revnum, name string) ([]byte, bool, error) {
	props, err := session.RevProps(ctx, revision)
	if err != nil {
		return nil, false, err
	}
	value, ok := props[name]
	return value, ok, nil
}
func (*Session) ChangeRevProp(context.Context, svn.Revnum, string, []byte, []byte, bool) error {
	return notImplemented("change revision property")
}

func (session *Session) CheckPath(_ context.Context, name string, revision svn.Revnum) (svn.NodeKind, error) {
	node, _, err := session.node(name, revision)
	if err != nil {
		return svn.NodeUnknown, err
	}
	if node == nil {
		return svn.NodeNone, nil
	}
	return node.Kind, nil
}
func (session *Session) Stat(_ context.Context, name string, revision svn.Revnum) (*svn.Dirent, error) {
	node, _, err := session.node(name, revision)
	if err != nil || node == nil {
		return nil, err
	}
	return (&[]svn.Dirent{nodeDirent(path.Base(name), node)}[0]), nil
}
func (session *Session) GetFile(_ context.Context, name string, revision svn.Revnum, destination io.Writer, wantProps bool) (svn.Revnum, svn.Props, error) {
	node, resolved, err := session.node(name, revision)
	if err != nil {
		return svn.InvalidRevnum, nil, err
	}
	if node == nil || node.Kind != svn.NodeFile {
		return svn.InvalidRevnum, nil, fmt.Errorf("%w: %s", svn.ErrFSNotFound, name)
	}
	if destination != nil {
		if _, err := destination.Write(node.Content); err != nil {
			return svn.InvalidRevnum, nil, err
		}
	}
	var props svn.Props
	if wantProps {
		props = node.Props.Clone()
	}
	return resolved, props, nil
}
func (session *Session) GetDir(_ context.Context, name string, revision svn.Revnum, fields svn.DirentFields) ([]svn.Dirent, svn.Revnum, svn.Props, error) {
	node, resolved, err := session.node(name, revision)
	if err != nil {
		return nil, svn.InvalidRevnum, nil, err
	}
	if node == nil || node.Kind != svn.NodeDir {
		return nil, svn.InvalidRevnum, nil, fmt.Errorf("%w: %s", svn.ErrFSNotFound, name)
	}
	names := sortedChildren(node)
	entries := make([]svn.Dirent, 0, len(names))
	for _, childName := range names {
		entries = append(entries, maskDirent(nodeDirent(childName, node.Children[childName]), fields))
	}
	return entries, resolved, node.Props.Clone(), nil
}

func (session *Session) List(ctx context.Context, name string, revision svn.Revnum, patterns []string, depth svn.Depth, fields svn.DirentFields, handler func(string, *svn.Dirent) error) error {
	node, _, err := session.node(name, revision)
	if err != nil {
		return err
	}
	if node == nil {
		return fmt.Errorf("%w: %s", svn.ErrFSNotFound, name)
	}
	var walk func(*Node, string, int) error
	walk = func(current *Node, relative string, level int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if relative != "" && matches(patterns, relative) {
			dirent := maskDirent(nodeDirent(relative, current), fields)
			if err := handler(relative, &dirent); err != nil {
				return err
			}
		}
		if current.Kind != svn.NodeDir || depth == svn.DepthEmpty {
			return nil
		}
		for _, childName := range sortedChildren(current) {
			child := current.Children[childName]
			childPath := strings.TrimPrefix(path.Join(relative, childName), "./")
			if level == 0 || depth == svn.DepthInfinity || depth == svn.DepthUnknown {
				if depth == svn.DepthFiles && child.Kind == svn.NodeDir {
					continue
				}
				if err := walk(child, childPath, level+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(node, "", 0)
}

func (session *Session) Log(ctx context.Context, options ra.LogOptions, handler func(*svn.LogEntry) error) error {
	latest, _ := session.LatestRevision(ctx)
	start, end := resolveRevision(options.Start, latest), resolveRevision(options.End, latest)
	step := svn.Revnum(1)
	if start > end {
		step = -1
	}
	count := 0
	for revision := start; ; revision += step {
		value, err := session.revision(revision)
		if err != nil {
			return err
		}
		if logMatches(value.Changes, options.Paths) {
			entry := &svn.LogEntry{Revision: revision, RevProps: filterProps(value.Props, options.RevProps)}
			entry.Author, entry.Message = string(value.Props["svn:author"]), string(value.Props["svn:log"])
			entry.Date = value.Time
			if options.DiscoverChangedPaths {
				entry.ChangedPaths = append([]svn.ChangedPath(nil), value.Changes...)
			}
			if err := handler(entry); err != nil {
				return err
			}
			count++
			if options.Limit > 0 && count >= options.Limit {
				return nil
			}
		}
		if revision == end {
			return nil
		}
	}
}

func (session *Session) GetLocations(_ context.Context, name string, _ svn.Revnum, revisions []svn.Revnum) (map[svn.Revnum]string, error) {
	result := make(map[svn.Revnum]string)
	for _, revision := range revisions {
		node, _, err := session.node(name, revision)
		if err != nil {
			return nil, err
		}
		if node != nil {
			result[revision] = "/" + join(session.base, name)
		}
	}
	return result, nil
}
func (session *Session) GetLocationSegments(ctx context.Context, name string, peg, start, end svn.Revnum, handler func(ra.LocationSegment) error) error {
	latest, _ := session.LatestRevision(ctx)
	start = resolveRevision(start, resolveRevision(peg, latest))
	end = resolveRevision(end, 0)
	var active *ra.LocationSegment
	for revision := start; revision >= end; revision-- {
		node, _, err := session.node(name, revision)
		if err != nil {
			return err
		}
		if node != nil {
			if active == nil {
				active = &ra.LocationSegment{Path: join(session.base, name), RangeStart: revision, RangeEnd: revision}
			} else {
				active.RangeStart = revision
			}
		} else if active != nil {
			if err := handler(*active); err != nil {
				return err
			}
			active = nil
		}
		if revision == 0 {
			break
		}
	}
	if active != nil {
		return handler(*active)
	}
	return nil
}
func (session *Session) GetFileRevs(ctx context.Context, name string, start, end svn.Revnum, _ bool, handler ra.FileRevHandler) error {
	latest, _ := session.LatestRevision(ctx)
	start, end = resolveRevision(start, 0), resolveRevision(end, latest)
	step := svn.Revnum(1)
	if start > end {
		step = -1
	}
	var previous []byte
	for revision := start; ; revision += step {
		node, _, err := session.node(name, revision)
		if err != nil {
			return err
		}
		if node != nil && node.Kind == svn.NodeFile && !bytes.Equal(previous, node.Content) {
			value, _ := session.revision(revision)
			fileRevision := ra.FileRevision{Path: join(session.base, name), Revision: revision, RevProps: value.Props.Clone(), PropDiffs: node.Props.Clone(), Delta: fullDelta(node.Content)}
			if err := handler(ctx, fileRevision); err != nil {
				return err
			}
			previous = append(previous[:0], node.Content...)
		}
		if revision == end {
			return nil
		}
	}
}

func (session *Session) GetMergeinfo(_ context.Context, paths []string, revision svn.Revnum, inheritance mergeinfo.Inheritance, descendants bool) (map[string]mergeinfo.Mergeinfo, error) {
	result := make(map[string]mergeinfo.Mergeinfo)
	for _, name := range paths {
		node, _, err := session.node(name, revision)
		if err != nil {
			return nil, err
		}
		for current := clean(name); node != nil; current = path.Dir(current) {
			if raw, ok := node.Props["svn:mergeinfo"]; ok {
				parsed, err := mergeinfo.Parse(string(raw))
				if err != nil {
					return nil, err
				}
				result[name] = parsed
				break
			}
			if inheritance == mergeinfo.InheritanceExplicit || current == "." || current == "" {
				break
			}
			node, _, _ = session.node(path.Dir(current), revision)
		}
		if descendants {
			collectMergeinfo(result, name, node)
		}
	}
	return result, nil
}
func (session *Session) GetInheritedProps(_ context.Context, name string, revision svn.Revnum) ([]ra.InheritedProps, error) {
	parts := strings.Split(clean(name), "/")
	result := make([]ra.InheritedProps, 0)
	for index := 0; index < len(parts); index++ {
		ancestor := strings.Join(parts[:index], "/")
		node, _, err := session.node(ancestor, revision)
		if err != nil {
			return nil, err
		}
		if node != nil && len(node.Props) > 0 {
			result = append(result, ra.InheritedProps{Path: ancestor, Props: node.Props.Clone()})
		}
	}
	return result, nil
}
func (session *Session) GetDeletedRev(_ context.Context, name string, peg, end svn.Revnum) (svn.Revnum, error) {
	for revision := peg + 1; revision <= end; revision++ {
		node, _, err := session.node(name, revision)
		if err != nil {
			return svn.InvalidRevnum, err
		}
		if node == nil {
			return revision, nil
		}
	}
	return svn.InvalidRevnum, nil
}

func (session *Session) DoUpdate(_ context.Context, revision svn.Revnum, target string, _ svn.Depth, _ bool, _ bool, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(revision, target, editor)
}
func (session *Session) DoSwitch(_ context.Context, revision svn.Revnum, target string, _ svn.Depth, _ string, _ bool, _ bool, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(revision, target, editor)
}
func (session *Session) DoStatus(_ context.Context, target string, revision svn.Revnum, _ svn.Depth, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(revision, target, editor)
}
func (session *Session) DoDiff(_ context.Context, revision svn.Revnum, target string, _ svn.Depth, _ bool, _ bool, _ string, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(revision, target, editor)
}

func (*Session) GetCommitEditor(context.Context, svn.Props, map[string]string, bool, func(*ra.CommitInfo) error) (delta.Editor, error) {
	return nil, notImplemented("commit")
}
func (*Session) Lock(context.Context, map[string]svn.Revnum, string, bool, ra.LockCallback) error {
	return notImplemented("lock")
}
func (*Session) Unlock(context.Context, map[string]string, bool, ra.LockCallback) error {
	return notImplemented("unlock")
}
func (session *Session) GetLock(_ context.Context, name string) (*svn.Lock, error) {
	session.repository.mu.RLock()
	defer session.repository.mu.RUnlock()
	return cloneLock(session.repository.locks["/"+join(session.base, name)]), nil
}
func (session *Session) GetLocks(_ context.Context, name string, depth svn.Depth) (map[string]*svn.Lock, error) {
	session.repository.mu.RLock()
	defer session.repository.mu.RUnlock()
	prefix := "/" + join(session.base, name)
	result := make(map[string]*svn.Lock)
	for lockPath, lock := range session.repository.locks {
		relative := strings.TrimPrefix(strings.TrimPrefix(lockPath, prefix), "/")
		if lockPath == prefix || depth == svn.DepthInfinity || (depth >= svn.DepthFiles && !strings.Contains(relative, "/")) {
			result[lockPath] = cloneLock(lock)
		}
	}
	return result, nil
}
func (session *Session) Replay(ctx context.Context, revision, _ svn.Revnum, _ bool, editor delta.Editor) error {
	current, err := session.revision(revision)
	if err != nil {
		return err
	}
	previous := Revision{Root: Directory()}
	if revision > 0 {
		previous, _ = session.revision(revision - 1)
	}
	if err := editor.SetTargetRevision(ctx, revision); err != nil {
		return err
	}
	return driveDiff(ctx, editor, previous.Root, current.Root, revision)
}
func (session *Session) ReplayRange(ctx context.Context, start, end, low svn.Revnum, send bool, begin func(svn.Revnum, svn.Props) (delta.Editor, error), finish func(svn.Revnum, svn.Props, delta.Editor) error) error {
	for revision := start; revision <= end; revision++ {
		value, err := session.revision(revision)
		if err != nil {
			return err
		}
		editor, err := begin(revision, value.Props.Clone())
		if err != nil {
			return err
		}
		if err := session.Replay(ctx, revision, low, send, editor); err != nil {
			return err
		}
		if err := finish(revision, value.Props.Clone(), editor); err != nil {
			return err
		}
	}
	return nil
}
func (*Session) Close() error { return nil }

func (session *Session) revision(number svn.Revnum) (Revision, error) {
	session.repository.mu.RLock()
	defer session.repository.mu.RUnlock()
	if !number.IsValid() {
		number = svn.Revnum(len(session.repository.revisions) - 1)
	}
	if number < 0 || int64(number) >= int64(len(session.repository.revisions)) {
		return Revision{}, fmt.Errorf("%w: revision %d", svn.ErrFSNoSuchRevision, number)
	}
	return session.repository.revisions[number], nil
}
func (session *Session) node(name string, revision svn.Revnum) (*Node, svn.Revnum, error) {
	value, err := session.revision(revision)
	if err != nil {
		return nil, svn.InvalidRevnum, err
	}
	resolved := revision
	if !resolved.IsValid() {
		resolved = svn.Revnum(len(session.repository.revisions) - 1)
	}
	return findNode(value.Root, join(session.base, name)), resolved, nil
}

type report struct {
	session  *Session
	revision svn.Revnum
	target   string
	editor   delta.Editor
	entries  map[string]reportEntry
	done     bool
}

type reportEntry struct {
	revision       svn.Revnum
	source         string
	empty, deleted bool
}

func (session *Session) newReporter(revision svn.Revnum, target string, editor delta.Editor) (ra.Reporter, error) {
	if editor == nil {
		return nil, fmt.Errorf("%w: nil editor", svn.ErrIncorrectParams)
	}
	return &report{session: session, revision: revision, target: target, editor: editor, entries: make(map[string]reportEntry)}, nil
}
func (report *report) SetPath(_ context.Context, name string, revision svn.Revnum, _ svn.Depth, startEmpty bool, _ string) error {
	report.entries[clean(name)] = reportEntry{revision: revision, source: join(join(report.session.base, report.target), name), empty: startEmpty}
	return nil
}
func (report *report) LinkPath(_ context.Context, name, rawURL string, revision svn.Revnum, _ svn.Depth, startEmpty bool, _ string) error {
	source, ok := relativeURL(report.session.repository.rootURL, rawURL)
	if !ok {
		return fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, rawURL)
	}
	report.entries[clean(name)] = reportEntry{revision: revision, source: source, empty: startEmpty}
	return nil
}
func (report *report) DeletePath(_ context.Context, name string) error {
	report.entries[clean(name)] = reportEntry{deleted: true}
	return nil
}
func (report *report) FinishReport(ctx context.Context) error {
	if report.done {
		return fmt.Errorf("%w: report complete", svn.ErrIncorrectParams)
	}
	report.done = true
	target, _, err := report.session.node(report.target, report.revision)
	if err != nil {
		return err
	}
	base := Directory()
	if root, ok := report.entries[""]; ok && !root.deleted && !root.empty {
		value, revisionErr := report.session.revision(root.revision)
		if revisionErr != nil {
			return revisionErr
		}
		base = cloneNode(findNode(value.Root, root.source))
		if base == nil {
			base = Directory()
		}
	}
	names := make([]string, 0, len(report.entries))
	for name := range report.entries {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.Count(names[i], "/") < strings.Count(names[j], "/") })
	for _, name := range names {
		entry := report.entries[name]
		if entry.deleted {
			removeNode(base, name)
			continue
		}
		value, revisionErr := report.session.revision(entry.revision)
		if revisionErr != nil {
			return revisionErr
		}
		node := cloneNode(findNode(value.Root, entry.source))
		if node == nil {
			removeNode(base, name)
			continue
		}
		if entry.empty {
			if node.Kind == svn.NodeDir {
				node = Directory()
			} else {
				node = File(nil)
			}
		}
		setNode(base, name, node)
	}
	return driveDiff(ctx, report.editor, base, target, report.revision)
}
func (report *report) AbortReport(context.Context) error {
	if report.done {
		return nil
	}
	report.done = true
	return nil
}

func driveDiff(ctx context.Context, editor delta.Editor, old, current *Node, revision svn.Revnum) error {
	root, err := editor.OpenRoot(ctx, revision)
	if err != nil {
		return err
	}
	if err := changeProps(ctx, root.ChangeProp, propsOf(old), propsOf(current)); err != nil {
		return err
	}
	if err := diffDir(ctx, root, old, current, "", revision); err != nil {
		_ = editor.AbortEdit(ctx)
		return err
	}
	if err := root.Close(ctx); err != nil {
		return err
	}
	return editor.CloseEdit(ctx)
}
func diffDir(ctx context.Context, output delta.DirEditor, old, current *Node, prefix string, revision svn.Revnum) error {
	if old == nil {
		old = Directory()
	}
	if current == nil {
		current = Directory()
	}
	for _, name := range sortedChildren(old) {
		if current.Children[name] == nil {
			if err := output.DeleteEntry(ctx, path.Join(prefix, name), revision-1); err != nil {
				return err
			}
		}
	}
	for _, name := range sortedChildren(current) {
		next, before := current.Children[name], old.Children[name]
		childPath := path.Join(prefix, name)
		if before != nil && before.Kind != next.Kind {
			if err := output.DeleteEntry(ctx, childPath, revision-1); err != nil {
				return err
			}
			before = nil
		}
		if next.Kind == svn.NodeDir {
			var child delta.DirEditor
			var err error
			if before == nil {
				child, err = output.AddDirectory(ctx, childPath, nil)
			} else {
				child, err = output.OpenDirectory(ctx, childPath, revision-1)
			}
			if err != nil {
				return err
			}
			if err := changeProps(ctx, child.ChangeProp, propsOf(before), next.Props); err != nil {
				return err
			}
			if err := diffDir(ctx, child, before, next, childPath, revision); err != nil {
				return err
			}
			if err := child.Close(ctx); err != nil {
				return err
			}
		} else {
			var file delta.FileEditor
			var err error
			if before == nil {
				file, err = output.AddFile(ctx, childPath, nil)
			} else {
				file, err = output.OpenFile(ctx, childPath, revision-1)
			}
			if err != nil {
				return err
			}
			if err := changeProps(ctx, file.ChangeProp, propsOf(before), next.Props); err != nil {
				return err
			}
			if before == nil || !bytes.Equal(before.Content, next.Content) {
				handler, err := file.ApplyTextDelta(ctx, checksumOf(before))
				if err != nil {
					return err
				}
				windows := fullDelta(next.Content)
				for {
					window, err := windows.NextWindow()
					if err == io.EOF {
						break
					}
					if err != nil {
						return err
					}
					if err := handler.Window(window); err != nil {
						return err
					}
				}
				if err := handler.Close(); err != nil {
					return err
				}
			}
			checksum := svn.Sum(svn.ChecksumMD5, next.Content)
			if err := file.Close(ctx, &checksum); err != nil {
				return err
			}
		}
	}
	return nil
}

func changeProps(ctx context.Context, change func(context.Context, string, []byte) error, old, current svn.Props) error {
	names := make(map[string]bool)
	for name := range old {
		names[name] = true
	}
	for name := range current {
		names[name] = true
	}
	for name := range names {
		if !bytes.Equal(old[name], current[name]) {
			if err := change(ctx, name, current[name]); err != nil {
				return err
			}
		}
	}
	return nil
}
func fullDelta(content []byte) delta.WindowReader {
	if len(content) == 0 {
		return delta.Windows()
	}
	return delta.Windows(delta.Window{TargetLength: len(content), Ops: []delta.Op{{Kind: delta.OpNew, Length: len(content)}}, NewData: append([]byte(nil), content...)})
}
func checksumOf(node *Node) *svn.Checksum {
	if node == nil {
		return nil
	}
	checksum := svn.Sum(svn.ChecksumMD5, node.Content)
	return &checksum
}
func propsOf(node *Node) svn.Props {
	if node == nil {
		return nil
	}
	return node.Props
}
func findNode(root *Node, name string) *Node {
	if root == nil {
		return nil
	}
	name = clean(name)
	if name == "" {
		return root
	}
	current := root
	for _, part := range strings.Split(name, "/") {
		if current == nil || current.Kind != svn.NodeDir {
			return nil
		}
		current = current.Children[part]
	}
	return current
}
func setNode(root *Node, name string, node *Node) {
	parent, base := parentNode(root, name)
	if parent != nil {
		parent.Children[base] = node
	}
}
func removeNode(root *Node, name string) {
	parent, base := parentNode(root, name)
	if parent != nil {
		delete(parent.Children, base)
	}
}
func parentNode(root *Node, name string) (*Node, string) {
	parentName, base := path.Split(clean(name))
	return findNode(root, strings.TrimSuffix(parentName, "/")), base
}
func clean(name string) string {
	return strings.Trim(strings.TrimPrefix(path.Clean("/"+name), "/"), "/")
}
func join(first, second string) string { return clean(path.Join(first, second)) }
func sortedChildren(node *Node) []string {
	names := make([]string, 0)
	if node != nil {
		for name := range node.Children {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
func nodeDirent(name string, node *Node) svn.Dirent {
	size := int64(0)
	if node.Kind == svn.NodeFile {
		size = int64(len(node.Content))
	}
	return svn.Dirent{Path: name, Kind: node.Kind, Size: size, HasProps: len(node.Props) > 0, CreatedRev: node.CreatedRev, Time: node.Time, LastAuthor: node.Author}
}
func maskDirent(value svn.Dirent, fields svn.DirentFields) svn.Dirent {
	if fields&svn.DirentKind == 0 {
		value.Kind = svn.NodeUnknown
	}
	if fields&svn.DirentSize == 0 {
		value.Size = 0
	}
	if fields&svn.DirentHasProps == 0 {
		value.HasProps = false
	}
	if fields&svn.DirentCreatedRev == 0 {
		value.CreatedRev = svn.InvalidRevnum
	}
	if fields&svn.DirentTime == 0 {
		value.Time = time.Time{}
	}
	if fields&svn.DirentLastAuthor == 0 {
		value.LastAuthor = ""
	}
	return value
}
func matches(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
		if ok, _ := path.Match(pattern, path.Base(name)); ok {
			return true
		}
	}
	return false
}
func resolveRevision(value, fallback svn.Revnum) svn.Revnum {
	if value.IsValid() {
		return value
	}
	return fallback
}
func filterProps(props svn.Props, names []string) svn.Props {
	if names == nil {
		return props.Clone()
	}
	result := make(svn.Props)
	for _, name := range names {
		if value, ok := props[name]; ok {
			result[name] = append([]byte(nil), value...)
		}
	}
	return result
}
func logMatches(changes []svn.ChangedPath, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for _, change := range changes {
		for _, filter := range paths {
			filter = clean(filter)
			changed := clean(change.Path)
			if changed == filter || strings.HasPrefix(changed, filter+"/") || strings.HasPrefix(filter, changed+"/") {
				return true
			}
		}
	}
	return false
}
func collectMergeinfo(result map[string]mergeinfo.Mergeinfo, prefix string, node *Node) {
	if node == nil {
		return
	}
	if raw, ok := node.Props["svn:mergeinfo"]; ok {
		if parsed, err := mergeinfo.Parse(string(raw)); err == nil {
			result[prefix] = parsed
		}
	}
	for name, child := range node.Children {
		collectMergeinfo(result, path.Join(prefix, name), child)
	}
}
func relativeURL(root, raw string) (string, bool) {
	root = strings.TrimSuffix(root, "/")
	raw = strings.TrimSuffix(raw, "/")
	if raw == root {
		return "", true
	}
	if strings.HasPrefix(raw, root+"/") {
		return strings.TrimPrefix(raw, root+"/"), true
	}
	return "", false
}
func cloneRevision(value Revision) Revision {
	return Revision{Root: cloneNode(value.Root), Props: value.Props.Clone(), Time: value.Time, Changes: append([]svn.ChangedPath(nil), value.Changes...)}
}
func cloneNode(node *Node) *Node {
	if node == nil {
		return nil
	}
	copy := &Node{Kind: node.Kind, Props: node.Props.Clone(), Content: append([]byte(nil), node.Content...), CreatedRev: node.CreatedRev, Time: node.Time, Author: node.Author}
	if node.Children != nil {
		copy.Children = make(map[string]*Node, len(node.Children))
		for name, child := range node.Children {
			copy.Children[name] = cloneNode(child)
		}
	}
	return copy
}
func stampMetadata(node *Node, revision svn.Revnum, date time.Time, author string) {
	if node == nil {
		return
	}
	if !node.CreatedRev.IsValid() {
		node.CreatedRev = revision
	}
	if node.Time.IsZero() {
		node.Time = date
	}
	if node.Author == "" {
		node.Author = author
	}
	for _, child := range node.Children {
		stampMetadata(child, revision, date, author)
	}
}
func cloneLock(lock *svn.Lock) *svn.Lock {
	if lock == nil {
		return nil
	}
	copy := *lock
	return &copy
}
func notImplemented(operation string) error {
	return fmt.Errorf("%w: in-memory %s", svn.ErrRANotImplemented, operation)
}
