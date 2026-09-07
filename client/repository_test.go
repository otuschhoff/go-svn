package client_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/ra"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
	"github.com/otuschhoff/go-svn/wc"
)

func TestCopyURLPinsExternals(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	revprops := svn.Props{"svn:log": []byte("test")}
	if _, err := instance.MkdirURL(ctx, rootURL, []string{"trunk/source/nested", "trunk/external"}, true, revprops); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Mucc(ctx, rootURL, []client.Action{{
		Kind: client.ActionSetProperty, Path: "trunk/source", PropertyName: props.Externals, PropertyValue: []byte("^/trunk/external ext\n"),
	}, {
		Kind: client.ActionSetProperty, Path: "trunk/source/nested", PropertyName: props.Externals, PropertyValue: []byte("../../external nested-ext\n"),
	}}, client.MuccOptions{RevisionProperties: revprops, BaseRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.CopyURLWithOptions(ctx, rootURL, rootURL+"/trunk/source", "trunk/copied", 2, svn.NodeDir, client.CopyURLOptions{
		RevisionProperties: revprops,
		PinExternals:       true,
	}); err != nil {
		t.Fatal(err)
	}
	properties, err := instance.PropList(ctx, rootURL+"/trunk/copied", client.PropertyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(properties[props.Externals]), "-r2 ^/trunk/external@2 ext\n"; got != want {
		t.Fatalf("pinned externals = %q, want %q", got, want)
	}
	properties, err = instance.PropList(ctx, rootURL+"/trunk/copied/nested", client.PropertyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(properties[props.Externals]), "-r2 ../../external@2 nested-ext\n"; got != want {
		t.Fatalf("nested pinned externals = %q, want %q", got, want)
	}
}

func TestRepositoryReadAndMutationSequence(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	var notifications []notify.Notify
	instance := client.New(&ra.Callbacks{Notify: func(event notify.Notify) { notifications = append(notifications, event) }})
	revprops := func(message string) svn.Props {
		return svn.Props{"svn:author": []byte("alice"), "svn:log": []byte(message)}
	}

	info, err := instance.MkdirURL(ctx, rootURL, []string{"trunk/nested"}, true, revprops("mkdir"))
	if err != nil || info.Revision != 1 {
		t.Fatalf("mkdir info=%#v error=%v", info, err)
	}
	info, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionPut, Path: "trunk/readme.txt", Content: []byte("hello\n")},
		{Kind: client.ActionSetProperty, Path: "trunk/readme.txt", PropertyName: "custom:name", PropertyValue: []byte("readme")},
	}, client.MuccOptions{RevisionProperties: revprops("put"), BaseRevision: svn.InvalidRevnum})
	if err != nil || info.Revision != 2 {
		t.Fatalf("put info=%#v error=%v", info, err)
	}

	var contents bytes.Buffer
	if err := instance.Cat(ctx, rootURL+"/trunk/readme.txt@2", &contents, client.CatOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}, IgnoreKeywords: true}); err != nil || contents.String() != "hello\n" {
		t.Fatalf("cat=%q error=%v", contents.String(), err)
	}
	properties, err := instance.PropList(ctx, rootURL+"/trunk/readme.txt", client.PropertyOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}})
	if err != nil || string(properties["custom:name"]) != "readme" {
		t.Fatalf("properties=%v error=%v", properties, err)
	}
	var listed []string
	if err := instance.List(ctx, rootURL+"/trunk", client.ListOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}, Depth: svn.DepthImmediates}, func(entry client.ListEntry) error {
		listed = append(listed, entry.Path)
		return nil
	}); err != nil || len(listed) != 2 {
		t.Fatalf("listed=%v error=%v", listed, err)
	}

	info, err = instance.CopyURL(ctx, rootURL, rootURL+"/trunk/readme.txt", "trunk/copied.txt", 2, svn.NodeFile, revprops("copy"))
	if err != nil || info.Revision != 3 {
		t.Fatalf("copy info=%#v error=%v", info, err)
	}
	info, err = instance.MoveURL(ctx, rootURL, rootURL+"/trunk/copied.txt", "trunk/moved.txt", 3, svn.NodeFile, revprops("move"))
	if err != nil || info.Revision != 4 {
		t.Fatalf("move info=%#v error=%v", info, err)
	}
	info, err = instance.DeleteURL(ctx, rootURL, []string{"trunk/readme.txt"}, 4, revprops("delete"))
	if err != nil || info.Revision != 5 {
		t.Fatalf("delete info=%#v error=%v", info, err)
	}

	var logs []*svn.LogEntry
	if err := instance.Log(ctx, rootURL+"/trunk", client.LogOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionHead}}, DiscoverChangedPaths: true, Search: []string{"move"}}, func(entry *svn.LogEntry) error {
		logs = append(logs, entry)
		return nil
	}); err != nil || len(logs) != 1 || logs[0].Revision != 4 {
		t.Fatalf("logs=%#v error=%v", logs, err)
	}
	var logDiff bytes.Buffer
	if err := instance.Log(ctx, rootURL+"/trunk", client.LogOptions{
		Ranges:     []svn.RevisionRange{{Start: svn.Revision{Kind: svn.RevisionNumber, Number: 2}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}},
		DiffOutput: &logDiff, DiffOptions: client.DiffOptions{Depth: svn.DepthInfinity},
	}, func(*svn.LogEntry) error { return nil }); err != nil || !bytes.Contains(logDiff.Bytes(), []byte("+hello\n")) {
		t.Fatalf("log diff=%q error=%v", logDiff.String(), err)
	}

	importSource := filepath.Join(t.TempDir(), "import")
	if err := os.MkdirAll(filepath.Join(importSource, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importSource, "nested", "script.sh"), []byte("echo imported\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importSource, "ignored.tmp"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err = instance.Import(ctx, importSource, rootURL, "imported", client.ImportOptions{
		RevisionProperties: revprops("import"), Depth: svn.DepthInfinity, Parents: true, GlobalIgnores: []string{"*.tmp"},
		AutoProps: map[string]svn.Props{"*.sh": {"custom:script": []byte("yes"), "svn:mime-type": []byte("text/plain")}},
	})
	if err != nil || info.Revision != 6 {
		t.Fatalf("import info=%#v error=%v", info, err)
	}
	exportDestination := filepath.Join(t.TempDir(), "export")
	if _, err := instance.Export(ctx, rootURL+"/imported", exportDestination, client.ExportOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 6}}}); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(exportDestination, "nested", "script.sh")); err != nil || string(content) != "echo imported\n" {
		t.Fatalf("exported content=%q error=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(exportDestination, "ignored.tmp")); !os.IsNotExist(err) {
		t.Fatalf("ignored export stat error=%v", err)
	}
	properties, err = instance.PropList(ctx, rootURL+"/imported/nested/script.sh", client.PropertyOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 6}}})
	if err != nil || string(properties["custom:script"]) != "yes" || runtime.GOOS != "windows" && len(properties["svn:executable"]) == 0 {
		t.Fatalf("imported properties=%v error=%v", properties, err)
	}

	lock, err := instance.Lock(ctx, rootURL+"/trunk/moved.txt", client.LockOptions{Comment: "client lock"})
	if err != nil || lock == nil || lock.Token == "" {
		t.Fatalf("lock=%#v error=%v", lock, err)
	}
	lockedInfo, err := instance.Info(ctx, rootURL+"/trunk/moved.txt", client.InfoOptions{})
	if err != nil || lockedInfo.Lock == nil || lockedInfo.Lock.Token != lock.Token {
		t.Fatalf("locked info=%#v error=%v", lockedInfo, err)
	}
	if err := instance.Unlock(ctx, rootURL+"/trunk/moved.txt", false); err != nil {
		t.Fatal(err)
	}

	info, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionPut, Path: "imported/nested/script.sh", Content: []byte("first\nsecond\n")}}, client.MuccOptions{RevisionProperties: revprops("rewrite"), BaseRevision: 6})
	if err != nil || info.Revision != 7 {
		t.Fatalf("rewrite info=%#v error=%v", info, err)
	}
	info, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionPut, Path: "imported/nested/script.sh", Content: []byte("first\nchanged\nthird\n")}}, client.MuccOptions{RevisionProperties: revprops("edit"), BaseRevision: 7})
	if err != nil || info.Revision != 8 {
		t.Fatalf("edit info=%#v error=%v", info, err)
	}
	var difference bytes.Buffer
	left := client.DiffTarget{Target: rootURL + "/imported/nested/script.sh", Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 7}}
	right := client.DiffTarget{Target: rootURL + "/imported/nested/script.sh", Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 8}}
	if err := instance.Diff(ctx, left, right, &difference, client.DiffOptions{}); err != nil || !bytes.Contains(difference.Bytes(), []byte("-second\n+changed\n+third\n")) {
		t.Fatalf("diff=%q error=%v", difference.String(), err)
	}
	var summaries []client.DiffSummary
	if err := instance.DiffSummarize(ctx, left, right, client.DiffOptions{}, func(summary client.DiffSummary) error {
		summaries = append(summaries, summary)
		return nil
	}); err != nil || len(summaries) != 1 || summaries[0].NodeStatus != 'M' {
		t.Fatalf("summaries=%#v error=%v", summaries, err)
	}
	var blame []client.BlameLine
	if err := instance.Blame(ctx, rootURL+"/imported/nested/script.sh", client.BlameOptions{
		InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 8}},
		Start:       svn.Revision{Kind: svn.RevisionNumber, Number: 6}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 8},
	}, func(line client.BlameLine) error {
		blame = append(blame, line)
		return nil
	}); err != nil || len(blame) != 3 || blame[0].Revision != 7 || blame[1].Revision != 8 || blame[2].Revision != 8 {
		t.Fatalf("blame=%#v error=%v", blame, err)
	}

	info, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionSetProperty, Path: "trunk/moved.txt", PropertyName: "svn:needs-lock", PropertyValue: []byte("*")},
		{Kind: client.ActionPut, Path: "trunk/translated.txt", Content: []byte("$Rev$\n")},
		{Kind: client.ActionSetProperty, Path: "trunk/translated.txt", PropertyName: "svn:keywords", PropertyValue: []byte("Rev")},
	}, client.MuccOptions{RevisionProperties: revprops("needs lock and translated file"), BaseRevision: 8})
	if err != nil || info.Revision != 9 {
		t.Fatalf("needs-lock info=%#v error=%v", info, err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL, working, 9, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	translatedFile := filepath.Join(working, "trunk", "translated.txt")
	var cleanSummary []client.DiffSummary
	if err := instance.DiffSummarize(ctx,
		client.DiffTarget{Target: translatedFile, Revision: svn.Revision{Kind: svn.RevisionBase}},
		client.DiffTarget{Target: translatedFile, Revision: svn.Revision{Kind: svn.RevisionWorking}},
		client.DiffOptions{}, func(summary client.DiffSummary) error {
			cleanSummary = append(cleanSummary, summary)
			return nil
		}); err != nil || len(cleanSummary) != 0 {
		t.Fatalf("clean translated diff=%#v error=%v", cleanSummary, err)
	}
	workingScript := filepath.Join(working, "imported", "nested", "script.sh")
	if err := os.WriteFile(workingScript, []byte("local modification\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	contents.Reset()
	if err := instance.Cat(ctx, workingScript, &contents, client.CatOptions{IgnoreKeywords: true}); err != nil || contents.String() != "first\nchanged\nthird\n" {
		t.Fatalf("default WC cat=%q error=%v", contents.String(), err)
	}
	contents.Reset()
	if err := instance.Cat(ctx, workingScript, &contents, client.CatOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionWorking}}, IgnoreKeywords: true}); err != nil || contents.String() != "local modification\n" {
		t.Fatalf("working WC cat=%q error=%v", contents.String(), err)
	}
	workingFile := filepath.Join(working, "trunk", "moved.txt")
	lock, err = instance.Lock(ctx, workingFile, client.LockOptions{Comment: "working lock"})
	if err != nil || lock == nil {
		t.Fatalf("working lock=%#v error=%v", lock, err)
	}
	workingInfo, err := instance.Info(ctx, workingFile, client.InfoOptions{})
	if err != nil || workingInfo.WorkingCopy == nil || workingInfo.WorkingCopy.Lock == nil || workingInfo.WorkingCopy.Lock.Token != lock.Token {
		t.Fatalf("working lock info=%#v error=%v", workingInfo, err)
	}
	if mode, err := os.Stat(workingFile); err != nil || mode.Mode().Perm()&0o200 == 0 {
		t.Fatalf("locked file mode=%v error=%v", mode, err)
	}
	if err := instance.Unlock(ctx, workingFile, false); err != nil {
		t.Fatal(err)
	}
	if mode, err := os.Stat(workingFile); err != nil || mode.Mode().Perm()&0o222 != 0 {
		t.Fatalf("unlocked file mode=%v error=%v", mode, err)
	}
	localhostRoot := strings.Replace(rootURL, "file://", "file://localhost", 1)
	if err := instance.Relocate(ctx, working, localhostRoot); err != nil {
		t.Fatal(err)
	}
	database, err := wc.Open(ctx, working, wc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if database.RepositoryRoot() != localhostRoot {
		t.Fatalf("relocated root=%q, want %q", database.RepositoryRoot(), localhostRoot)
	}

	info, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionSetProperty, Path: "", PropertyName: "custom:root", PropertyValue: []byte("root value")}}, client.MuccOptions{RevisionProperties: revprops("root property"), BaseRevision: 9})
	if err != nil || info.Revision != 10 {
		t.Fatalf("root property info=%#v error=%v", info, err)
	}
	properties, err = instance.PropList(ctx, rootURL, client.PropertyOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 10}}})
	if err != nil || string(properties["custom:root"]) != "root value" {
		t.Fatalf("root properties=%v error=%v", properties, err)
	}
	var rootSummary []client.DiffSummary
	if err := instance.DiffSummarize(ctx,
		client.DiffTarget{Target: rootURL, Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 9}},
		client.DiffTarget{Target: rootURL, Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 10}},
		client.DiffOptions{}, func(summary client.DiffSummary) error {
			rootSummary = append(rootSummary, summary)
			return nil
		}); err != nil || len(rootSummary) != 1 || rootSummary[0].Path != "." || !rootSummary[0].PropertiesChanged {
		t.Fatalf("root property diff=%#v error=%v", rootSummary, err)
	}
	info, err = instance.MkdirURL(ctx, rootURL, []string{"trunk/new/deep"}, true, revprops("parents"))
	if err != nil || info.Revision != 11 {
		t.Fatalf("parents info=%#v error=%v", info, err)
	}
	foundLocked, foundUnlocked, foundPostfix := false, false, false
	for _, event := range notifications {
		foundLocked = foundLocked || event.Action == notify.ActionLocked
		foundUnlocked = foundUnlocked || event.Action == notify.ActionUnlocked
		foundPostfix = foundPostfix || event.Action == notify.ActionCommitPostfixTxdelta
	}
	if !foundLocked || !foundUnlocked || !foundPostfix {
		t.Fatalf("notifications missing lock=%t unlock=%t commit=%t", foundLocked, foundUnlocked, foundPostfix)
	}
}

func TestImportMergesDirectoriesWithoutOverwritingFiles(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	revprops := func(message string) svn.Props { return svn.Props{"svn:log": []byte(message)} }
	if _, err := instance.MkdirURL(ctx, rootURL, []string{"existing"}, false, revprops("mkdir")); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, err := instance.Import(ctx, source, rootURL, "existing", client.ImportOptions{RevisionProperties: revprops("import"), Depth: svn.DepthInfinity}); err != nil || info.Revision != 2 {
		t.Fatalf("import info=%#v error=%v", info, err)
	}
	if _, err := instance.Import(ctx, source, rootURL, "existing", client.ImportOptions{RevisionProperties: revprops("duplicate"), Depth: svn.DepthInfinity, Force: true}); err == nil {
		t.Fatal("forced import overwrote an existing repository file")
	}
	var contents bytes.Buffer
	if err := instance.Cat(ctx, rootURL+"/existing/file", &contents, client.CatOptions{IgnoreKeywords: true}); err != nil || contents.String() != "original\n" {
		t.Fatalf("repository content=%q error=%v", contents.String(), err)
	}
	single := filepath.Join(t.TempDir(), "script.sh")
	if err := os.WriteFile(single, []byte("echo single\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := instance.Import(ctx, single, rootURL, "nested/script.sh", client.ImportOptions{
		RevisionProperties: revprops("single"), Parents: true,
		AutoProps: map[string]svn.Props{"*.sh": {"custom:script": []byte("yes"), "svn:mime-type": []byte("text/plain")}},
	})
	if err != nil || info.Revision != 3 {
		t.Fatalf("single import info=%#v error=%v", info, err)
	}
	properties, err := instance.PropList(ctx, rootURL+"/nested/script.sh", client.PropertyOptions{})
	if err != nil || string(properties["custom:script"]) != "yes" || runtime.GOOS != "windows" && len(properties["svn:executable"]) == 0 {
		t.Fatalf("single import properties=%v error=%v", properties, err)
	}
	info, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionDelete, Path: "nested/script.sh", Revision: 3},
		{Kind: client.ActionPut, Path: "nested/script.sh", Content: []byte("replacement\n")},
		{Kind: client.ActionSetProperty, Path: "nested/script.sh", PropertyName: "custom:replacement", PropertyValue: []byte("yes")},
	}, client.MuccOptions{RevisionProperties: revprops("replace"), BaseRevision: 3})
	if err != nil || info.Revision != 4 {
		t.Fatalf("replacement info=%#v error=%v", info, err)
	}
	contents.Reset()
	if err := instance.Cat(ctx, rootURL+"/nested/script.sh", &contents, client.CatOptions{IgnoreKeywords: true}); err != nil || contents.String() != "replacement\n" {
		t.Fatalf("replacement content=%q error=%v", contents.String(), err)
	}
}
