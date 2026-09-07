//go:build integration

package client_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/internal/testutil"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
)

func TestReadAndExportMatchReference(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	svnTool := testutil.RequireTool(t, "svn", "GOSVN_SVN")
	svnmucc := testutil.RequireTool(t, "svnmucc", "GOSVN_SVNMUCC")
	readme := filepath.Join(t.TempDir(), "readme")
	keyword := filepath.Join(t.TempDir(), "keyword")
	if err := os.WriteFile(readme, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyword, []byte("$Rev$\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runSVN(t, svnmucc, "-m", "seed", "--username", "alice",
		"mkdir", rootURL+"/trunk", "mkdir", rootURL+"/trunk/sub",
		"put", readme, rootURL+"/trunk/readme", "propset", "custom:p", "value", rootURL+"/trunk/readme",
		"put", keyword, rootURL+"/trunk/sub/keyword", "propset", "svn:keywords", "Rev", rootURL+"/trunk/sub/keyword",
		"propset", "svn:executable", "*", rootURL+"/trunk/sub/keyword")
	if err := os.WriteFile(readme, []byte("one\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSVN(t, svnmucc, "-m", "change", "--username", "alice", "put", readme, rootURL+"/trunk/readme")
	runSVN(t, svnmucc, "-m", "mergeinfo", "--username", "alice",
		"mkdir", rootURL+"/branches", "cp", "2", rootURL+"/trunk/readme", rootURL+"/branches/target",
		"propset", "svn:mergeinfo", "/trunk/readme:1-2", rootURL+"/branches/target")
	runSVN(t, svnmucc, "-m", "move", "--username", "alice",
		"cp", "3", rootURL+"/trunk/readme", rootURL+"/trunk/renamed", "rm", rootURL+"/trunk/readme")

	t.Run("info", func(t *testing.T) {
		actual, err := instance.Info(ctx, rootURL+"/trunk/readme", client.InfoOptions{PegRevision: numericClientRevision(2), Revision: numericClientRevision(2)})
		if err != nil {
			t.Fatal(err)
		}
		var reference struct {
			Entry struct {
				Kind       string `xml:"kind,attr"`
				Revision   string `xml:"revision,attr"`
				URL        string `xml:"url"`
				Repository struct {
					Root string `xml:"root"`
					UUID string `xml:"uuid"`
				} `xml:"repository"`
				Commit struct {
					Revision string `xml:"revision,attr"`
					Author   string `xml:"author"`
				} `xml:"commit"`
			} `xml:"entry"`
		}
		decodeSVNXML(t, svnTool, []string{"info", "--xml", "-r", "2", rootURL + "/trunk/readme@2"}, &reference)
		if actual.URL != reference.Entry.URL || actual.RepositoryRoot != reference.Entry.Repository.Root || actual.RepositoryUUID != reference.Entry.Repository.UUID ||
			strconv.FormatInt(int64(actual.Revision), 10) != reference.Entry.Revision || strconv.FormatInt(int64(actual.ChangedRevision), 10) != reference.Entry.Commit.Revision ||
			actual.ChangedAuthor != reference.Entry.Commit.Author || actual.Kind.String() != reference.Entry.Kind {
			t.Fatalf("go-svn info=%#v reference=%#v", actual, reference.Entry)
		}
	})

	t.Run("info peg and operative revisions", func(t *testing.T) {
		actual, err := instance.Info(ctx, rootURL+"/trunk/renamed", client.InfoOptions{
			PegRevision: numericClientRevision(4),
			Revision:    numericClientRevision(2),
		})
		if err != nil {
			t.Fatal(err)
		}
		var reference struct {
			Entry struct {
				Kind       string `xml:"kind,attr"`
				Revision   string `xml:"revision,attr"`
				URL        string `xml:"url"`
				Repository struct {
					Root string `xml:"root"`
					UUID string `xml:"uuid"`
				} `xml:"repository"`
				Commit struct {
					Revision string `xml:"revision,attr"`
					Author   string `xml:"author"`
				} `xml:"commit"`
			} `xml:"entry"`
		}
		decodeSVNXML(t, svnTool, []string{"info", "--xml", "-r", "2", rootURL + "/trunk/renamed@4"}, &reference)
		if actual.URL != reference.Entry.URL || actual.RepositoryRoot != reference.Entry.Repository.Root || actual.RepositoryUUID != reference.Entry.Repository.UUID ||
			strconv.FormatInt(int64(actual.Revision), 10) != reference.Entry.Revision || strconv.FormatInt(int64(actual.ChangedRevision), 10) != reference.Entry.Commit.Revision ||
			actual.ChangedAuthor != reference.Entry.Commit.Author || actual.Kind.String() != reference.Entry.Kind {
			t.Fatalf("go-svn info=%#v reference=%#v", actual, reference.Entry)
		}
	})

	t.Run("cat", func(t *testing.T) {
		var actual bytes.Buffer
		if err := instance.Cat(ctx, rootURL+"/trunk/readme", &actual, client.CatOptions{InfoOptions: client.InfoOptions{PegRevision: numericClientRevision(2), Revision: numericClientRevision(2)}, IgnoreKeywords: true}); err != nil {
			t.Fatal(err)
		}
		expected := runSVN(t, svnTool, "cat", "-r", "2", rootURL+"/trunk/readme@2")
		if !bytes.Equal(actual.Bytes(), expected) {
			t.Fatalf("go-svn cat=%q reference=%q", actual.Bytes(), expected)
		}
	})

	t.Run("list", func(t *testing.T) {
		var actual []string
		if err := instance.List(ctx, rootURL+"/trunk", client.ListOptions{InfoOptions: client.InfoOptions{Revision: numericClientRevision(2)}, Depth: svn.DepthInfinity}, func(entry client.ListEntry) error {
			actual = append(actual, entry.Path+":"+entry.Entry.Kind.String())
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var reference struct {
			Lists []struct {
				Entries []struct {
					Kind string `xml:"kind,attr"`
					Name string `xml:"name"`
				} `xml:"entry"`
			} `xml:"list"`
		}
		decodeSVNXML(t, svnTool, []string{"list", "--xml", "-R", "-r", "2", rootURL + "/trunk"}, &reference)
		var expected []string
		for _, list := range reference.Lists {
			for _, entry := range list.Entries {
				expected = append(expected, entry.Name+":"+entry.Kind)
			}
		}
		sort.Strings(actual)
		sort.Strings(expected)
		if !equalStrings(actual, expected) {
			t.Fatalf("go-svn list=%v reference=%v", actual, expected)
		}
	})

	t.Run("log", func(t *testing.T) {
		var actual []string
		if err := instance.Log(ctx, rootURL+"/trunk/readme", client.LogOptions{InfoOptions: client.InfoOptions{PegRevision: numericClientRevision(2), Revision: numericClientRevision(2)}, Ranges: []svn.RevisionRange{{Start: numericClientRevision(2), End: numericClientRevision(1)}}, DiscoverChangedPaths: true}, func(entry *svn.LogEntry) error {
			actual = append(actual, strconv.FormatInt(int64(entry.Revision), 10)+":"+entry.Author+":"+entry.Message)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var reference struct {
			Entries []struct {
				Revision string `xml:"revision,attr"`
				Author   string `xml:"author"`
				Message  string `xml:"msg"`
			} `xml:"logentry"`
		}
		decodeSVNXML(t, svnTool, []string{"log", "--xml", "-r", "2:1", rootURL + "/trunk/readme@2"}, &reference)
		var expected []string
		for _, entry := range reference.Entries {
			expected = append(expected, entry.Revision+":"+entry.Author+":"+entry.Message)
		}
		if !equalStrings(actual, expected) {
			t.Fatalf("go-svn log=%v reference=%v", actual, expected)
		}
	})

	t.Run("blame", func(t *testing.T) {
		var actual []string
		if err := instance.Blame(ctx, rootURL+"/trunk/readme", client.BlameOptions{InfoOptions: client.InfoOptions{PegRevision: numericClientRevision(2), Revision: numericClientRevision(2)}, Start: numericClientRevision(1), End: numericClientRevision(2)}, func(line client.BlameLine) error {
			actual = append(actual, strconv.Itoa(line.LineNumber)+":"+strconv.FormatInt(int64(line.Revision), 10)+":"+line.Author)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		var reference struct {
			Targets []struct {
				Entries []struct {
					Line   string `xml:"line-number,attr"`
					Commit struct {
						Revision string `xml:"revision,attr"`
						Author   string `xml:"author"`
					} `xml:"commit"`
				} `xml:"entry"`
			} `xml:"target"`
		}
		decodeSVNXML(t, svnTool, []string{"blame", "--xml", "-r", "1:2", rootURL + "/trunk/readme@2"}, &reference)
		var expected []string
		for _, target := range reference.Targets {
			for _, entry := range target.Entries {
				expected = append(expected, entry.Line+":"+entry.Commit.Revision+":"+entry.Commit.Author)
			}
		}
		if !equalStrings(actual, expected) {
			t.Fatalf("go-svn blame=%v reference=%v", actual, expected)
		}
	})

	t.Run("properties", func(t *testing.T) {
		actual, err := instance.PropList(ctx, rootURL+"/trunk/readme", client.PropertyOptions{InfoOptions: client.InfoOptions{PegRevision: numericClientRevision(2), Revision: numericClientRevision(2)}})
		if err != nil {
			t.Fatal(err)
		}
		var reference struct {
			Targets []struct {
				Properties []struct {
					Name  string `xml:"name,attr"`
					Value string `xml:",chardata"`
				} `xml:"property"`
			} `xml:"target"`
		}
		decodeSVNXML(t, svnTool, []string{"proplist", "--xml", "-v", "-r", "2", rootURL + "/trunk/readme@2"}, &reference)
		for _, target := range reference.Targets {
			for _, property := range target.Properties {
				if string(actual[property.Name]) != property.Value {
					t.Fatalf("property %s=%q, reference=%q", property.Name, actual[property.Name], property.Value)
				}
			}
		}
	})

	t.Run("mergeinfo", func(t *testing.T) {
		actualRevisions, err := instance.MergeinfoRevisions(ctx, rootURL+"/trunk/readme@3", rootURL+"/branches/target@3", numericClientRevision(3), true, 1, 2)
		if err != nil {
			t.Fatal(err)
		}
		actual := make([]string, len(actualRevisions))
		for index, revision := range actualRevisions {
			actual[index] = "r" + strconv.FormatInt(int64(revision), 10)
		}
		expectedBytes := runSVN(t, svnTool, "mergeinfo", "--show-revs", "merged", "-r", "1:2", rootURL+"/trunk/readme@3", rootURL+"/branches/target@3")
		expected := bytes.Fields(expectedBytes)
		expectedStrings := make([]string, len(expected))
		for index := range expected {
			expectedStrings[index] = string(expected[index])
		}
		if !equalStrings(actual, expectedStrings) {
			t.Fatalf("go-svn mergeinfo=%v reference=%v", actual, expectedStrings)
		}
	})

	t.Run("export", func(t *testing.T) {
		actual := filepath.Join(t.TempDir(), "actual")
		expected := filepath.Join(t.TempDir(), "expected")
		if _, err := instance.Export(ctx, rootURL+"/trunk", actual, client.ExportOptions{InfoOptions: client.InfoOptions{Revision: numericClientRevision(2)}}); err != nil {
			t.Fatal(err)
		}
		runSVN(t, svnTool, "export", "-q", "-r", "2", rootURL+"/trunk", expected)
		for _, name := range []string{"readme", "sub/keyword"} {
			actualBytes, actualErr := os.ReadFile(filepath.Join(actual, filepath.FromSlash(name)))
			expectedBytes, expectedErr := os.ReadFile(filepath.Join(expected, filepath.FromSlash(name)))
			if actualErr != nil || expectedErr != nil || !bytes.Equal(actualBytes, expectedBytes) {
				t.Fatalf("export %s: actual=%q (%v), reference=%q (%v)", name, actualBytes, actualErr, expectedBytes, expectedErr)
			}
		}
		actualMode, _ := os.Stat(filepath.Join(actual, "sub", "keyword"))
		expectedMode, _ := os.Stat(filepath.Join(expected, "sub", "keyword"))
		if actualMode.Mode().Perm() != expectedMode.Mode().Perm() {
			t.Fatalf("export mode=%o, reference=%o", actualMode.Mode().Perm(), expectedMode.Mode().Perm())
		}
	})
}

func numericClientRevision(revision svn.Revnum) svn.Revision {
	return svn.Revision{Kind: svn.RevisionNumber, Number: revision}
}

func decodeSVNXML(t *testing.T, svnTool string, arguments []string, destination any) {
	t.Helper()
	output := runSVN(t, svnTool, arguments...)
	if err := xml.Unmarshal(output, destination); err != nil {
		t.Fatalf("decode svn XML: %v\n%s", err, output)
	}
}

func runSVN(t *testing.T, svnTool string, arguments ...string) []byte {
	t.Helper()
	output, err := exec.Command(svnTool, arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("svn %v: %v\n%s", arguments, err, output)
	}
	return output
}

func equalStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}
