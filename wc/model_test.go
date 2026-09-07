package wc

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/ra"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	"github.com/otuschhoff/go-svn/svn"
)

func TestReferenceWorkingModel(t *testing.T) {
	svnTool := requireTool(t, "svn")
	svnadmin := requireTool(t, "svnadmin")
	svnmucc := requireTool(t, "svnmucc")
	temporary := t.TempDir()
	repositoryPath := filepath.Join(temporary, "repository")
	workingPath := filepath.Join(temporary, "working")
	alphaPath, betaPath := filepath.Join(temporary, "alpha"), filepath.Join(temporary, "beta")
	if err := os.WriteFile(alphaPath, []byte("alpha\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(betaPath, []byte("beta\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	runCommand(t, svnadmin, "create", repositoryPath)
	runCommand(t, svnmucc, "-U", "file://"+repositoryPath, "mkdir", "trunk", "mkdir", "trunk/dir", "put", alphaPath, "trunk/a.txt", "put", betaPath, "trunk/b.txt", "put", alphaPath, "trunk/replace.txt", "put", alphaPath, "trunk/remote-mod.txt", "put", alphaPath, "trunk/remote-del.txt", "propset", "svn:eol-style", "CRLF", "trunk/b.txt", "propset", "svn:keywords", "Id", "trunk/b.txt", "-m", "seed")
	runCommand(t, svnTool, "checkout", "--quiet", "file://"+repositoryPath+"/trunk", workingPath)
	runCommand(t, svnTool, "lock", "--quiet", "-m", "test lock", filepath.Join(workingPath, "b.txt"))
	if err := os.WriteFile(filepath.Join(workingPath, "new.txt"), []byte("new\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	runCommand(t, svnTool, "add", "--quiet", filepath.Join(workingPath, "new.txt"))
	runCommand(t, svnTool, "delete", "--quiet", filepath.Join(workingPath, "a.txt"))
	runCommand(t, svnTool, "copy", "--quiet", filepath.Join(workingPath, "b.txt"), filepath.Join(workingPath, "copied.txt"))
	runCommand(t, svnTool, "move", "--quiet", filepath.Join(workingPath, "dir"), filepath.Join(workingPath, "moved-dir"))
	runCommand(t, svnTool, "delete", "--quiet", filepath.Join(workingPath, "replace.txt"))
	if err := os.WriteFile(filepath.Join(workingPath, "replace.txt"), []byte("replacement\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	runCommand(t, svnTool, "add", "--quiet", filepath.Join(workingPath, "replace.txt"))
	runCommand(t, svnTool, "propset", "--quiet", "custom", "value", filepath.Join(workingPath, "b.txt"))
	runCommand(t, svnTool, "propset", "--quiet", "svn:ignore", "*.tmp\nwith space.txt", workingPath)
	runCommand(t, svnTool, "changelist", "group", filepath.Join(workingPath, "b.txt"))
	if err := os.WriteFile(filepath.Join(workingPath, "loose.txt"), []byte("loose\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingPath, "scratch.tmp"), []byte("ignored\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingPath, "artifact.pyc"), []byte("ignored globally\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingPath, "with space.txt"), []byte("ignored with a space\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	remotePath := filepath.Join(temporary, "remote")
	if err := os.WriteFile(remotePath, []byte("remote\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	runCommand(t, svnmucc, "-U", "file://"+repositoryPath, "put", remotePath, "trunk/remote.txt", "put", remotePath, "trunk/remote-mod.txt", "rm", "trunk/remote-del.txt", "-m", "remote change")

	database, err := Open(context.Background(), filepath.Join(workingPath, "b.txt"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if database.RootPath() != workingPath || database.RepositoryRoot() != "file://"+repositoryPath || database.RepositoryUUID() == "" {
		t.Fatalf("root metadata = %q, %q, %q", database.RootPath(), database.RepositoryRoot(), database.RepositoryUUID())
	}
	tests := []struct {
		name       string
		schedule   Schedule
		copied     bool
		movedFrom  string
		movedTo    string
		changelist string
	}{
		{"a.txt", ScheduleDelete, false, "", "", ""},
		{"b.txt", ScheduleNormal, false, "", "", "group"},
		{"copied.txt", ScheduleAdd, true, "", "", ""},
		{"dir", ScheduleDelete, false, "", filepath.Join(workingPath, "moved-dir"), ""},
		{"moved-dir", ScheduleAdd, true, filepath.Join(workingPath, "dir"), "", ""},
		{"new.txt", ScheduleAdd, false, "", "", ""},
		{"replace.txt", ScheduleReplace, false, "", "", ""},
	}
	for _, test := range tests {
		info, err := database.Info(context.Background(), filepath.Join(workingPath, test.name))
		if err != nil {
			t.Fatalf("Info(%s): %v", test.name, err)
		}
		if info.Schedule != test.schedule || info.Copied != test.copied || info.MovedFrom != test.movedFrom || info.MovedTo != test.movedTo || info.Changelist != test.changelist {
			t.Errorf("Info(%s) = schedule %s copied %t moved-from %q moved-to %q changelist %q", test.name, info.Schedule, info.Copied, info.MovedFrom, info.MovedTo, info.Changelist)
		}
		if test.name == "copied.txt" && (info.CopyFromPath != "trunk/b.txt" || info.CopyFromRevision != 1) {
			t.Errorf("copy source = %q@%d", info.CopyFromPath, info.CopyFromRevision)
		}
	}
	deletedInfo, err := database.Info(context.Background(), filepath.Join(workingPath, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if deletedInfo.RepositoryPath != "trunk/a.txt" || deletedInfo.Revision != 1 || deletedInfo.Checksum == nil {
		t.Fatalf("deleted metadata = repository path %q, revision %d, checksum %#v", deletedInfo.RepositoryPath, deletedInfo.Revision, deletedInfo.Checksum)
	}
	bInfo, err := database.Info(context.Background(), filepath.Join(workingPath, "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(bInfo.WorkingProperties["custom"]) != "value" || string(bInfo.BaseProperties["svn:eol-style"]) != "CRLF" || bInfo.BaseProperties["custom"] != nil {
		t.Fatalf("working props = %v, base props = %v", bInfo.WorkingProperties, bInfo.BaseProperties)
	}
	value, found, err := database.PropGet(context.Background(), filepath.Join(workingPath, "b.txt"), "custom", PropertiesWorking)
	if err != nil || !found || string(value) != "value" {
		t.Fatalf("working PropGet = %q, %t, %v", value, found, err)
	}
	_, found, err = database.PropGet(context.Background(), filepath.Join(workingPath, "b.txt"), "custom", PropertiesBase)
	if err != nil || found {
		t.Fatalf("base PropGet custom found = %t, error = %v", found, err)
	}
	if bInfo.Checksum == nil || bInfo.Checksum.Kind != svn.ChecksumSHA1 {
		t.Fatalf("b.txt checksum = %#v, want SHA-1", bInfo.Checksum)
	}
	if bInfo.CopyFromPath != "" || bInfo.CopyFromRevision.IsValid() {
		t.Fatalf("normal node copy source = %q@%d", bInfo.CopyFromPath, bInfo.CopyFromRevision)
	}
	if bInfo.Lock == nil || bInfo.Lock.Token == "" || bInfo.Lock.Comment != "test lock" {
		t.Fatalf("b.txt lock = %#v", bInfo.Lock)
	}
	pristine, err := database.OpenPristine(context.Background(), *bInfo.Checksum)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(pristine)
	pristine.Close()
	if err != nil || string(contents) != "beta\n" {
		t.Fatalf("pristine = %q, error = %v", contents, err)
	}
	translated, err := database.OpenTranslatedTextBase(context.Background(), filepath.Join(workingPath, "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	contents, err = io.ReadAll(translated)
	translated.Close()
	if err != nil || string(contents) != "beta\r\n" {
		t.Fatalf("translated text base = %q, error = %v", contents, err)
	}
	recorder := &reportRecorder{}
	if err := database.Crawl(context.Background(), workingPath, svn.DepthInfinity, recorder); err != nil {
		t.Fatal(err)
	}
	if !recorder.finished || recorder.aborted || len(recorder.calls) != 2 || recorder.calls[0].path != "" || recorder.calls[0].revision != 1 || recorder.calls[1].path != "b.txt" || recorder.calls[1].lockToken != bInfo.Lock.Token {
		t.Fatalf("crawl calls = %#v, finished=%t aborted=%t", recorder.calls, recorder.finished, recorder.aborted)
	}
	if err := os.WriteFile(filepath.Join(workingPath, "b.txt"), []byte("beta changed\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(workingPath, "new.txt")); err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]*Status)
	err = database.Status(context.Background(), workingPath, StatusOptions{Depth: svn.DepthInfinity, Verbose: true, NoIgnore: true}, func(status *Status) error {
		statuses[status.RelativePath] = status
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, statuses, "a.txt", StatusDeleted, StatusDeleted, StatusNormal)
	assertStatus(t, statuses, "b.txt", StatusModified, StatusModified, StatusModified)
	if statuses["b.txt"].Lock == nil || statuses["b.txt"].Lock.Token != bInfo.Lock.Token {
		t.Fatalf("b.txt status lock = %#v", statuses["b.txt"].Lock)
	}
	assertStatus(t, statuses, "copied.txt", StatusAdded, StatusAdded, StatusNormal)
	assertStatus(t, statuses, "loose.txt", StatusUnversioned, StatusUnversioned, StatusNone)
	assertStatus(t, statuses, "replace.txt", StatusReplaced, StatusReplaced, StatusNormal)
	assertStatus(t, statuses, "scratch.tmp", StatusIgnored, StatusIgnored, StatusNone)
	assertStatus(t, statuses, "artifact.pyc", StatusIgnored, StatusIgnored, StatusNone)
	assertStatus(t, statuses, "with space.txt", StatusIgnored, StatusIgnored, StatusNone)
	assertStatus(t, statuses, "new.txt", StatusMissing, StatusMissing, StatusNormal)
	if !statuses["copied.txt"].Copied || statuses["dir"].MovedTo == "" || statuses["moved-dir"].MovedFrom == "" {
		t.Fatalf("copy/move status = copied %t, moved-to %q, moved-from %q", statuses["copied.txt"].Copied, statuses["dir"].MovedTo, statuses["moved-dir"].MovedFrom)
	}
	foundDiff := false
	if err := database.Diff(context.Background(), filepath.Join(workingPath, "b.txt"), svn.DepthEmpty, func(_ context.Context, difference *Difference) error {
		foundDiff = true
		base, err := io.ReadAll(difference.Base)
		if err != nil {
			return err
		}
		working, err := io.ReadAll(difference.Working)
		if err != nil {
			return err
		}
		if string(base) != "beta\n" || string(working) != "beta changed\n" || string(difference.WorkingProperties["custom"]) != "value" {
			t.Fatalf("diff base=%q working=%q properties=%v", base, working, difference.WorkingProperties)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !foundDiff {
		t.Fatal("modified file was omitted from diff")
	}
	if err := database.Diff(context.Background(), filepath.Join(workingPath, "replace.txt"), svn.DepthEmpty, func(_ context.Context, difference *Difference) error {
		base, err := io.ReadAll(difference.Base)
		if err != nil {
			return err
		}
		working, err := io.ReadAll(difference.Working)
		if err != nil {
			return err
		}
		if string(base) != "alpha\n" || string(working) != "replacement\n" {
			t.Fatalf("replacement diff base=%q working=%q", base, working)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	session, _, err := ra.Open(context.Background(), "file://"+repositoryPath+"/trunk", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	remoteStatuses := make(map[string]*Status)
	err = database.Status(context.Background(), workingPath, StatusOptions{Depth: svn.DepthInfinity, Verbose: true, ShowUpdates: true, Session: session}, func(status *Status) error {
		remoteStatuses[status.RelativePath] = status
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if remoteStatuses["remote.txt"] == nil || remoteStatuses["remote.txt"].RepositoryStatus != StatusAdded {
		t.Fatalf("remote.txt repository status = %#v", remoteStatuses["remote.txt"])
	}
	if remoteStatuses["remote-mod.txt"].RepositoryStatus != StatusModified || remoteStatuses["remote-del.txt"].RepositoryStatus != StatusDeleted {
		t.Fatalf("remote changes: modified=%#v deleted=%#v", remoteStatuses["remote-mod.txt"], remoteStatuses["remote-del.txt"])
	}
	if remoteStatuses["b.txt"].RepositoryLock == nil || remoteStatuses["b.txt"].RepositoryLock.Token != bInfo.Lock.Token {
		t.Fatalf("b.txt repository lock = %#v", remoteStatuses["b.txt"].RepositoryLock)
	}
}

func TestReferenceWorkingTopologies(t *testing.T) {
	svnTool := requireTool(t, "svn")
	svnadmin := requireTool(t, "svnadmin")
	svnmucc := requireTool(t, "svnmucc")
	temporary := t.TempDir()
	repositoryPath := filepath.Join(temporary, "repository")
	workingPath := filepath.Join(temporary, "working")
	sparsePath := filepath.Join(temporary, "sparse")
	excludedPath := filepath.Join(temporary, "excluded")
	inheritedPath := filepath.Join(temporary, "inherited")
	incompletePath := filepath.Join(temporary, "incomplete")
	contentPath := filepath.Join(temporary, "content")
	specialPath := filepath.Join(temporary, "special")
	if err := os.WriteFile(contentPath, []byte("content\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specialPath, []byte("link missing-target"), 0o666); err != nil {
		t.Fatal(err)
	}
	runCommand(t, svnadmin, "create", repositoryPath)
	rootURL := fileURL(t, repositoryPath)
	runCommand(t, svnmucc, "-U", rootURL,
		"mkdir", "trunk", "mkdir", "trunk/dir", "put", contentPath, "trunk/dir/file.txt",
		"mkdir", "branch", "mkdir", "branch/dir", "put", contentPath, "branch/dir/file.txt",
		"mkdir", "external", "put", contentPath, "external/external.txt",
		"put", specialPath, "trunk/link", "propset", "svn:special", "*", "trunk/link",
		"propset", "svn:externals", "^/external ext\n^/external/external.txt file-ext", "trunk",
		"propset", "custom:inherited", "value", "trunk", "-m", "topologies")
	runCommand(t, svnTool, "checkout", "--quiet", rootURL+"/trunk", workingPath)
	runCommand(t, svnTool, "switch", "--quiet", "--ignore-ancestry", rootURL+"/branch/dir", filepath.Join(workingPath, "dir"))

	database, err := Open(context.Background(), filepath.Join(workingPath, "dir", "file.txt"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	statuses := make(map[string]*Status)
	if err := database.Status(context.Background(), workingPath, StatusOptions{Depth: svn.DepthInfinity, Verbose: true}, func(status *Status) error {
		statuses[status.RelativePath] = status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if statuses["dir"] == nil || !statuses["dir"].Switched || statuses["dir/file.txt"] == nil || statuses["dir/file.txt"].Switched {
		t.Fatalf("switched statuses: dir=%#v child=%#v", statuses["dir"], statuses["dir/file.txt"])
	}
	if statuses["ext"] == nil || statuses["ext"].NodeStatus != StatusExternal || statuses["ext/external.txt"] != nil {
		t.Fatalf("directory external statuses: root=%#v child=%#v", statuses["ext"], statuses["ext/external.txt"])
	}
	switchedReport := &reportRecorder{}
	if err := database.Crawl(context.Background(), workingPath, svn.DepthInfinity, switchedReport); err != nil {
		t.Fatal(err)
	}
	foundSwitch := false
	for _, call := range switchedReport.calls {
		foundSwitch = foundSwitch || call.path == "dir" && call.url == rootURL+"/branch/dir"
	}
	if !foundSwitch {
		t.Fatalf("switched crawl calls = %#v", switchedReport.calls)
	}
	linkInfo, err := database.Info(context.Background(), filepath.Join(workingPath, "link"))
	if err != nil || linkInfo.Kind != svn.NodeFile {
		t.Fatalf("special link info = %#v, error = %v", linkInfo, err)
	}
	translatedLink, err := database.OpenTranslatedTextBase(context.Background(), filepath.Join(workingPath, "link"))
	if err != nil {
		t.Fatal(err)
	}
	linkTarget, err := io.ReadAll(translatedLink)
	translatedLink.Close()
	if err != nil || string(linkTarget) != "missing-target" {
		t.Fatalf("translated special = %q, error = %v", linkTarget, err)
	}
	if err := os.Remove(filepath.Join(workingPath, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("changed-target", filepath.Join(workingPath, "link")); err != nil {
		t.Fatal(err)
	}
	var linkStatus *Status
	if err := database.Status(context.Background(), filepath.Join(workingPath, "link"), StatusOptions{Depth: svn.DepthEmpty, Verbose: true}, func(status *Status) error {
		linkStatus = status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if linkStatus == nil || linkStatus.NodeStatus != StatusModified {
		t.Fatalf("changed special status = %#v", linkStatus)
	}
	externals, err := database.Externals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(externals) != 2 {
		t.Fatalf("externals = %#v", externals)
	}
	fileExternal, err := database.Info(context.Background(), filepath.Join(workingPath, "file-ext"))
	if err != nil || !fileExternal.FileExternal {
		t.Fatalf("file external = %#v, error = %v", fileExternal, err)
	}
	externalRoot, err := FindRoot(context.Background(), filepath.Join(workingPath, "ext", "external.txt"))
	if err != nil || externalRoot != filepath.Join(workingPath, "ext") {
		t.Fatalf("external root = %q, error = %v", externalRoot, err)
	}
	runCommand(t, svnTool, "checkout", "--quiet", rootURL+"/trunk/dir", inheritedPath)
	inheritedDatabase, err := Open(context.Background(), inheritedPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer inheritedDatabase.Close()
	inherited, err := inheritedDatabase.InheritedPropList(context.Background(), inheritedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(inherited) != 1 || inherited[0].RepositoryPath != "trunk" || string(inherited[0].Properties["custom:inherited"]) != "value" {
		t.Fatalf("inherited properties = %#v", inherited)
	}
	cached, err := inheritedDatabase.InheritedPropList(context.Background(), inheritedPath)
	if err != nil || len(cached) != 1 || string(cached[0].Properties["custom:inherited"]) != "value" {
		t.Fatalf("cached inherited properties = %#v, error = %v", cached, err)
	}

	runCommand(t, svnTool, "checkout", "--quiet", "--depth", "files", rootURL+"/trunk", sparsePath)
	sparse, err := Open(context.Background(), sparsePath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer sparse.Close()
	rootInfo, err := sparse.Info(context.Background(), sparsePath)
	if err != nil || rootInfo.Depth != svn.DepthFiles {
		t.Fatalf("sparse root = %#v, error = %v", rootInfo, err)
	}
	if _, err := sparse.Info(context.Background(), filepath.Join(sparsePath, "dir")); !errors.Is(err, svn.ErrWCPathNotFound) {
		t.Fatalf("unmaterialized sparse child error = %v", err)
	}
	for _, depth := range []svn.Depth{svn.DepthEmpty, svn.DepthImmediates, svn.DepthInfinity} {
		depthPath := filepath.Join(temporary, "depth-"+depth.String())
		runCommand(t, svnTool, "checkout", "--quiet", "--depth", depth.String(), rootURL+"/trunk", depthPath)
		depthDatabase, err := Open(context.Background(), depthPath, Options{})
		if err != nil {
			t.Fatal(err)
		}
		depthInfo, err := depthDatabase.Info(context.Background(), depthPath)
		closeErr := depthDatabase.Close()
		if err != nil || closeErr != nil || depthInfo.Depth != depth {
			t.Fatalf("depth %s root = %#v, info error = %v, close error = %v", depth, depthInfo, err, closeErr)
		}
	}
	runCommand(t, svnTool, "checkout", "--quiet", rootURL+"/trunk", excludedPath)
	runCommand(t, svnTool, "update", "--quiet", "--set-depth", "exclude", filepath.Join(excludedPath, "dir"))
	excluded, err := Open(context.Background(), excludedPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer excluded.Close()
	dirInfo, err := excluded.Info(context.Background(), filepath.Join(excludedPath, "dir"))
	if err != nil || dirInfo.Presence != PresenceExcluded {
		t.Fatalf("explicitly excluded dir = %#v, error = %v", dirInfo, err)
	}
	excludedStatuses := make(map[string]*Status)
	if err := excluded.Status(context.Background(), excludedPath, StatusOptions{Depth: svn.DepthInfinity, Verbose: true}, func(status *Status) error {
		excludedStatuses[status.RelativePath] = status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if excludedStatuses["dir"] != nil {
		t.Fatalf("excluded dir appeared in status: %#v", excludedStatuses["dir"])
	}
	runCommand(t, svnTool, "checkout", "--quiet", rootURL+"/trunk", incompletePath)
	writable, err := sql.Open("sqlite", filepath.Join(incompletePath, ".svn", "wc.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.Exec(`UPDATE NODES SET presence = 'incomplete' WHERE local_relpath = 'dir' AND op_depth = 0`); err != nil {
		writable.Close()
		t.Fatal(err)
	}
	if _, err := writable.Exec(`INSERT INTO WC_LOCK (wc_id, local_dir_relpath, locked_levels) VALUES (1, '', -1)`); err != nil {
		writable.Close()
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	incomplete, err := Open(context.Background(), incompletePath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer incomplete.Close()
	incompleteInfo, err := incomplete.Info(context.Background(), filepath.Join(incompletePath, "dir"))
	if err != nil || incompleteInfo.Presence != PresenceIncomplete {
		t.Fatalf("incomplete dir = %#v, error = %v", incompleteInfo, err)
	}
	incompleteStatuses := make(map[string]*Status)
	if err := incomplete.Status(context.Background(), incompletePath, StatusOptions{Depth: svn.DepthInfinity, Verbose: true}, func(status *Status) error {
		incompleteStatuses[status.RelativePath] = status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if incompleteStatuses["dir"].NodeStatus != StatusIncomplete || !incompleteStatuses["dir"].WCInfoLocked {
		t.Fatalf("incomplete/locked status = %#v", incompleteStatuses["dir"])
	}
	incompleteReport := &reportRecorder{}
	if err := incomplete.Crawl(context.Background(), incompletePath, svn.DepthInfinity, incompleteReport); err != nil {
		t.Fatal(err)
	}
	foundStartEmpty := false
	for _, call := range incompleteReport.calls {
		foundStartEmpty = foundStartEmpty || call.path == "dir" && call.startEmpty
	}
	if !foundStartEmpty {
		t.Fatalf("incomplete crawl calls = %#v", incompleteReport.calls)
	}
}

func TestReferenceConflicts(t *testing.T) {
	svnTool := requireTool(t, "svn")
	svnadmin := requireTool(t, "svnadmin")
	svnmucc := requireTool(t, "svnmucc")
	temporary := t.TempDir()
	repositoryPath := filepath.Join(temporary, "repository")
	workingPath := filepath.Join(temporary, "working")
	basePath := filepath.Join(temporary, "base")
	remotePath := filepath.Join(temporary, "remote")
	if err := os.WriteFile(basePath, []byte("base\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remotePath, []byte("remote\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	runCommand(t, svnadmin, "create", repositoryPath)
	rootURL := fileURL(t, repositoryPath)
	runCommand(t, svnmucc, "-U", rootURL, "mkdir", "trunk", "put", basePath, "trunk/file", "propset", "shared", "base", "trunk/file", "mkdir", "trunk/tree", "put", basePath, "trunk/tree/child", "-m", "seed")
	runCommand(t, svnTool, "checkout", "--quiet", rootURL+"/trunk", workingPath)
	if err := os.WriteFile(filepath.Join(workingPath, "file"), []byte("local\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingPath, "tree", "child"), []byte("local tree\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	runCommand(t, svnTool, "propset", "--quiet", "shared", "local", filepath.Join(workingPath, "file"))
	runCommand(t, svnmucc, "-U", rootURL, "put", remotePath, "trunk/file", "propset", "shared", "remote", "trunk/file", "rm", "trunk/tree", "-m", "conflicts")
	runCommand(t, svnTool, "update", "--quiet", "--accept", "postpone", "--non-interactive", workingPath)

	database, err := Open(context.Background(), workingPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	fileInfo, err := database.Info(context.Background(), filepath.Join(workingPath, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Conflict == nil || !fileInfo.Conflict.Text || !fileInfo.Conflict.Property || fileInfo.Conflict.Tree || fileInfo.Conflict.OldPath == "" || fileInfo.Conflict.NewPath == "" || fileInfo.Conflict.WorkingPath == "" || fileInfo.Conflict.PropertyPath == "" {
		t.Fatalf("file conflict = %#v", fileInfo.Conflict)
	}
	for _, marker := range []string{fileInfo.Conflict.OldPath, fileInfo.Conflict.NewPath, fileInfo.Conflict.WorkingPath, fileInfo.Conflict.PropertyPath} {
		if !filepath.IsAbs(marker) {
			t.Fatalf("conflict marker is not absolute: %q", marker)
		}
	}
	treeInfo, err := database.Info(context.Background(), filepath.Join(workingPath, "tree"))
	if err != nil {
		t.Fatal(err)
	}
	if treeInfo.Conflict == nil || !treeInfo.Conflict.Tree {
		t.Fatalf("tree conflict = %#v", treeInfo.Conflict)
	}
	statuses := make(map[string]*Status)
	if err := database.Status(context.Background(), workingPath, StatusOptions{Depth: svn.DepthInfinity, Verbose: true}, func(status *Status) error {
		statuses[status.RelativePath] = status
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !statuses["file"].Conflicted || !statuses["tree"].TreeConflicted {
		t.Fatalf("conflict statuses: file=%#v tree=%#v", statuses["file"], statuses["tree"])
	}
}

type reportCall struct {
	path, url  string
	revision   svn.Revnum
	depth      svn.Depth
	startEmpty bool
	lockToken  string
}

type reportRecorder struct {
	calls             []reportCall
	deleted           []string
	finished, aborted bool
}

func (reporter *reportRecorder) SetPath(_ context.Context, name string, revision svn.Revnum, depth svn.Depth, startEmpty bool, lockToken string) error {
	reporter.calls = append(reporter.calls, reportCall{path: name, revision: revision, depth: depth, startEmpty: startEmpty, lockToken: lockToken})
	return nil
}

func (reporter *reportRecorder) LinkPath(_ context.Context, name, url string, revision svn.Revnum, depth svn.Depth, startEmpty bool, lockToken string) error {
	reporter.calls = append(reporter.calls, reportCall{path: name, url: url, revision: revision, depth: depth, startEmpty: startEmpty, lockToken: lockToken})
	return nil
}

func (reporter *reportRecorder) DeletePath(_ context.Context, name string) error {
	reporter.deleted = append(reporter.deleted, name)
	return nil
}

func (reporter *reportRecorder) FinishReport(context.Context) error {
	reporter.finished = true
	return nil
}
func (reporter *reportRecorder) AbortReport(context.Context) error {
	reporter.aborted = true
	return nil
}

var _ ra.Reporter = (*reportRecorder)(nil)

func assertStatus(t *testing.T, statuses map[string]*Status, name string, node, text, property StatusKind) {
	t.Helper()
	status := statuses[name]
	if status == nil || status.NodeStatus != node || status.TextStatus != text || status.PropertyStatus != property {
		t.Fatalf("status %s = %#v, want node=%s text=%s property=%s", name, status, node, text, property)
	}
}

func requireTool(t *testing.T, name string) string {
	t.Helper()
	tool, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed", name)
	}
	return tool
}

func runCommand(t *testing.T, name string, arguments ...string) {
	t.Helper()
	if output, err := exec.Command(name, arguments...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, arguments, err, output)
	}
}
