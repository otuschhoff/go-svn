//go:build integration

package rasvn_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/internal/testutil"
	"github.com/otuschhoff/go-svn/internal/testutil/servers"
	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/ra/conformance"
	_ "github.com/otuschhoff/go-svn/rasvn"
	"github.com/otuschhoff/go-svn/svn"
)

func TestReadSessionAgainstSvnserve(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	repository := filepath.Join(t.TempDir(), "repo")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	dump, err := os.Open("../testdata/repos/basic.dump")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(svnadmin, "load", "--quiet", repository)
	command.Stdin = dump
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("svnadmin load: %v:\n%s", err, output)
	}
	if err := dump.Close(); err != nil {
		t.Fatal(err)
	}
	server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, corrected, err := ra.Open(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if corrected != server.URL {
		t.Fatalf("corrected URL = %q, want %q", corrected, server.URL)
	}
	revision, err := session.LatestRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 4 {
		t.Fatalf("latest revision = %d, want 4", revision)
	}
	datedRevision, err := session.DatedRevision(ctx, time.Date(2020, 1, 4, 12, 0, 0, 123456789, time.UTC))
	if err != nil || datedRevision != 3 {
		t.Fatalf("dated revision = %d, error=%v", datedRevision, err)
	}
	kind, err := session.CheckPath(ctx, "", svn.InvalidRevnum)
	if err != nil {
		t.Fatal(err)
	}
	if kind != svn.NodeDir {
		t.Fatalf("root kind = %v", kind)
	}
	dirent, err := session.Stat(ctx, "", svn.InvalidRevnum)
	if err != nil {
		t.Fatal(err)
	}
	if dirent == nil || dirent.Kind != svn.NodeDir {
		t.Fatalf("root dirent = %#v", dirent)
	}
	lock, err := session.GetLock(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if lock != nil {
		t.Fatalf("root lock = %#v", lock)
	}
	var contents bytes.Buffer
	fileRevision, props, err := session.GetFile(ctx, "trunk/README.txt", svn.InvalidRevnum, &contents, true)
	if err != nil {
		t.Fatal(err)
	}
	if fileRevision != 4 || contents.String() != "go-svn fixture\nsecond line\n" || string(props["svn:eol-style"]) != "LF" {
		t.Fatalf("file revision=%d contents=%q props=%v", fileRevision, contents.String(), props)
	}
	entries, dirRevision, _, err := session.GetDir(ctx, "trunk", svn.InvalidRevnum, svn.DirentAll)
	if err != nil {
		t.Fatal(err)
	}
	if dirRevision != 4 || len(entries) != 1 || entries[0].Path != "README.txt" || entries[0].Kind != svn.NodeFile {
		t.Fatalf("directory revision=%d entries=%#v", dirRevision, entries)
	}
	revisionProps, err := session.RevProps(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(revisionProps["svn:log"]) != "delete executable" {
		t.Fatalf("revision properties = %v", revisionProps)
	}
	author, exists, err := session.RevProp(ctx, 4, "svn:author")
	if err != nil || !exists || string(author) != "fixture\n" {
		t.Fatalf("author=%q exists=%v error=%v", author, exists, err)
	}
	deletedRevision, err := session.GetDeletedRev(ctx, "trunk/run.sh", 3, 4)
	if err != nil || deletedRevision != 4 {
		t.Fatalf("deleted revision=%d error=%v", deletedRevision, err)
	}
	locks, err := session.GetLocks(ctx, "", svn.DepthInfinity)
	if err != nil || len(locks) != 0 {
		t.Fatalf("locks=%v error=%v", locks, err)
	}
	var listed []string
	if err := session.List(ctx, "trunk", svn.InvalidRevnum, nil, svn.DepthInfinity, svn.DirentAll, func(path string, _ *svn.Dirent) error {
		listed = append(listed, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("listed paths = %v", listed)
	}
	var logs []*svn.LogEntry
	if err := session.Log(ctx, ra.LogOptions{Paths: []string{"trunk"}, Start: 4, End: 1, DiscoverChangedPaths: true}, func(entry *svn.LogEntry) error {
		logs = append(logs, entry)
		return nil
	}); err != nil {
		t.Fatalf("log: %v\nsvnserve:\n%s", err, server.Output())
	}
	if len(logs) != 4 || logs[0].Revision != 4 || len(logs[0].ChangedPaths) != 1 {
		t.Fatalf("log entries = %#v", logs)
	}
	locations, err := session.GetLocations(ctx, "trunk/README.txt", 4, []svn.Revnum{2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	if len(locations) != 3 || locations[2] != "/trunk/README.txt" {
		t.Fatalf("locations = %v", locations)
	}
	var segments []ra.LocationSegment
	if err := session.GetLocationSegments(ctx, "trunk/README.txt", 4, 4, 0, func(segment ra.LocationSegment) error {
		segments = append(segments, segment)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(segments) == 0 || segments[0].Path != "trunk/README.txt" {
		t.Fatalf("segments = %#v", segments)
	}
	var fileRevisions []svn.Revnum
	if err := session.GetFileRevs(ctx, "trunk/README.txt", 1, 4, false, func(_ context.Context, revision ra.FileRevision) error {
		fileRevisions = append(fileRevisions, revision.Revision)
		if revision.Delta != nil {
			for {
				if _, err := revision.Delta.NextWindow(); err == io.EOF {
					break
				} else if err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(fileRevisions) != 2 || fileRevisions[0] != 2 || fileRevisions[1] != 3 {
		t.Fatalf("file revisions = %v", fileRevisions)
	}
	mergeinfo, err := session.GetMergeinfo(ctx, []string{"trunk"}, 4, mergeinfo.InheritanceExplicit, false)
	if err != nil || len(mergeinfo) != 0 {
		t.Fatalf("mergeinfo=%v error=%v", mergeinfo, err)
	}
	inherited, err := session.GetInheritedProps(ctx, "trunk/README.txt", 4)
	if err != nil || len(inherited) != 0 {
		t.Fatalf("inherited props=%v error=%v", inherited, err)
	}
	if err := session.Reparent(ctx, server.URL+"/trunk"); err != nil {
		t.Fatal(err)
	}
	if kind, err := session.CheckPath(ctx, "README.txt", 4); err != nil || kind != svn.NodeFile {
		t.Fatalf("reparented path kind=%v error=%v", kind, err)
	}
	conformance.Run(t, conformance.Fixture{
		Open: func(t *testing.T) ra.Session {
			opened, _, err := ra.Open(context.Background(), server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			return opened
		},
		RootURL: strings.TrimSuffix(server.URL, "/"), UUID: mustUUID(t, session), Latest: 4,
		FilePath: "trunk/README.txt", OldContent: "go-svn fixture\nsecond line\n", Content: "go-svn fixture\nsecond line\n", Deleted: "trunk/run.sh",
		LatestTime: time.Date(2020, 1, 5, 0, 0, 0, 0, time.UTC), RevProp: "svn:log", RevValue: "delete executable",
	})
}

func mustUUID(t *testing.T, session ra.Session) string {
	t.Helper()
	uuid, err := session.UUID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return uuid
}

func TestMergeinfoAndInheritedPropsAgainstSvnserve(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	tests := []struct {
		name      string
		dump      string
		path      string
		mergeinfo bool
	}{
		{name: "mergeinfo", dump: "mergeinfo.dump", path: "branches/feature", mergeinfo: true},
		{name: "inherited-properties", dump: "props.dump", path: "trunk/props.txt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := filepath.Join(t.TempDir(), "repo")
			if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
				t.Fatalf("svnadmin create: %v:\n%s", err, output)
			}
			dump, err := os.Open(filepath.Join("../testdata/repos", test.dump))
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(svnadmin, "load", "--quiet", repository)
			command.Stdin = dump
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("svnadmin load: %v:\n%s", err, output)
			}
			if err := dump.Close(); err != nil {
				t.Fatal(err)
			}
			server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{})
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			session, _, err := ra.Open(ctx, server.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			latest, err := session.LatestRevision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if test.mergeinfo {
				catalog, err := session.GetMergeinfo(ctx, []string{test.path}, latest, mergeinfo.InheritanceExplicit, true)
				if err != nil || len(catalog) == 0 {
					t.Fatalf("mergeinfo=%v error=%v", catalog, err)
				}
				return
			}
			inherited, err := session.GetInheritedProps(ctx, test.path, latest)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range inherited {
				if strings.TrimSpace(string(item.Props["svn:global-ignores"])) == "*.cache" {
					return
				}
			}
			t.Fatalf("inherited properties=%v", inherited)
		})
	}
}

func TestReadSessionThroughTunnel(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	repository := filepath.Join(t.TempDir(), "repo")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	tunnel := servers.TunnelScript(t, repository)
	t.Setenv("SVN_SSH", tunnel)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, _, err := ra.Open(ctx, "svn+ssh://example.invalid/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if revision, err := session.LatestRevision(ctx); err != nil || revision != 0 {
		t.Fatalf("latest revision=%d error=%v", revision, err)
	}
	if err := session.Reparent(ctx, "svn+ssh://example.invalid/child"); err != nil {
		t.Fatalf("tunnel reparent: %v", err)
	}
}

func TestAuthenticatedReadSession(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	repository := filepath.Join(t.TempDir(), "repo")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{
		AnonymousAccess: servers.AccessNone, AuthenticatedAccess: servers.AccessRead,
	})
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword(server.Username, server.Password)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, _, err := ra.Open(ctx, parsed.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if revision, err := session.LatestRevision(ctx); err != nil || revision != 0 {
		t.Fatalf("latest revision=%d error=%v", revision, err)
	}
}

func TestWriteSessionAgainstSvnserve(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	svnClient := testutil.RequireTool(t, "svn", "GOSVN_SVN")
	repository := filepath.Join(t.TempDir(), "repo")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(repository, "hooks", "pre-revprop-change"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{})
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword(server.Username, server.Password)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, _, err := ra.Open(ctx, parsed.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	commitFile := func(message, content string, base svn.Revnum, lockTokens map[string]string) error {
		editor, err := session.GetCommitEditor(ctx, svn.Props{"svn:log": []byte(message), "custom:commit": []byte("yes")}, lockTokens, false, nil)
		if err != nil {
			return err
		}
		root, err := editor.OpenRoot(ctx, base)
		if err != nil {
			return err
		}
		var file delta.FileEditor
		if base == 0 {
			file, err = root.AddFile(ctx, "written.txt", nil)
		} else {
			file, err = root.OpenFile(ctx, "written.txt", base)
		}
		if err != nil {
			return err
		}
		windows, err := file.ApplyTextDelta(ctx, nil)
		if err != nil {
			return err
		}
		if err := windows.Window(&delta.Window{TargetLength: len(content), Ops: []delta.Op{{Kind: delta.OpNew, Length: len(content)}}, NewData: []byte(content)}); err != nil {
			return err
		}
		if err := windows.Close(); err != nil {
			return err
		}
		checksum := svn.Sum(svn.ChecksumMD5, []byte(content))
		if err := file.Close(ctx, &checksum); err != nil {
			return err
		}
		if err := root.Close(ctx); err != nil {
			return err
		}
		return editor.CloseEdit(ctx)
	}

	if err := commitFile("created by go-svn", "first\n", 0, nil); err != nil {
		t.Fatalf("initial commit: %v\nsvnserve:\n%s", err, server.Output())
	}
	logOutput, err := exec.Command(svnClient, "log", "-v", "--xml", server.URL).CombinedOutput()
	if err != nil || !bytes.Contains(logOutput, []byte("created by go-svn")) || !bytes.Contains(logOutput, []byte("/written.txt")) {
		t.Fatalf("svn log -v --xml: %v:\n%s", err, logOutput)
	}
	if output, err := exec.Command(svnadmin, "verify", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin verify: %v:\n%s", err, output)
	}
	if err := session.ChangeRevProp(ctx, 1, "custom:reviewed", []byte("true"), nil, false); err != nil {
		t.Fatal(err)
	}
	value, found, err := session.RevProp(ctx, 1, "custom:reviewed")
	if err != nil || !found || string(value) != "true" {
		t.Fatalf("changed revprop=%q found=%v error=%v", value, found, err)
	}
	var lock *svn.Lock
	if err := session.Lock(ctx, map[string]svn.Revnum{"written.txt": 1}, "write test", false, func(_ string, value *svn.Lock, callbackErr error) error {
		lock = value
		return callbackErr
	}); err != nil || lock == nil {
		t.Fatalf("lock=%#v error=%v", lock, err)
	}
	if err := commitFile("locked update", "second\n", 1, map[string]string{"written.txt": lock.Token}); err != nil {
		t.Fatalf("locked commit: %v", err)
	}
	if got, err := session.GetLock(ctx, "written.txt"); err != nil || got != nil {
		t.Fatalf("lock after commit=%#v error=%v", got, err)
	}
	lock = nil
	if err := session.Lock(ctx, map[string]svn.Revnum{"written.txt": 2}, "unlock test", false, func(_ string, value *svn.Lock, callbackErr error) error {
		lock = value
		return callbackErr
	}); err != nil || lock == nil {
		t.Fatalf("second lock=%#v error=%v", lock, err)
	}
	if err := session.Unlock(ctx, map[string]string{"written.txt": lock.Token}, false, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := session.GetLock(ctx, "written.txt"); err != nil || got != nil {
		t.Fatalf("lock after unlock=%#v error=%v", got, err)
	}

	hook := "#!/bin/sh\necho 'blocked by integration hook' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(repository, "hooks", "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	err = commitFile("must fail", "third\n", 2, nil)
	if !errors.Is(err, svn.ErrReposHookFailure) || !strings.Contains(err.Error(), "blocked by integration hook") {
		t.Fatalf("hook rejection error=%v", err)
	}
	if latest, latestErr := session.LatestRevision(ctx); latestErr != nil || latest != 2 {
		t.Fatalf("latest after rejection=%d error=%v", latest, latestErr)
	}
}

func TestWriteConformanceAgainstSvnserve(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	repository := filepath.Join(t.TempDir(), "repo")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(repository, "hooks", "pre-revprop-change"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{})
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword(server.Username, server.Password)
	conformance.RunWrites(t, conformance.WriteFixture{
		Open: func(t *testing.T) ra.Session {
			session, _, err := ra.Open(context.Background(), parsed.String(), nil)
			if err != nil {
				t.Fatal(err)
			}
			return session
		},
		RootURL: strings.TrimSuffix(server.URL, "/"),
	})
	if output, err := exec.Command(svnadmin, "verify", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin verify: %v:\n%s", err, output)
	}
}

func TestUpdateAndReplayAgainstSvnserve(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	svnadmin := testutil.RequireTool(t, "svnadmin", "GOSVN_SVNADMIN")
	repository := filepath.Join(t.TempDir(), "repo")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	dump, err := os.Open("../testdata/repos/basic.dump")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(svnadmin, "load", "--quiet", repository)
	command.Stdin = dump
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("svnadmin load: %v:\n%s", err, output)
	}
	_ = dump.Close()
	server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{})
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, _, err := ra.Open(ctx, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	checkout := delta.NewTreeBuilder()
	reporter, err := session.DoUpdate(ctx, 2, "", svn.DepthInfinity, true, false, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.SetPath(ctx, "", 0, svn.DepthInfinity, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.FinishReport(ctx); err != nil {
		t.Fatalf("finish checkout: %v\nsvnserve:\n%s", err, server.Output())
	}
	trunk := checkout.Root().Children["trunk"]
	if trunk == nil || string(trunk.Children["README.txt"].Content) != "go-svn fixture\n" || trunk.Children["run.sh"] == nil {
		t.Fatalf("unexpected checkout tree: %#v", checkout.Root())
	}
	incremental := &recordingEditor{}
	reporter, err = session.DoUpdate(ctx, 4, "", svn.DepthInfinity, true, false, incremental)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.SetPath(ctx, "", 2, svn.DepthInfinity, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.FinishReport(ctx); err != nil {
		t.Fatalf("finish update: %v\nsvnserve:\n%s", err, server.Output())
	}
	if incremental.openedDirs == 0 || incremental.openedFiles == 0 || incremental.deleted == 0 {
		t.Fatalf("incremental callbacks: dirs=%d files=%d deletes=%d", incremental.openedDirs, incremental.openedFiles, incremental.deleted)
	}

	replayed := delta.NewTreeBuilder()
	if err := session.Replay(ctx, 1, 0, true, replayed); err != nil {
		t.Fatalf("replay: %v\nsvnserve:\n%s", err, server.Output())
	}
	if replayed.Root().Children["trunk"] == nil || replayed.Root().Children["branches"] == nil {
		t.Fatalf("unexpected replay tree: %#v", replayed.Root())
	}
	var started, finished int
	if err := session.ReplayRange(ctx, 1, 4, 0, true, func(revision svn.Revnum, props svn.Props) (delta.Editor, error) {
		started++
		if len(props) == 0 {
			t.Fatalf("revision %d has no revprops", revision)
		}
		return &recordingEditor{}, nil
	}, func(svn.Revnum, svn.Props, delta.Editor) error {
		finished++
		return nil
	}); err != nil {
		t.Fatalf("replay range: %v\nsvnserve:\n%s", err, server.Output())
	}
	if started != 4 || finished != 4 {
		t.Fatalf("replay callbacks started=%d finished=%d", started, finished)
	}
	for _, sendDeltas := range []bool{true, false} {
		t.Run(fmt.Sprintf("reconstruct-replay-send-deltas-%v", sendDeltas), func(t *testing.T) {
			assertReplayTrees(t, ctx, session, 4, sendDeltas)
		})
		t.Run(fmt.Sprintf("reconstruct-replay-range-send-deltas-%v", sendDeltas), func(t *testing.T) {
			assertReplayRangeTrees(t, ctx, session, 4, sendDeltas)
		})
	}

	operations := []struct {
		name  string
		start func(delta.Editor) (ra.Reporter, error)
	}{
		{"status", func(editor delta.Editor) (ra.Reporter, error) {
			return session.DoStatus(ctx, "", 4, svn.DepthInfinity, editor)
		}},
		{"diff", func(editor delta.Editor) (ra.Reporter, error) {
			return session.DoDiff(ctx, 4, "", svn.DepthInfinity, false, true, server.URL, editor)
		}},
		{"switch", func(editor delta.Editor) (ra.Reporter, error) {
			return session.DoSwitch(ctx, 4, "", svn.DepthInfinity, server.URL, true, false, editor)
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			recorder := &recordingEditor{}
			reporter, err := operation.start(recorder)
			if err != nil {
				t.Fatal(err)
			}
			if err := reporter.SetPath(ctx, "", 0, svn.DepthInfinity, true, ""); err != nil {
				t.Fatal(err)
			}
			if err := reporter.FinishReport(ctx); err != nil {
				t.Fatalf("%s: %v", operation.name, err)
			}
		})
	}
}

func assertReplayRangeTrees(t *testing.T, ctx context.Context, session ra.Session, latest svn.Revnum, sendDeltas bool) {
	t.Helper()
	expected := make(map[svn.Revnum]*delta.TreeNode, latest)
	for revision := svn.Revnum(1); revision <= latest; revision++ {
		builder := delta.NewTreeBuilder()
		reporter, err := session.DoUpdate(ctx, revision, "", svn.DepthInfinity, true, false, builder)
		if err != nil {
			t.Fatalf("update r%d: %v", revision, err)
		}
		if err := reporter.SetPath(ctx, "", 0, svn.DepthInfinity, true, ""); err != nil {
			t.Fatal(err)
		}
		if err := reporter.FinishReport(ctx); err != nil {
			t.Fatalf("finish update r%d: %v", revision, err)
		}
		stripEntryProps(builder.Root())
		expected[revision] = builder.Root()
	}
	actual := delta.NewTreeBuilder()
	var current *delta.TreeBuilder
	snapshots := make(map[svn.Revnum]*delta.TreeNode, latest)
	err := session.ReplayRange(ctx, 1, latest, 0, sendDeltas, func(svn.Revnum, svn.Props) (delta.Editor, error) {
		current = delta.NewTreeBuilderFrom(actual.Root())
		actual = current
		if !sendDeltas {
			return noTextDeltaEditor{Editor: current}, nil
		}
		return current, nil
	}, func(revision svn.Revnum, _ svn.Props, _ delta.Editor) error {
		snapshots[revision] = cloneReplayTree(t, current.Root())
		return nil
	})
	if err != nil {
		t.Fatalf("replay range: %v", err)
	}
	for revision := svn.Revnum(1); revision <= latest; revision++ {
		actualRoot := snapshots[revision]
		if actualRoot == nil {
			t.Fatalf("replay range omitted r%d", revision)
		}
		if !sendDeltas {
			hydrateReplayTree(t, ctx, session, actualRoot, "", revision)
		}
		stripEntryProps(actualRoot)
		if !reflect.DeepEqual(actualRoot, expected[revision]) {
			actualJSON, _ := json.MarshalIndent(actualRoot, "", "  ")
			expectedJSON, _ := json.MarshalIndent(expected[revision], "", "  ")
			t.Fatalf("replay range tree at r%d differs from update snapshot\nactual: %s\nexpected: %s", revision, actualJSON, expectedJSON)
		}
	}
}

func cloneReplayTree(t *testing.T, root *delta.TreeNode) *delta.TreeNode {
	t.Helper()
	wire, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	var clone delta.TreeNode
	if err := json.Unmarshal(wire, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}

func assertReplayTrees(t *testing.T, ctx context.Context, session ra.Session, latest svn.Revnum, sendDeltas bool) {
	t.Helper()
	actual := delta.NewTreeBuilder()
	for revision := svn.Revnum(1); revision <= latest; revision++ {
		actual = delta.NewTreeBuilderFrom(actual.Root())
		var editor delta.Editor = actual
		if !sendDeltas {
			editor = noTextDeltaEditor{Editor: editor}
		}
		if err := session.Replay(ctx, revision, 0, sendDeltas, editor); err != nil {
			t.Fatalf("replay r%d: %v", revision, err)
		}
		if !sendDeltas {
			hydrateReplayTree(t, ctx, session, actual.Root(), "", revision)
		}
		expected := delta.NewTreeBuilder()
		reporter, err := session.DoUpdate(ctx, revision, "", svn.DepthInfinity, true, false, expected)
		if err != nil {
			t.Fatalf("update r%d: %v", revision, err)
		}
		if err := reporter.SetPath(ctx, "", 0, svn.DepthInfinity, true, ""); err != nil {
			t.Fatal(err)
		}
		if err := reporter.FinishReport(ctx); err != nil {
			t.Fatalf("finish update r%d: %v", revision, err)
		}
		stripEntryProps(actual.Root())
		stripEntryProps(expected.Root())
		if !reflect.DeepEqual(actual.Root(), expected.Root()) {
			actualJSON, _ := json.MarshalIndent(actual.Root(), "", "  ")
			expectedJSON, _ := json.MarshalIndent(expected.Root(), "", "  ")
			t.Fatalf("replay tree at r%d differs from update snapshot\nactual: %s\nexpected: %s", revision, actualJSON, expectedJSON)
		}
	}
}

type noTextDeltaEditor struct{ delta.Editor }

func (editor noTextDeltaEditor) OpenRoot(ctx context.Context, revision svn.Revnum) (delta.DirEditor, error) {
	directory, err := editor.Editor.OpenRoot(ctx, revision)
	return noTextDeltaDir{DirEditor: directory}, err
}

type noTextDeltaDir struct{ delta.DirEditor }

func (directory noTextDeltaDir) AddDirectory(ctx context.Context, path string, source *delta.CopySource) (delta.DirEditor, error) {
	child, err := directory.DirEditor.AddDirectory(ctx, path, source)
	return noTextDeltaDir{DirEditor: child}, err
}

func (directory noTextDeltaDir) OpenDirectory(ctx context.Context, path string, revision svn.Revnum) (delta.DirEditor, error) {
	child, err := directory.DirEditor.OpenDirectory(ctx, path, revision)
	return noTextDeltaDir{DirEditor: child}, err
}

func (directory noTextDeltaDir) AddFile(ctx context.Context, path string, source *delta.CopySource) (delta.FileEditor, error) {
	file, err := directory.DirEditor.AddFile(ctx, path, source)
	return noTextDeltaFile{FileEditor: file}, err
}

func (directory noTextDeltaDir) OpenFile(ctx context.Context, path string, revision svn.Revnum) (delta.FileEditor, error) {
	file, err := directory.DirEditor.OpenFile(ctx, path, revision)
	return noTextDeltaFile{FileEditor: file}, err
}

type noTextDeltaFile struct{ delta.FileEditor }

func (file noTextDeltaFile) Close(ctx context.Context, _ *svn.Checksum) error {
	return file.FileEditor.Close(ctx, nil)
}

func stripEntryProps(node *delta.TreeNode) {
	for name := range node.Props {
		if strings.HasPrefix(name, "svn:entry:") {
			delete(node.Props, name)
		}
	}
	for _, child := range node.Children {
		stripEntryProps(child)
	}
}

func hydrateReplayTree(t *testing.T, ctx context.Context, session ra.Session, node *delta.TreeNode, nodePath string, revision svn.Revnum) {
	t.Helper()
	for name, child := range node.Children {
		childPath := path.Join(nodePath, name)
		if child.Kind == svn.NodeDir {
			hydrateReplayTree(t, ctx, session, child, childPath, revision)
			continue
		}
		var content bytes.Buffer
		_, props, err := session.GetFile(ctx, childPath, revision, &content, true)
		if err != nil {
			t.Fatalf("hydrate %s@%d: %v", childPath, revision, err)
		}
		child.Props = props
		child.Content = append(child.Content[:0], content.Bytes()...)
		child.ContentChecksum = svn.Sum(svn.ChecksumMD5, child.Content)
	}
}

type recordingEditor struct {
	openedDirs  int
	openedFiles int
	deleted     int
}

func (*recordingEditor) SetTargetRevision(context.Context, svn.Revnum) error { return nil }
func (editor *recordingEditor) OpenRoot(context.Context, svn.Revnum) (delta.DirEditor, error) {
	return recordingDir{editor}, nil
}
func (*recordingEditor) CloseEdit(context.Context) error { return nil }
func (*recordingEditor) AbortEdit(context.Context) error { return nil }

type recordingDir struct{ editor *recordingEditor }

func (directory recordingDir) DeleteEntry(context.Context, string, svn.Revnum) error {
	directory.editor.deleted++
	return nil
}
func (directory recordingDir) AddDirectory(context.Context, string, *delta.CopySource) (delta.DirEditor, error) {
	return directory, nil
}
func (directory recordingDir) OpenDirectory(context.Context, string, svn.Revnum) (delta.DirEditor, error) {
	directory.editor.openedDirs++
	return directory, nil
}
func (recordingDir) ChangeProp(context.Context, string, []byte) error { return nil }
func (recordingDir) AbsentDirectory(context.Context, string) error    { return nil }
func (directory recordingDir) AddFile(context.Context, string, *delta.CopySource) (delta.FileEditor, error) {
	return recordingFile{}, nil
}
func (directory recordingDir) OpenFile(context.Context, string, svn.Revnum) (delta.FileEditor, error) {
	directory.editor.openedFiles++
	return recordingFile{}, nil
}
func (recordingDir) AbsentFile(context.Context, string) error { return nil }
func (recordingDir) Close(context.Context) error              { return nil }

type recordingFile struct{}

func (recordingFile) ApplyTextDelta(context.Context, *svn.Checksum) (delta.WindowHandler, error) {
	return delta.WindowHandlerFunc(func(*delta.Window) error { return nil }), nil
}
func (recordingFile) ChangeProp(context.Context, string, []byte) error { return nil }
func (recordingFile) Close(context.Context, *svn.Checksum) error       { return nil }
