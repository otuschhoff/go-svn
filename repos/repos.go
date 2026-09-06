package repos

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/fs"
	_ "github.com/otuschhoff/go-svn/fs/fsfs"
	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

type Repository struct {
	path string
	fs   fs.FS
}

func Open(ctx context.Context, repositoryPath string) (*Repository, error) {
	filesystem, err := fs.Open(ctx, repositoryPath)
	if err != nil {
		return nil, err
	}
	return &Repository{path: filesystem.Path(), fs: filesystem}, nil
}

func (repository *Repository) Path() string { return repository.path }

func (repository *Repository) UUID(ctx context.Context) (string, error) {
	return repository.fs.UUID(ctx)
}

func (repository *Repository) Youngest(ctx context.Context) (svn.Revnum, error) {
	return repository.fs.YoungestRevision(ctx)
}

func (repository *Repository) RevisionProps(ctx context.Context, revision svn.Revnum) (svn.Props, error) {
	return repository.fs.RevisionProps(ctx, revision)
}

func (repository *Repository) GetLock(ctx context.Context, nodePath string) (*svn.Lock, error) {
	return repository.fs.GetLock(ctx, nodePath)
}

func (repository *Repository) GetLocks(ctx context.Context, nodePath string, depth svn.Depth) (map[string]*svn.Lock, error) {
	return repository.fs.GetLocks(ctx, nodePath, depth)
}

func (repository *Repository) Root(ctx context.Context, revision svn.Revnum) (fs.Root, svn.Revnum, error) {
	resolved, err := repository.ResolveRevision(ctx, revision)
	if err != nil {
		return nil, svn.InvalidRevnum, err
	}
	root, err := repository.fs.RevisionRoot(ctx, resolved)
	return root, resolved, err
}

func (repository *Repository) ResolveRevision(ctx context.Context, revision svn.Revnum) (svn.Revnum, error) {
	if revision.IsValid() {
		youngest, err := repository.Youngest(ctx)
		if err != nil {
			return svn.InvalidRevnum, err
		}
		if revision > youngest {
			return svn.InvalidRevnum, fmt.Errorf("%w: revision %d", svn.ErrFSNoSuchRevision, revision)
		}
		return revision, nil
	}
	return repository.Youngest(ctx)
}

func (repository *Repository) DatedRevision(ctx context.Context, date time.Time) (svn.Revnum, error) {
	youngest, err := repository.Youngest(ctx)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	low, high := svn.Revnum(0), youngest
	for low <= high {
		middle := low + (high-low)/2
		props, err := repository.RevisionProps(ctx, middle)
		if err != nil {
			return svn.InvalidRevnum, err
		}
		stamp, err := revisionDate(props)
		if err != nil || stamp.After(date) {
			high = middle - 1
		} else {
			low = middle + 1
		}
	}
	if high < 0 {
		return 0, nil
	}
	return high, nil
}

func (repository *Repository) Dirent(ctx context.Context, root fs.Root, nodePath string, fields svn.DirentFields) (*svn.Dirent, error) {
	kind, err := root.CheckPath(ctx, nodePath)
	if err != nil || kind == svn.NodeNone {
		return nil, err
	}
	entry := &svn.Dirent{Path: path.Base(nodePath), Kind: kind, CreatedRev: svn.InvalidRevnum}
	if fields&svn.DirentSize != 0 && kind == svn.NodeFile {
		entry.Size, err = root.FileLength(ctx, nodePath)
	}
	if err == nil && fields&svn.DirentHasProps != 0 {
		var props svn.Props
		props, err = root.NodeProps(ctx, nodePath)
		entry.HasProps = len(props) != 0
	}
	if err == nil && fields&svn.DirentCreatedRev != 0 {
		entry.CreatedRev, err = root.NodeCreatedRevision(ctx, nodePath)
	}
	if err == nil && fields&(svn.DirentTime|svn.DirentLastAuthor) != 0 {
		created := entry.CreatedRev
		if !created.IsValid() {
			created, err = root.NodeCreatedRevision(ctx, nodePath)
		}
		if err == nil {
			var props svn.Props
			props, err = repository.RevisionProps(ctx, created)
			if fields&svn.DirentLastAuthor != 0 {
				entry.LastAuthor = strings.TrimSpace(string(props["svn:author"]))
			}
			if fields&svn.DirentTime != 0 {
				entry.Time, err = revisionDate(props)
			}
		}
	}
	if fields&svn.DirentKind == 0 {
		entry.Kind = svn.NodeUnknown
	}
	return entry, err
}

func (repository *Repository) Log(ctx context.Context, options ra.LogOptions, handler func(*svn.LogEntry) error) error {
	youngest, err := repository.Youngest(ctx)
	if err != nil {
		return err
	}
	start := resolve(options.Start, youngest)
	end := resolve(options.End, youngest)
	step := svn.Revnum(1)
	if start > end {
		step = -1
	}
	pathHistories := make([][]fs.HistoryEntry, 0, len(options.Paths))
	for _, nodePath := range options.Paths {
		history, err := repository.nodeHistory(ctx, nodePath, start, !options.StrictNodeHistory)
		if err != nil {
			return err
		}
		pathHistories = append(pathHistories, history)
	}
	count := 0
	delivered := make(map[svn.Revnum]bool)
	for revision := start; ; revision += step {
		if err := ctx.Err(); err != nil {
			return err
		}
		root, _, err := repository.Root(ctx, revision)
		if err != nil {
			return err
		}
		changes, err := root.PathsChanged(ctx)
		if err != nil {
			return err
		}
		filterPaths := options.Paths
		if len(pathHistories) != 0 {
			filterPaths = filterPaths[:0]
			for _, history := range pathHistories {
				if location, ok := historyLocation(history, revision); ok {
					filterPaths = append(filterPaths, location)
				}
			}
		}
		if !delivered[revision] && (len(options.Paths) == 0 || len(filterPaths) != 0) && changesMatch(changes, filterPaths) {
			entry, err := repository.logEntry(ctx, revision, changes, options)
			if err != nil {
				return err
			}
			var merged []*svn.LogEntry
			if options.IncludeMerged {
				delivered[revision] = true
				merged, err = repository.mergedLogEntries(ctx, revision, filterPaths, step, options, delivered)
				if err != nil {
					return err
				}
				entry.HasChildren = len(merged) != 0
			}
			if err := handler(entry); err != nil {
				return err
			}
			delivered[revision] = true
			for _, child := range merged {
				if err := handler(child); err != nil {
					return err
				}
				delivered[child.Revision] = true
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

func (repository *Repository) logEntry(ctx context.Context, revision svn.Revnum, changes map[string]fs.PathChange, options ra.LogOptions) (*svn.LogEntry, error) {
	props, err := repository.RevisionProps(ctx, revision)
	if err != nil {
		return nil, err
	}
	entry := &svn.LogEntry{Revision: revision, RevProps: filterProps(props, options.RevProps)}
	entry.Author = strings.TrimSpace(string(props["svn:author"]))
	entry.Message = string(props["svn:log"])
	entry.Date, _ = revisionDate(props)
	if options.DiscoverChangedPaths {
		entry.ChangedPaths = svnChanges(changes)
	}
	return entry, nil
}

type mergedRevision struct {
	revision    svn.Revnum
	subtractive bool
	paths       []string
}

func (repository *Repository) mergedLogEntries(ctx context.Context, revision svn.Revnum, targetPaths []string, step svn.Revnum, options ra.LogOptions, delivered map[svn.Revnum]bool) ([]*svn.LogEntry, error) {
	merged, err := repository.mergeinfoChanges(ctx, revision, targetPaths)
	if err != nil {
		return nil, err
	}
	byRevision := make(map[svn.Revnum]*mergedRevision)
	for source, change := range merged {
		for _, mergedRevisionNumber := range mergeinfo.ToRevs(change.added) {
			item := byRevision[mergedRevisionNumber]
			if item == nil {
				item = &mergedRevision{revision: mergedRevisionNumber}
				byRevision[mergedRevisionNumber] = item
			}
			item.paths = append(item.paths, source)
		}
		for _, mergedRevisionNumber := range mergeinfo.ToRevs(change.deleted) {
			item := byRevision[mergedRevisionNumber]
			if item == nil {
				item = &mergedRevision{revision: mergedRevisionNumber, subtractive: true}
				byRevision[mergedRevisionNumber] = item
			}
			item.paths = append(item.paths, source)
		}
	}
	revisions := make([]svn.Revnum, 0, len(byRevision))
	for candidate := range byRevision {
		revisions = append(revisions, candidate)
	}
	sort.Slice(revisions, func(first, second int) bool {
		if step < 0 {
			return revisions[first] > revisions[second]
		}
		return revisions[first] < revisions[second]
	})
	result := make([]*svn.LogEntry, 0, len(revisions))
	for _, candidate := range revisions {
		if delivered[candidate] {
			continue
		}
		root, _, err := repository.Root(ctx, candidate)
		if err != nil {
			return nil, err
		}
		changes, err := root.PathsChanged(ctx)
		if err != nil {
			return nil, err
		}
		item := byRevision[candidate]
		if !changesMatch(changes, item.paths) {
			continue
		}
		entry, err := repository.logEntry(ctx, candidate, changes, options)
		if err != nil {
			return nil, err
		}
		entry.SubtractiveMerge = item.subtractive
		delivered[candidate] = true
		children, err := repository.mergedLogEntries(ctx, candidate, item.paths, step, options, delivered)
		if err != nil {
			return nil, err
		}
		entry.HasChildren = len(children) != 0
		result = append(result, entry)
		result = append(result, children...)
	}
	return result, nil
}

type mergeinfoChange struct {
	added   mergeinfo.Rangelist
	deleted mergeinfo.Rangelist
}

func (repository *Repository) MergeinfoChanges(ctx context.Context, revision svn.Revnum, targetPaths []string) (map[string]mergeinfo.Rangelist, error) {
	changes, err := repository.mergeinfoChanges(ctx, revision, targetPaths)
	if err != nil {
		return nil, err
	}
	result := make(map[string]mergeinfo.Rangelist, len(changes))
	for source, change := range changes {
		result[source] = mergeinfo.MergeRangelists(change.added, change.deleted)
	}
	return result, nil
}

func (repository *Repository) mergeinfoChanges(ctx context.Context, revision svn.Revnum, targetPaths []string) (map[string]mergeinfoChange, error) {
	current, _, err := repository.Root(ctx, revision)
	if err != nil {
		return nil, err
	}
	var previous fs.Root
	if revision > 0 {
		previous, _, err = repository.Root(ctx, revision-1)
		if err != nil {
			return nil, err
		}
	}
	changes, err := current.PathsChanged(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]mergeinfoChange)
	for changedPath := range changes {
		sourcesFor := mergeSourceMapper(changedPath, targetPaths)
		if sourcesFor == nil {
			continue
		}
		before, err := nodeMergeinfo(ctx, previous, changedPath)
		if err != nil {
			return nil, err
		}
		after, err := nodeMergeinfo(ctx, current, changedPath)
		if err != nil {
			return nil, err
		}
		deleted, added := mergeinfo.Diff(before, after, false)
		for source, ranges := range added {
			for _, mapped := range sourcesFor(source) {
				item := result[mapped]
				item.added = mergeinfo.MergeRangelists(item.added, ranges)
				result[mapped] = item
			}
		}
		for source, ranges := range deleted {
			for _, mapped := range sourcesFor(source) {
				item := result[mapped]
				item.deleted = mergeinfo.MergeRangelists(item.deleted, ranges)
				result[mapped] = item
			}
		}
	}
	return result, nil
}

func mergeSourceMapper(changedPath string, targetPaths []string) func(string) []string {
	changedPath = clean(changedPath)
	if len(targetPaths) == 0 {
		return func(source string) []string { return []string{clean(source)} }
	}
	var suffixes []string
	for _, targetPath := range targetPaths {
		targetPath = clean(targetPath)
		switch {
		case targetPath == changedPath || strings.HasPrefix(changedPath, targetPath+"/"):
			suffixes = append(suffixes, "")
		case strings.HasPrefix(targetPath, changedPath+"/"):
			suffixes = append(suffixes, strings.TrimPrefix(targetPath, changedPath+"/"))
		}
	}
	if len(suffixes) == 0 {
		return nil
	}
	return func(source string) []string {
		mapped := make([]string, 0, len(suffixes))
		for _, suffix := range suffixes {
			mapped = append(mapped, path.Join(clean(source), suffix))
		}
		return mapped
	}
}

func nodeMergeinfo(ctx context.Context, root fs.Root, nodePath string) (mergeinfo.Mergeinfo, error) {
	if root == nil {
		return nil, nil
	}
	kind, err := root.CheckPath(ctx, nodePath)
	if err != nil || kind == svn.NodeNone {
		return nil, err
	}
	props, err := root.NodeProps(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	raw, found := props["svn:mergeinfo"]
	if !found {
		return nil, nil
	}
	return mergeinfo.Parse(string(raw))
}

func (repository *Repository) GetDeletedRev(ctx context.Context, nodePath string, peg, end svn.Revnum) (svn.Revnum, error) {
	for revision := peg + 1; revision <= end; revision++ {
		root, _, err := repository.Root(ctx, revision)
		if err != nil {
			return svn.InvalidRevnum, err
		}
		kind, err := root.CheckPath(ctx, nodePath)
		if err != nil {
			return svn.InvalidRevnum, err
		}
		if kind == svn.NodeNone {
			return revision, nil
		}
	}
	return svn.InvalidRevnum, nil
}

func (repository *Repository) GetLocations(ctx context.Context, nodePath string, peg svn.Revnum, revisions []svn.Revnum) (map[svn.Revnum]string, error) {
	history, err := repository.nodeHistory(ctx, nodePath, peg, true)
	if err != nil {
		return nil, err
	}
	result := make(map[svn.Revnum]string)
	for _, revision := range revisions {
		if location, ok := historyLocation(history, revision); ok {
			result[revision] = location
		}
	}
	return result, nil
}

func (repository *Repository) GetLocationSegments(ctx context.Context, nodePath string, peg, start, end svn.Revnum, handler func(ra.LocationSegment) error) error {
	history, err := repository.nodeHistory(ctx, nodePath, peg, true)
	if err != nil {
		return err
	}
	for index, entry := range history {
		rangeEnd := min(entry.Revision, start)
		rangeStart := end
		if index+1 < len(history) {
			rangeStart = max(rangeStart, history[index+1].Revision+1)
		}
		if rangeStart <= rangeEnd {
			if err := handler(ra.LocationSegment{Path: clean(entry.Path), RangeStart: rangeStart, RangeEnd: rangeEnd}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (repository *Repository) nodeHistory(ctx context.Context, nodePath string, revision svn.Revnum, crossCopies bool) ([]fs.HistoryEntry, error) {
	root, resolved, err := repository.Root(ctx, revision)
	if err != nil {
		return nil, err
	}
	history, err := root.NodeHistory(ctx, nodePath, crossCopies)
	if err != nil {
		return nil, err
	}
	if len(history) != 0 && history[0].Revision < resolved {
		history[0].Revision = resolved
	}
	return history, nil
}

func historyLocation(history []fs.HistoryEntry, revision svn.Revnum) (string, bool) {
	for index, entry := range history {
		if revision <= entry.Revision && (index+1 == len(history) || revision > history[index+1].Revision) {
			return entry.Path, true
		}
	}
	return "", false
}

func (repository *Repository) GetInheritedProps(ctx context.Context, nodePath string, revision svn.Revnum) ([]ra.InheritedProps, error) {
	root, _, err := repository.Root(ctx, revision)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(clean(nodePath), "/")
	result := make([]ra.InheritedProps, 0)
	for index := 0; index < len(parts); index++ {
		ancestor := strings.Join(parts[:index], "/")
		props, err := root.NodeProps(ctx, ancestor)
		if err != nil {
			return nil, err
		}
		if len(props) != 0 {
			result = append(result, ra.InheritedProps{Path: ancestor, Props: props})
		}
	}
	return result, nil
}

func (repository *Repository) GetMergeinfo(ctx context.Context, paths []string, revision svn.Revnum, inheritance mergeinfo.Inheritance, descendants bool) (map[string]mergeinfo.Mergeinfo, error) {
	root, _, err := repository.Root(ctx, revision)
	if err != nil {
		return nil, err
	}
	result := make(map[string]mergeinfo.Mergeinfo)
	for _, nodePath := range paths {
		current := clean(nodePath)
		for {
			props, err := root.NodeProps(ctx, current)
			if err != nil {
				return nil, err
			}
			if raw, ok := props["svn:mergeinfo"]; ok {
				result[nodePath], err = mergeinfo.Parse(string(raw))
				if err != nil {
					return nil, err
				}
				break
			}
			if inheritance == mergeinfo.InheritanceExplicit || current == "" {
				break
			}
			current = clean(path.Dir("/" + current))
		}
		if descendants {
			catalog, err := root.Mergeinfo(ctx, clean(nodePath), true)
			if err != nil {
				return nil, err
			}
			for catalogPath, info := range catalog {
				result[catalogPath] = info
			}
		}
	}
	return result, nil
}

func revisionDate(props svn.Props) (time.Time, error) {
	value := strings.TrimSpace(string(props["svn:date"]))
	if value == "" {
		return time.Time{}, nil
	}
	return svn.ParseDate(value)
}

func resolve(value, fallback svn.Revnum) svn.Revnum {
	if value.IsValid() {
		return value
	}
	return fallback
}

func clean(value string) string {
	return strings.Trim(strings.TrimPrefix(path.Clean("/"+value), "/"), "/")
}

func filterProps(properties svn.Props, names []string) svn.Props {
	if names == nil {
		return properties.Clone()
	}
	result := make(svn.Props)
	for _, name := range names {
		if value, ok := properties[name]; ok {
			result[name] = append([]byte(nil), value...)
		}
	}
	return result
}

func changesMatch(changes map[string]fs.PathChange, paths []string) bool {
	if len(paths) == 0 {
		return true
	}
	for changed := range changes {
		changed = clean(changed)
		for _, filter := range paths {
			filter = clean(filter)
			if changed == filter || strings.HasPrefix(changed, filter+"/") {
				return true
			}
		}
	}
	return false
}

func svnChanges(changes map[string]fs.PathChange) []svn.ChangedPath {
	result := make([]svn.ChangedPath, 0, len(changes))
	for _, change := range changes {
		action := svn.LogModified
		switch change.Kind {
		case fs.ChangeAdd:
			action = svn.LogAdded
		case fs.ChangeDelete:
			action = svn.LogDeleted
		case fs.ChangeReplace:
			action = svn.LogReplaced
		}
		result = append(result, svn.ChangedPath{Path: change.Path, Action: action, CopyfromPath: change.CopyFromPath, CopyfromRev: change.CopyFromRev, NodeKind: change.NodeKind, TextModified: tristate(change.TextModified), PropsModified: tristate(change.PropsModified)})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	return result
}

func tristate(value bool) svn.Tristate {
	if value {
		return svn.TristateTrue
	}
	return svn.TristateFalse
}

func ReadFile(ctx context.Context, root fs.Root, nodePath string) ([]byte, error) {
	var data bytes.Buffer
	if err := root.FileContents(ctx, nodePath, &data); err != nil {
		return nil, err
	}
	return data.Bytes(), nil
}

func CopyFile(ctx context.Context, root fs.Root, nodePath string, writer io.Writer) error {
	return root.FileContents(ctx, nodePath, writer)
}
