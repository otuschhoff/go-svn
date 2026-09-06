package ralocal

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/internal/testutil/servers"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/ra/conformance"
	_ "github.com/otuschhoff/go-svn/rasvn"
	"github.com/otuschhoff/go-svn/svn"
)

func TestConformanceAcrossFSFSFormats(t *testing.T) {
	for _, number := range []int{1, 2, 3, 4, 6, 7, 8} {
		number := number
		t.Run(strconv.Itoa(number), func(t *testing.T) {
			repository := extractFixture(t, number)
			rootURL := fileURL(repository)
			conformance.Run(t, conformance.Fixture{
				Open: func(t *testing.T) ra.Session {
					t.Helper()
					session, _, err := ra.Open(context.Background(), rootURL, nil)
					if err != nil {
						t.Fatal(err)
					}
					return session
				},
				RootURL:    rootURL,
				UUID:       fmt.Sprintf("10000000-0000-0000-0000-%012d", number),
				Latest:     4,
				FilePath:   "trunk/README.txt",
				OldContent: "go-svn fixture\nsecond line\n",
				Content:    "go-svn fixture\nsecond line\n",
				Deleted:    "trunk/run.sh",
				LatestTime: time.Date(2020, 1, 5, 0, 0, 0, 0, time.UTC),
				RevProp:    "svn:log",
				RevValue:   "delete executable",
			})
		})
	}
}

func TestWriteConformance(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	rootURL := fileURL(repository)
	conformance.RunWrites(t, conformance.WriteFixture{
		Open: func(t *testing.T) ra.Session {
			t.Helper()
			session, _, err := ra.Open(context.Background(), rootURL, nil)
			if err != nil {
				t.Fatal(err)
			}
			return session
		},
		RootURL: rootURL,
	})
}

func TestFileURLForms(t *testing.T) {
	repository := extractFixture(t, 8)
	root := fileURL(repository)
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "root", url: root, want: root},
		{name: "localhost", url: strings.Replace(root, "file://", "file://localhost", 1), want: root},
		{name: "child", url: root + "/trunk", want: root + "/trunk"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, actual, err := ra.Open(context.Background(), test.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if actual != test.want {
				t.Fatalf("URL = %q, want %q", actual, test.want)
			}
		})
	}
	if _, _, err := ra.Open(context.Background(), strings.Replace(root, "file://", "file://remote.example", 1), nil); err == nil {
		t.Fatal("remote file authority was accepted")
	}
	session, _, err := ra.Open(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Reparent(context.Background(), root+"-other"); err == nil {
		t.Fatal("reparent outside repository was accepted")
	}
	spacedParent := filepath.Join(t.TempDir(), "repository with spaces")
	if err := os.Rename(repository, spacedParent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ra.Open(context.Background(), fileURL(spacedParent)+"/trunk", nil); err != nil {
		t.Fatalf("escaped file URL: %v", err)
	}
}

func TestLocalFilePathWindowsDrive(t *testing.T) {
	if got := filepath.ToSlash(localFilePath("/C:/repositories/sample", "windows")); got != "C:/repositories/sample" {
		t.Fatalf("Windows file URL path = %q", got)
	}
}

func TestSubversionClientOracle(t *testing.T) {
	svnPath, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn client is not installed")
	}
	repository := extractFixture(t, 8)
	rootURL := fileURL(repository)
	session, _, err := ra.Open(context.Background(), rootURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	var content bytes.Buffer
	if _, _, err := session.GetFile(context.Background(), "trunk/README.txt", 4, &content, false); err != nil {
		t.Fatal(err)
	}
	oracleContent, err := exec.Command(svnPath, "cat", rootURL+"/trunk/README.txt@4").Output()
	if err != nil || !bytes.Equal(content.Bytes(), oracleContent) {
		t.Fatalf("svn cat mismatch: error=%v", err)
	}
	entries, _, _, err := session.GetDir(context.Background(), "trunk", 4, svn.DirentKind)
	if err != nil {
		t.Fatal(err)
	}
	oracleList, err := exec.Command(svnPath, "list", "-r", "4", rootURL+"/trunk").Output()
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotNames = append(gotNames, entry.Path)
	}
	sort.Strings(gotNames)
	wantNames := strings.Fields(string(oracleList))
	for index := range wantNames {
		wantNames[index] = strings.TrimSuffix(wantNames[index], "/")
	}
	sort.Strings(wantNames)
	if strings.Join(gotNames, "\n") != strings.Join(wantNames, "\n") {
		t.Fatalf("svn list = %v, local = %v", wantNames, gotNames)
	}
	var localRevisions []svn.Revnum
	if err := session.Log(context.Background(), ra.LogOptions{Start: 4, End: 1}, func(entry *svn.LogEntry) error {
		localRevisions = append(localRevisions, entry.Revision)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oracleLog, err := exec.Command(svnPath, "log", "--xml", "-r", "4:1", rootURL).Output()
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Entries []struct {
			Revision svn.Revnum `xml:"revision,attr"`
		} `xml:"logentry"`
	}
	if err := xml.Unmarshal(oracleLog, &parsed); err != nil {
		t.Fatal(err)
	}
	wantRevisions := make([]svn.Revnum, len(parsed.Entries))
	for index, entry := range parsed.Entries {
		wantRevisions[index] = entry.Revision
	}
	if fmt.Sprint(localRevisions) != fmt.Sprint(wantRevisions) {
		t.Fatalf("svn log revisions = %v, local = %v", wantRevisions, localRevisions)
	}
}

func TestWindowWriterBoundsLargeFiles(t *testing.T) {
	handler := &sizingWindowHandler{}
	written, err := io.Copy(&windowWriter{handler: handler}, io.LimitReader(zeroReader{}, 50<<20))
	if err != nil {
		t.Fatal(err)
	}
	if written != 50<<20 || handler.total != written || handler.maximum > 64<<10 {
		t.Fatalf("written=%d total=%d maximum=%d", written, handler.total, handler.maximum)
	}
}

func TestReporterDeletePathRestoresCurrentNode(t *testing.T) {
	repository := extractFixture(t, 8)
	session, _, err := ra.Open(context.Background(), fileURL(repository), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	ctx := context.Background()
	initial := delta.NewTreeBuilder()
	reporter, err := session.DoUpdate(ctx, 4, "", svn.DepthInfinity, true, false, initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.SetPath(ctx, "", 0, svn.DepthInfinity, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.FinishReport(ctx); err != nil {
		t.Fatal(err)
	}
	trunk := initial.Root().Children["trunk"]
	delete(trunk.Children, "README.txt")

	updated := delta.NewTreeBuilderFrom(initial.Root())
	reporter, err = session.DoUpdate(ctx, 4, "", svn.DepthInfinity, true, false, updated)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.SetPath(ctx, "", 4, svn.DepthInfinity, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.DeletePath(ctx, "trunk/README.txt"); err != nil {
		t.Fatal(err)
	}
	if err := reporter.FinishReport(ctx); err != nil {
		t.Fatal(err)
	}
	if got := string(trunk.Children["README.txt"].Content); got != "go-svn fixture\nsecond line\n" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestReporterHonorsExcludedPath(t *testing.T) {
	repository := extractFixture(t, 8)
	session, _, err := ra.Open(context.Background(), fileURL(repository), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx := context.Background()
	tree := delta.NewTreeBuilder()
	reporter, err := session.DoUpdate(ctx, 4, "", svn.DepthInfinity, true, false, tree)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.SetPath(ctx, "", 0, svn.DepthInfinity, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.SetPath(ctx, "trunk", svn.InvalidRevnum, svn.DepthExclude, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.FinishReport(ctx); err != nil {
		t.Fatal(err)
	}
	if tree.Root().Children["trunk"] != nil {
		t.Fatal("excluded report path was driven into the editor")
	}
}

func TestLogIncludesMergedRevisions(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	dump, err := os.Open(filepath.Join("..", "..", "testdata", "repos", "mergeinfo.dump"))
	if err != nil {
		t.Fatal(err)
	}
	defer dump.Close()
	command := exec.Command(svnadmin, "load", "--quiet", repository)
	command.Stdin = dump
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("svnadmin load: %v:\n%s", err, output)
	}
	session, _, err := ra.Open(context.Background(), fileURL(repository), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var entries []*svn.LogEntry
	err = session.Log(context.Background(), ra.LogOptions{
		Paths:                []string{"branches/feature"},
		Start:                4,
		End:                  1,
		DiscoverChangedPaths: true,
		IncludeMerged:        true,
	}, func(entry *svn.LogEntry) error {
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []svn.Revnum{4, 2, 3, 1}
	got := make([]svn.Revnum, len(entries))
	for index, entry := range entries {
		got[index] = entry.Revision
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("merged log revisions = %v, want %v", got, want)
	}
	if !entries[0].HasChildren || entries[1].SubtractiveMerge {
		t.Fatalf("merged log flags = parent:%t subtractive:%t", entries[0].HasChildren, entries[1].SubtractiveMerge)
	}
}

func TestSwitchUsesRequestedRepositoryPath(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	dump, err := os.Open(filepath.Join("..", "..", "testdata", "repos", "copies.dump"))
	if err != nil {
		t.Fatal(err)
	}
	defer dump.Close()
	command := exec.Command(svnadmin, "load", "--quiet", repository)
	command.Stdin = dump
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("svnadmin load: %v:\n%s", err, output)
	}
	rootURL := fileURL(repository)
	session, _, err := ra.Open(context.Background(), rootURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tree := delta.NewTreeBuilder()
	reporter, err := session.DoSwitch(context.Background(), 4, "", svn.DepthInfinity, rootURL+"/branches/feature", true, false, tree)
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.SetPath(context.Background(), "", 0, svn.DepthInfinity, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := reporter.FinishReport(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := string(tree.Root().Children["moved.txt"].Content); got != "copy source\n" {
		t.Fatalf("switched content = %q", got)
	}
	if tree.Root().Children["trunk"] != nil {
		t.Fatal("switch returned the session root instead of the requested branch")
	}
	local := session.(*Session)
	root, _, err := local.repository.Root(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := root.PathsChanged(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source, err := copySource(context.Background(), local.repository, root, changes, "branches/feature", true, svn.InvalidRevnum)
	if err != nil {
		t.Fatal(err)
	}
	if source == nil || source.Path != "/trunk" || source.Rev != 1 {
		copyPath, copyRevision, copyErr := root.ClosestCopy(context.Background(), "branches/feature")
		t.Fatalf("copy source = %+v, closest copy = %q@%d, error = %v", source, copyPath, copyRevision, copyErr)
	}
	filtered, err := copySource(context.Background(), local.repository, root, changes, "branches/feature", true, 2)
	if err != nil || filtered != nil {
		t.Fatalf("copy source below low-water mark = %+v, error = %v", filtered, err)
	}
	if err := session.Replay(context.Background(), 4, svn.InvalidRevnum, true, delta.NewTreeBuilder()); !errors.Is(err, svn.ErrIncorrectParams) {
		t.Fatalf("invalid replay low-water error = %v", err)
	}
}

func TestMergedFileRevisionsMatchSvnserve(t *testing.T) {
	svnadmin, adminErr := exec.LookPath("svnadmin")
	svn, svnErr := exec.LookPath("svn")
	svnserve, serveErr := exec.LookPath("svnserve")
	if adminErr != nil || svnErr != nil || serveErr != nil {
		t.Skip("Subversion command-line tools are not installed")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	runCommand(t, "", svnadmin, "create", repository)
	rootURL := fileURL(repository)
	runCommand(t, "", svn, "mkdir", rootURL+"/trunk", rootURL+"/branches", "-m", "layout")
	workingCopy := filepath.Join(t.TempDir(), "wc")
	runCommand(t, "", svn, "checkout", rootURL, workingCopy)
	fileName := filepath.Join(workingCopy, "trunk", "file.txt")
	if err := os.WriteFile(fileName, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, workingCopy, svn, "add", "trunk/file.txt")
	runCommand(t, workingCopy, svn, "commit", "-m", "add file")
	runCommand(t, "", svn, "copy", rootURL+"/trunk", rootURL+"/branches/feature", "-m", "branch")
	runCommand(t, workingCopy, svn, "update")
	if err := os.WriteFile(filepath.Join(workingCopy, "branches", "feature", "file.txt"), []byte("branch change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, workingCopy, svn, "commit", "-m", "branch change")
	runCommand(t, workingCopy, svn, "update")
	runCommand(t, "", svn, "merge", rootURL+"/branches/feature", filepath.Join(workingCopy, "trunk"))
	runCommand(t, workingCopy, svn, "commit", "-m", "merge branch", "trunk")

	server := servers.StartSvnserve(t, repository, servers.SvnserveOptions{Path: svnserve})
	defer server.Close()
	localSessionValue, _, err := ra.Open(context.Background(), rootURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	localSession := localSessionValue.(*Session)
	mergeChanges, err := localSession.repository.MergeinfoChanges(context.Background(), 5, []string{"trunk/file.txt"})
	if err != nil {
		t.Fatal(err)
	}
	sourcePoints, err := localSession.interestingFileRevisions(context.Background(), "branches/feature/file.txt", 3, 4, true)
	if err != nil {
		t.Fatal(err)
	}
	localSession.Close()
	if len(sourcePoints) != 2 {
		t.Fatalf("merge changes = %v, source points = %v", mergeChanges, sourcePoints)
	}
	want := fileRevisionTuples(t, server.URL, "trunk/file.txt")
	got := fileRevisionTuples(t, rootURL, "trunk/file.txt")
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("local merged file revisions = %v, svnserve = %v, merge changes = %v", got, want, mergeChanges)
	}
}

func TestReporterHonorsIgnoreAncestry(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	repository := filepath.Join(t.TempDir(), "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v:\n%s", err, output)
	}
	dump, err := os.Open(filepath.Join("..", "..", "testdata", "repos", "symlinks.dump"))
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
	session, _, err := ra.Open(context.Background(), fileURL(repository), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	buildAtTwo := func() *delta.TreeBuilder {
		tree := delta.NewTreeBuilder()
		reporter, err := session.DoUpdate(context.Background(), 2, "", svn.DepthInfinity, true, false, tree)
		if err != nil {
			t.Fatal(err)
		}
		if err := reporter.SetPath(context.Background(), "", 0, svn.DepthInfinity, true, ""); err != nil {
			t.Fatal(err)
		}
		if err := reporter.FinishReport(context.Background()); err != nil {
			t.Fatal(err)
		}
		return tree
	}
	for _, test := range []struct {
		name           string
		ignoreAncestry bool
		wantReplaced   bool
	}{
		{name: "honor ancestry", wantReplaced: true},
		{name: "ignore ancestry", ignoreAncestry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree := buildAtTwo()
			oldNode := tree.Root().Children["trunk"].Children["link.txt"]
			updated := delta.NewTreeBuilderFrom(tree.Root())
			reporter, err := session.DoUpdate(context.Background(), 3, "", svn.DepthInfinity, true, test.ignoreAncestry, updated)
			if err != nil {
				t.Fatal(err)
			}
			if err := reporter.SetPath(context.Background(), "", 2, svn.DepthInfinity, false, ""); err != nil {
				t.Fatal(err)
			}
			if err := reporter.FinishReport(context.Background()); err != nil {
				t.Fatal(err)
			}
			newNode := tree.Root().Children["trunk"].Children["link.txt"]
			if replaced := oldNode != newNode; replaced != test.wantReplaced {
				t.Fatalf("node replaced = %t, want %t", replaced, test.wantReplaced)
			}
		})
	}
}

type fileRevisionTuple struct {
	path     string
	revision svn.Revnum
	merged   bool
}

func fileRevisionTuples(t *testing.T, rawURL, name string) []fileRevisionTuple {
	t.Helper()
	session, _, err := ra.Open(context.Background(), rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var result []fileRevisionTuple
	err = session.GetFileRevs(context.Background(), name, 0, svn.InvalidRevnum, true, func(_ context.Context, revision ra.FileRevision) error {
		result = append(result, fileRevisionTuple{path: revision.Path, revision: revision.Revision, merged: revision.Merged})
		if revision.Delta != nil {
			for {
				_, err := revision.Delta.NextWindow()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func runCommand(t *testing.T, directory, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "LC_ALL=C")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v:\n%s", name, strings.Join(arguments, " "), err, output)
	}
}

type zeroReader struct{}

func (zeroReader) Read(data []byte) (int, error) {
	clear(data)
	return len(data), nil
}

type sizingWindowHandler struct {
	total   int64
	maximum int
}

func (handler *sizingWindowHandler) Window(window *delta.Window) error {
	if window != nil {
		handler.total += int64(len(window.NewData))
		handler.maximum = max(handler.maximum, len(window.NewData))
	}
	return nil
}

func (*sizingWindowHandler) Close() error { return nil }

func extractFixture(t *testing.T, number int) string {
	t.Helper()
	archive, err := os.Open(filepath.Join("..", "..", "testdata", "fsfs", "format"+strconv.Itoa(number)+".tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	destination := t.TempDir()
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if filepath.IsAbs(name) || name == ".." || len(name) > 3 && name[:3] == ".."+string(filepath.Separator) {
			t.Fatalf("unsafe archive path %q", header.Name)
		}
		filePath := filepath.Join(destination, name)
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(filePath, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode())
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(file, reader)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatal(copyErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	return filepath.Join(destination, "format"+strconv.Itoa(number))
}
