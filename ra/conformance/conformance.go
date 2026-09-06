// Package conformance contains reusable ra.Session read and write test suites.
package conformance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/mergeinfo"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type Fixture struct {
	Open       any
	RootURL    string
	UUID       string
	Latest     svn.Revnum
	FilePath   string
	OldContent string
	Content    string
	Deleted    string
	LatestTime time.Time
	RevProp    string
	RevValue   string
	MergePath  string
	InheritKey string
	InheritVal string
}

func Run(t *testing.T, fixture Fixture) {
	t.Helper()
	t.Run("Identity", func(t *testing.T) {
		session := open(t, fixture)
		defer session.Close()
		ctx := context.Background()
		if root, err := session.RepositoryRoot(ctx); err != nil || root != fixture.RootURL {
			t.Fatalf("root=%q error=%v", root, err)
		}
		if uuid, err := session.UUID(ctx); err != nil || uuid != fixture.UUID {
			t.Fatalf("uuid=%q error=%v", uuid, err)
		}
		if latest, err := session.LatestRevision(ctx); err != nil || latest != fixture.Latest {
			t.Fatalf("latest=%d error=%v", latest, err)
		}
		childURL := fixture.RootURL + "/" + path.Dir(fixture.FilePath)
		if err := session.Reparent(ctx, childURL); err != nil || session.URL() != childURL {
			t.Fatalf("reparent child URL=%q error=%v", session.URL(), err)
		}
		if err := session.Reparent(ctx, fixture.RootURL); err != nil || session.URL() != fixture.RootURL {
			t.Fatalf("reparent root URL=%q error=%v", session.URL(), err)
		}
		if err := session.Reparent(ctx, "invalid://outside/repository"); !errors.Is(err, svn.ErrRAIllegalURL) {
			t.Fatalf("outside reparent error=%v", err)
		}
	})

	t.Run("RevisionMetadata", func(t *testing.T) {
		session := open(t, fixture)
		defer session.Close()
		ctx := context.Background()
		if props, err := session.RevProps(ctx, 0); err != nil || props == nil {
			t.Fatalf("r0 props=%v error=%v", props, err)
		}
		props, err := session.RevProps(ctx, fixture.Latest)
		if err != nil || string(props[fixture.RevProp]) != fixture.RevValue {
			t.Fatalf("latest props=%v error=%v", props, err)
		}
		value, found, err := session.RevProp(ctx, fixture.Latest, fixture.RevProp)
		if err != nil || !found || string(value) != fixture.RevValue {
			t.Fatalf("revprop=%q found=%v error=%v", value, found, err)
		}
		if _, found, err := session.RevProp(ctx, fixture.Latest, "go-svn:missing"); err != nil || found {
			t.Fatalf("missing revprop found=%v error=%v", found, err)
		}
		if !fixture.LatestTime.IsZero() {
			if revision, err := session.DatedRevision(ctx, fixture.LatestTime); err != nil || revision != fixture.Latest {
				t.Fatalf("dated revision=%d error=%v", revision, err)
			}
		}
	})

	t.Run("HistoricalReads", func(t *testing.T) {
		session := open(t, fixture)
		defer session.Close()
		ctx := context.Background()
		var current bytes.Buffer
		revision, props, err := session.GetFile(ctx, fixture.FilePath, fixture.Latest, &current, true)
		if err != nil || revision != fixture.Latest || current.String() != fixture.Content || props == nil {
			t.Fatalf("revision=%d content=%q props=%v error=%v", revision, current.String(), props, err)
		}
		var old bytes.Buffer
		if _, _, err := session.GetFile(ctx, fixture.FilePath, fixture.Latest-1, &old, false); err != nil || old.String() != fixture.OldContent {
			t.Fatalf("old content=%q error=%v", old.String(), err)
		}
		if kind, err := session.CheckPath(ctx, fixture.Deleted, fixture.Latest); err != nil || kind != svn.NodeNone {
			t.Fatalf("deleted kind=%v error=%v", kind, err)
		}
		if kind, err := session.CheckPath(ctx, fixture.FilePath, fixture.Latest); err != nil || kind != svn.NodeFile {
			t.Fatalf("file kind=%v error=%v", kind, err)
		}
		if kind, err := session.CheckPath(ctx, path.Dir(fixture.FilePath), fixture.Latest); err != nil || kind != svn.NodeDir {
			t.Fatalf("directory kind=%v error=%v", kind, err)
		}
		if entry, err := session.Stat(ctx, fixture.FilePath, fixture.Latest); err != nil || entry == nil || entry.Kind != svn.NodeFile {
			t.Fatalf("file stat=%v error=%v", entry, err)
		}
		if entry, err := session.Stat(ctx, "go-svn-missing", fixture.Latest); err != nil || entry != nil {
			t.Fatalf("missing stat=%v error=%v", entry, err)
		}
		if _, _, err := session.GetFile(ctx, "go-svn-missing", fixture.Latest, io.Discard, false); !errors.Is(err, svn.ErrFSNotFound) {
			t.Fatalf("missing file error=%v", err)
		}
		if revision, err := session.GetDeletedRev(ctx, fixture.Deleted, fixture.Latest-1, fixture.Latest); err != nil || revision != fixture.Latest {
			t.Fatalf("deleted revision=%d error=%v", revision, err)
		}
	})

	t.Run("DirectoryListAndLog", func(t *testing.T) {
		session := open(t, fixture)
		defer session.Close()
		ctx := context.Background()
		entries, revision, _, err := session.GetDir(ctx, "", fixture.Latest, svn.DirentAll)
		if err != nil || revision != fixture.Latest || len(entries) == 0 {
			t.Fatalf("entries=%v revision=%d error=%v", entries, revision, err)
		}
		if !sort.SliceIsSorted(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path }) {
			t.Fatalf("directory entries are not sorted: %v", entries)
		}
		for fields := svn.DirentFields(0); fields <= svn.DirentAll; fields++ {
			masked, _, _, err := session.GetDir(ctx, "", fixture.Latest, fields)
			if err != nil || len(masked) != len(entries) {
				t.Fatalf("fields=%#x entries=%v error=%v", fields, masked, err)
			}
			for _, entry := range masked {
				if fields&svn.DirentKind == 0 && entry.Kind != svn.NodeUnknown {
					t.Fatalf("fields=%#x unmasked kind in %#v", fields, entry)
				}
				if fields&svn.DirentSize == 0 && entry.Size != 0 {
					t.Fatalf("fields=%#x unmasked size in %#v", fields, entry)
				}
				if fields&svn.DirentHasProps == 0 && entry.HasProps {
					t.Fatalf("fields=%#x unmasked has-props in %#v", fields, entry)
				}
				if fields&svn.DirentCreatedRev == 0 && entry.CreatedRev != svn.InvalidRevnum {
					t.Fatalf("fields=%#x unmasked revision in %#v", fields, entry)
				}
				if fields&svn.DirentTime == 0 && !entry.Time.IsZero() {
					t.Fatalf("fields=%#x unmasked time in %#v", fields, entry)
				}
				if fields&svn.DirentLastAuthor == 0 && entry.LastAuthor != "" {
					t.Fatalf("fields=%#x unmasked author in %#v", fields, entry)
				}
			}
		}
		listed := 0
		if err := session.List(ctx, "", fixture.Latest, nil, svn.DepthInfinity, svn.DirentAll, func(string, *svn.Dirent) error { listed++; return nil }); err != nil || listed == 0 {
			t.Fatalf("listed=%d error=%v", listed, err)
		}
		matched := 0
		if err := session.List(ctx, path.Dir(fixture.FilePath), fixture.Latest, []string{"*"}, svn.DepthFiles, svn.DirentKind, func(name string, _ *svn.Dirent) error {
			if path.Base(name) == path.Base(fixture.FilePath) {
				matched++
			}
			return nil
		}); err != nil || matched != 1 {
			t.Fatalf("pattern matches=%d error=%v", matched, err)
		}
		for _, depth := range []svn.Depth{svn.DepthEmpty, svn.DepthFiles, svn.DepthImmediates, svn.DepthInfinity} {
			if err := session.List(ctx, "", fixture.Latest, nil, depth, svn.DirentKind, func(name string, _ *svn.Dirent) error {
				relative := strings.Trim(strings.TrimPrefix(name, "/"), "/")
				components := 0
				if relative != "" {
					components = strings.Count(relative, "/") + 1
				}
				if depth == svn.DepthEmpty && components > 0 || depth == svn.DepthFiles && components > 1 || depth == svn.DepthImmediates && components > 1 {
					t.Fatalf("depth=%s returned %q", depth, name)
				}
				return nil
			}); err != nil {
				t.Fatalf("depth=%s error=%v", depth, err)
			}
		}
		logs := 0
		if err := session.Log(ctx, ra.LogOptions{Start: fixture.Latest, End: 0, DiscoverChangedPaths: true}, func(entry *svn.LogEntry) error {
			logs++
			if entry.RevProps == nil {
				t.Fatal("nil revprops")
			}
			return nil
		}); err != nil || logs != int(fixture.Latest)+1 {
			t.Fatalf("logs=%d error=%v", logs, err)
		}
		limited := 0
		if err := session.Log(ctx, ra.LogOptions{Paths: []string{fixture.FilePath}, Start: fixture.Latest, End: 0, Limit: 1, StrictNodeHistory: true, RevProps: []string{fixture.RevProp}}, func(entry *svn.LogEntry) error {
			limited++
			if _, ok := entry.RevProps[fixture.RevProp]; !ok {
				t.Fatalf("requested revprop missing from %v", entry.RevProps)
			}
			return nil
		}); err != nil || limited != 1 {
			t.Fatalf("limited logs=%d error=%v", limited, err)
		}
		var ascending []svn.Revnum
		if err := session.Log(ctx, ra.LogOptions{Start: 0, End: fixture.Latest}, func(entry *svn.LogEntry) error {
			ascending = append(ascending, entry.Revision)
			return nil
		}); err != nil || len(ascending) != int(fixture.Latest)+1 || !sort.SliceIsSorted(ascending, func(i, j int) bool { return ascending[i] < ascending[j] }) {
			t.Fatalf("ascending logs=%v error=%v", ascending, err)
		}
	})

	t.Run("MergeinfoAndInheritedProperties", func(t *testing.T) {
		if fixture.MergePath == "" && fixture.InheritKey == "" {
			t.Skip("fixture has no mergeinfo or inherited properties")
		}
		session := open(t, fixture)
		defer session.Close()
		ctx := context.Background()
		if fixture.MergePath != "" {
			info, err := session.GetMergeinfo(ctx, []string{fixture.MergePath}, fixture.Latest, mergeinfo.InheritanceExplicit, true)
			if err != nil || len(info) == 0 {
				t.Fatalf("mergeinfo=%v error=%v", info, err)
			}
		}
		if fixture.InheritKey != "" {
			inherited, err := session.GetInheritedProps(ctx, fixture.FilePath, fixture.Latest)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, item := range inherited {
				if string(item.Props[fixture.InheritKey]) == fixture.InheritVal {
					found = true
				}
			}
			if !found {
				t.Fatalf("inherited properties=%v", inherited)
			}
		}
	})

	t.Run("FileRevisions", func(t *testing.T) {
		session := open(t, fixture)
		defer session.Close()
		var revisions []svn.Revnum
		err := session.GetFileRevs(context.Background(), fixture.FilePath, 0, fixture.Latest, false, func(_ context.Context, revision ra.FileRevision) error {
			revisions = append(revisions, revision.Revision)
			for revision.Delta != nil {
				if _, err := revision.Delta.NextWindow(); err == io.EOF {
					break
				} else if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil || len(revisions) < 2 {
			t.Fatalf("revisions=%v error=%v", revisions, err)
		}
		var reverse []svn.Revnum
		err = session.GetFileRevs(context.Background(), fixture.FilePath, fixture.Latest, 0, false, func(_ context.Context, revision ra.FileRevision) error {
			reverse = append(reverse, revision.Revision)
			return consumeWindows(revision.Delta)
		})
		if err != nil || len(reverse) != len(revisions) || !sort.SliceIsSorted(reverse, func(i, j int) bool { return reverse[i] > reverse[j] }) {
			t.Fatalf("reverse revisions=%v error=%v", reverse, err)
		}
	})

	t.Run("Locations", func(t *testing.T) {
		session := open(t, fixture)
		defer session.Close()
		ctx := context.Background()
		locations, err := session.GetLocations(ctx, fixture.FilePath, fixture.Latest, []svn.Revnum{fixture.Latest - 1, fixture.Latest})
		if err != nil || len(locations) != 2 {
			t.Fatalf("locations=%v error=%v", locations, err)
		}
		var segments []ra.LocationSegment
		if err := session.GetLocationSegments(ctx, fixture.FilePath, fixture.Latest, fixture.Latest, 0, func(segment ra.LocationSegment) error {
			segments = append(segments, segment)
			return nil
		}); err != nil || len(segments) == 0 {
			t.Fatalf("segments=%v error=%v", segments, err)
		}
	})

	t.Run("UpdateAndReplay", func(t *testing.T) {
		session := open(t, fixture)
		defer session.Close()
		ctx := context.Background()
		updated := delta.NewTreeBuilder()
		reporter, err := session.DoUpdate(ctx, fixture.Latest, "", svn.DepthInfinity, true, false, updated)
		if err != nil {
			t.Fatal(err)
		}
		if err := reporter.SetPath(ctx, "", 0, svn.DepthInfinity, true, ""); err != nil {
			t.Fatal(err)
		}
		if err := reporter.FinishReport(ctx); err != nil {
			t.Fatal(err)
		}
		if got := treeFile(updated.Root(), fixture.FilePath); got != fixture.Content {
			t.Fatalf("updated content=%q", got)
		}
		replayed := delta.NewTreeBuilder()
		replayRevision := fixture.Latest
		if replayRevision > 1 {
			replayRevision = 1
		}
		if err := session.Replay(ctx, replayRevision, 0, true, replayed); err != nil {
			t.Fatal(err)
		}

		operations := []struct {
			name  string
			start func(delta.Editor) (ra.Reporter, error)
		}{
			{"switch", func(editor delta.Editor) (ra.Reporter, error) {
				return session.DoSwitch(ctx, fixture.Latest, "", svn.DepthInfinity, fixture.RootURL, true, false, editor)
			}},
			{"status", func(editor delta.Editor) (ra.Reporter, error) {
				return session.DoStatus(ctx, "", fixture.Latest, svn.DepthInfinity, editor)
			}},
			{"diff", func(editor delta.Editor) (ra.Reporter, error) {
				return session.DoDiff(ctx, fixture.Latest, "", svn.DepthInfinity, false, true, fixture.RootURL, editor)
			}},
		}
		for _, operation := range operations {
			reporter, err := operation.start(discardEditor{})
			if err != nil {
				t.Fatalf("%s start: %v", operation.name, err)
			}
			if err := reporter.SetPath(ctx, "", 0, svn.DepthInfinity, true, ""); err != nil {
				t.Fatalf("%s set path: %v", operation.name, err)
			}
			if err := reporter.FinishReport(ctx); err != nil {
				t.Fatalf("%s finish: %v", operation.name, err)
			}
		}
		begun, finished := 0, 0
		if err := session.ReplayRange(ctx, 1, fixture.Latest, 0, true, func(svn.Revnum, svn.Props) (delta.Editor, error) {
			begun++
			return discardEditor{}, nil
		}, func(svn.Revnum, svn.Props, delta.Editor) error {
			finished++
			return nil
		}); err != nil || begun != int(fixture.Latest) || finished != begun {
			t.Fatalf("replay range begun=%d finished=%d error=%v", begun, finished, err)
		}

		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		err = session.List(cancelled, "", fixture.Latest, nil, svn.DepthInfinity, svn.DirentKind, func(string, *svn.Dirent) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled list error=%v", err)
		}
	})
}

func consumeWindows(windows delta.WindowReader) error {
	for windows != nil {
		if _, err := windows.NextWindow(); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
	}
	return nil
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

func open(t *testing.T, fixture Fixture) ra.Session {
	t.Helper()
	factory := reflect.ValueOf(fixture.Open)
	wantArgument := reflect.TypeOf(t)
	if factory.Kind() != reflect.Func || factory.Type().NumIn() != 1 || factory.Type().In(0) != wantArgument || factory.Type().NumOut() != 1 {
		t.Fatalf("fixture Open must be func(*testing.T) T, got %T", fixture.Open)
	}
	value := factory.Call([]reflect.Value{reflect.ValueOf(t)})[0].Interface()
	session, ok := value.(ra.Session)
	if !ok {
		t.Fatalf("fixture returned %T, want ra.Session", value)
	}
	return session
}

func treeFile(root *delta.TreeNode, name string) string {
	current := root
	for _, part := range bytes.Split([]byte(name), []byte{'/'}) {
		if current == nil {
			return ""
		}
		current = current.Children[string(part)]
	}
	if current == nil {
		return ""
	}
	return string(current.Content)
}
