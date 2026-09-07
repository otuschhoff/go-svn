package client_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
	"github.com/otuschhoff/go-svn/wc"
)

func TestMergeRangeAppliesChangesAndRecordsMergeinfo(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "source"}, {Kind: client.ActionMkdir, Path: "target"},
		{Kind: client.ActionPut, Path: "source/edited", Content: []byte("one\n")}, {Kind: client.ActionPut, Path: "target/edited", Content: []byte("one\n")},
		{Kind: client.ActionPut, Path: "source/deleted", Content: []byte("gone\n")}, {Kind: client.ActionPut, Path: "target/deleted", Content: []byte("gone\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionPut, Path: "source/edited", Content: []byte("two\n")},
		{Kind: client.ActionDelete, Path: "source/deleted", Revision: 1, NodeKind: svn.NodeFile},
		{Kind: client.ActionPut, Path: "source/added", Content: []byte("new\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("change")}, BaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/target", working, 2, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	ranges := []svn.RevisionRange{{Start: svn.Revision{Kind: svn.RevisionNumber, Number: 1}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}}
	if err := instance.Merge(ctx, rootURL+"/source", working, client.MergeOptions{Ranges: ranges, Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"edited": "two\n", "added": "new\n"} {
		contents, err := os.ReadFile(filepath.Join(working, name))
		if err != nil || string(contents) != want {
			t.Fatalf("%s = %q, error = %v", name, contents, err)
		}
	}
	if _, err := os.Stat(filepath.Join(working, "deleted")); !os.IsNotExist(err) {
		t.Fatalf("deleted path remains: %v", err)
	}
	database, err := wc.Open(ctx, working, wc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	info, err := database.Info(ctx, working)
	if err != nil || !strings.Contains(string(info.WorkingProperties[props.Mergeinfo]), "/source:2") {
		t.Fatalf("mergeinfo = %q, error = %v", info.WorkingProperties[props.Mergeinfo], err)
	}
}

func TestMergeRecordsAndResolvesTextConflict(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "source"}, {Kind: client.ActionMkdir, Path: "target"},
		{Kind: client.ActionPut, Path: "source/file", Content: []byte("base\n")},
		{Kind: client.ActionPut, Path: "target/file", Content: []byte("base\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionPut, Path: "source/file", Content: []byte("theirs\n")}}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("change")}, BaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/target", working, 2, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(working, "file")
	if err := os.WriteFile(file, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ranges := []svn.RevisionRange{{Start: svn.Revision{Kind: svn.RevisionNumber, Number: 1}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}}
	if err := instance.Merge(ctx, rootURL+"/source", working, client.MergeOptions{Ranges: ranges, Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	description, err := instance.DescribeConflict(ctx, file)
	if err != nil || description == nil || !description.Text {
		t.Fatalf("conflict = %#v, error = %v", description, err)
	}
	resolved := 0
	if err := instance.ResolveConflicts(ctx, working, svn.DepthInfinity, func(_ context.Context, conflict client.ConflictDescription) (wc.ConflictChoice, error) {
		resolved++
		return wc.ConflictTheirsConflict, nil
	}); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("resolved conflicts = %d", resolved)
	}
	contents, err := os.ReadFile(file)
	if err != nil || string(contents) != "theirs\n" {
		t.Fatalf("resolved contents = %q, error = %v", contents, err)
	}
}

func TestMergeRecordOnlyDryRunAndReverse(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "source"}, {Kind: client.ActionMkdir, Path: "record"}, {Kind: client.ActionMkdir, Path: "dry"}, {Kind: client.ActionMkdir, Path: "reverse"},
		{Kind: client.ActionPut, Path: "source/file", Content: []byte("one\n")},
		{Kind: client.ActionPut, Path: "record/file", Content: []byte("one\n")},
		{Kind: client.ActionPut, Path: "dry/file", Content: []byte("one\n")},
		{Kind: client.ActionPut, Path: "reverse/file", Content: []byte("one\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionPut, Path: "source/file", Content: []byte("two\n")}, {Kind: client.ActionPut, Path: "reverse/file", Content: []byte("two\n")}}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("change")}, BaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	forward := []svn.RevisionRange{{Start: svn.Revision{Kind: svn.RevisionNumber, Number: 1}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}}
	reverse := []svn.RevisionRange{{Start: svn.Revision{Kind: svn.RevisionNumber, Number: 2}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 1}}}
	for name, options := range map[string]client.MergeOptions{
		"record":  {Ranges: forward, Depth: svn.DepthInfinity, RecordOnly: true},
		"dry":     {Ranges: forward, Depth: svn.DepthInfinity, DryRun: true},
		"reverse": {Ranges: reverse, Depth: svn.DepthInfinity},
	} {
		working := filepath.Join(t.TempDir(), name)
		if _, err := instance.Checkout(ctx, rootURL+"/"+name, working, 2, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
			t.Fatal(err)
		}
		if err := instance.Merge(ctx, rootURL+"/source", working, options); err != nil {
			t.Fatalf("%s merge: %v", name, err)
		}
		contents, err := os.ReadFile(filepath.Join(working, "file"))
		if err != nil || string(contents) != "one\n" {
			t.Fatalf("%s contents = %q, error = %v", name, contents, err)
		}
		database, err := wc.Open(ctx, working, wc.Options{})
		if err != nil {
			t.Fatal(err)
		}
		info, infoErr := database.Info(ctx, working)
		database.Close()
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		hasMergeinfo := len(info.WorkingProperties[props.Mergeinfo]) != 0
		if name == "record" && !hasMergeinfo || name == "dry" && hasMergeinfo {
			t.Fatalf("%s mergeinfo = %q", name, info.WorkingProperties[props.Mergeinfo])
		}
	}
}

func TestMergeTwoSources(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "left"}, {Kind: client.ActionMkdir, Path: "right"}, {Kind: client.ActionMkdir, Path: "target"},
		{Kind: client.ActionPut, Path: "left/file", Content: []byte("left\n")},
		{Kind: client.ActionPut, Path: "right/file", Content: []byte("right\n")},
		{Kind: client.ActionPut, Path: "right/added", Content: []byte("added\n")},
		{Kind: client.ActionPut, Path: "target/file", Content: []byte("left\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/target", working, 1, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	revision := svn.Revision{Kind: svn.RevisionNumber, Number: 1}
	if err := instance.MergeTwoSources(ctx,
		client.DiffTarget{Target: rootURL + "/left", Revision: revision},
		client.DiffTarget{Target: rootURL + "/right", Revision: revision}, working,
		client.MergeOptions{Depth: svn.DepthInfinity}); err == nil {
		t.Fatal("unrelated two-source merge succeeded without IgnoreAncestry")
	}
	if err := instance.MergeTwoSources(ctx,
		client.DiffTarget{Target: rootURL + "/left", Revision: revision},
		client.DiffTarget{Target: rootURL + "/right", Revision: revision}, working,
		client.MergeOptions{Depth: svn.DepthInfinity, IgnoreAncestry: true}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"file": "right\n", "added": "added\n"} {
		contents, err := os.ReadFile(filepath.Join(working, name))
		if err != nil || string(contents) != want {
			t.Fatalf("%s = %q, error = %v", name, contents, err)
		}
	}
}

func TestMergeEmitsNotifications(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionMkdir, Path: "source"}, {Kind: client.ActionMkdir, Path: "target"}, {Kind: client.ActionPut, Path: "source/file", Content: []byte("one\n")}, {Kind: client.ActionPut, Path: "target/file", Content: []byte("one\n")}}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionPut, Path: "source/file", Content: []byte("two\n")}}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("change")}, BaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/target", working, 2, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	var actions []notify.Action
	options := client.MergeOptions{Depth: svn.DepthInfinity, Ranges: []svn.RevisionRange{{Start: svn.Revision{Kind: svn.RevisionNumber, Number: 1}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}}, Notify: func(event notify.Notify) { actions = append(actions, event.Action) }}
	if err := instance.Merge(ctx, rootURL+"/source", working, options); err != nil {
		t.Fatal(err)
	}
	if len(actions) < 2 || actions[0] != notify.ActionMergeBegin || actions[1] != notify.ActionUpdateUpdate {
		t.Fatalf("merge actions = %v", actions)
	}
}

func TestAutomaticMergeUsesCommonHistoryAndEligibleRevisions(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"},
		{Kind: client.ActionPut, Path: "trunk/file", Content: []byte("base\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionCopy, Path: "branch", Source: rootURL + "/trunk", Revision: 1, NodeKind: svn.NodeDir}}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("branch")}, BaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionPut, Path: "branch/file", Content: []byte("changed\n")}}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("change")}, BaseRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/trunk", working, 3, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Merge(ctx, rootURL+"/branch", working, client.MergeOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(working, "file"))
	if err != nil || string(contents) != "changed\n" {
		t.Fatalf("automatic merge contents = %q, error = %v", contents, err)
	}
}

func TestMergeRecordsPropertyConflict(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "source"}, {Kind: client.ActionMkdir, Path: "target"},
		{Kind: client.ActionPut, Path: "source/file", Content: []byte("text\n")}, {Kind: client.ActionPut, Path: "target/file", Content: []byte("text\n")},
		{Kind: client.ActionSetProperty, Path: "source/file", PropertyName: "custom", PropertyValue: []byte("base")},
		{Kind: client.ActionSetProperty, Path: "target/file", PropertyName: "custom", PropertyValue: []byte("base")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionSetProperty, Path: "source/file", PropertyName: "custom", PropertyValue: []byte("theirs")}}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("change")}, BaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/target", working, 2, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(working, "file")
	if err := instance.SetProperty(ctx, file, "custom", []byte("mine"), false); err != nil {
		t.Fatal(err)
	}
	ranges := []svn.RevisionRange{{Start: svn.Revision{Kind: svn.RevisionNumber, Number: 1}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}}
	if err := instance.Merge(ctx, rootURL+"/source", working, client.MergeOptions{Ranges: ranges, Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	description, err := instance.DescribeConflict(ctx, file)
	if err != nil || description == nil || !description.Property {
		t.Fatalf("property conflict = %#v, error = %v", description, err)
	}
}

func TestMergeRecordsTreeConflictForIncomingDelete(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "source"}, {Kind: client.ActionMkdir, Path: "target"},
		{Kind: client.ActionPut, Path: "source/file", Content: []byte("base\n")}, {Kind: client.ActionPut, Path: "target/file", Content: []byte("base\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionDelete, Path: "source/file", Revision: 1, NodeKind: svn.NodeFile}}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("delete")}, BaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/target", working, 2, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(working, "file")
	if err := os.WriteFile(file, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ranges := []svn.RevisionRange{{Start: svn.Revision{Kind: svn.RevisionNumber, Number: 1}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}}
	if err := instance.Merge(ctx, rootURL+"/source", working, client.MergeOptions{Ranges: ranges, Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	description, err := instance.DescribeConflict(ctx, file)
	if err != nil || description == nil || !description.Tree || description.LocalChange != "edited" || description.IncomingChange != "deleted" {
		t.Fatalf("tree conflict = %#v, error = %v", description, err)
	}
	contents, err := os.ReadFile(file)
	if err != nil || string(contents) != "local\n" {
		t.Fatalf("local contents = %q, error = %v", contents, err)
	}
}
