package client_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

func TestCheckoutProcessesDirectoryExternals(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"},
		{Kind: client.ActionMkdir, Path: "library"},
		{Kind: client.ActionPut, Path: "library/file", Content: []byte("external\n")},
		{Kind: client.ActionSetProperty, Path: "trunk", PropertyName: "svn:externals", PropertyValue: []byte("-r1 ^/library@1 vendor/library\n-r1 ^/library/file@1 external-file\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}

	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/trunk", working, 1, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(working, "vendor", "library", "file"))
	if err != nil || string(contents) != "external\n" {
		t.Fatalf("external contents = %q, error = %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(working, "vendor", "library", ".svn", "wc.db")); err != nil {
		t.Fatalf("external is not a nested working copy: %v", err)
	}
	fileInfo, err := instance.Info(ctx, filepath.Join(working, "external-file"), client.InfoOptions{})
	if err != nil || fileInfo.WorkingCopy == nil || !fileInfo.WorkingCopy.FileExternal {
		t.Fatalf("file external info = %#v, error = %v", fileInfo, err)
	}
	contents, err = os.ReadFile(filepath.Join(working, "external-file"))
	if err != nil || string(contents) != "external\n" {
		t.Fatalf("file external contents = %q, error = %v", contents, err)
	}

	ignored := filepath.Join(t.TempDir(), "ignored")
	if _, err := instance.Checkout(ctx, rootURL+"/trunk", ignored, 1, wc.UpdateOptions{Depth: svn.DepthInfinity, IgnoreExternals: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ignored, "vendor", "library")); !os.IsNotExist(err) {
		t.Fatalf("ignored external exists: %v", err)
	}

	if _, err := instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionSetProperty, Path: "trunk", PropertyName: "svn:externals"}}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("remove externals")}, BaseRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Update(ctx, working, 2, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join("vendor", "library"), "external-file"} {
		if _, err := os.Stat(filepath.Join(working, name)); !os.IsNotExist(err) {
			t.Fatalf("removed external %s remains: %v", name, err)
		}
	}
}

func TestExportProcessesAndIgnoresExternals(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"}, {Kind: client.ActionMkdir, Path: "library"},
		{Kind: client.ActionPut, Path: "library/file", Content: []byte("external\n")},
		{Kind: client.ActionSetProperty, Path: "trunk", PropertyName: "svn:externals", PropertyValue: []byte("^/library vendor\n^/library/file file-external\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "export")
	if _, err := instance.Export(ctx, rootURL+"/trunk", destination, client.ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join("vendor", "file"), "file-external"} {
		contents, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || string(contents) != "external\n" {
			t.Fatalf("exported external %s = %q, error = %v", name, contents, err)
		}
	}
	ignored := filepath.Join(t.TempDir(), "ignored")
	if _, err := instance.Export(ctx, rootURL+"/trunk", ignored, client.ExportOptions{IgnoreExternals: true}); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(ignored); err != nil || len(entries) != 0 {
		t.Fatalf("ignored export entries = %v, error = %v", entries, err)
	}
}

func TestCommitIncludesDirectoryExternal(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"}, {Kind: client.ActionMkdir, Path: "library"},
		{Kind: client.ActionPut, Path: "library/file", Content: []byte("one\n")},
		{Kind: client.ActionSetProperty, Path: "trunk", PropertyName: "svn:externals", PropertyValue: []byte("^/library vendor\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/trunk", working, 1, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(working, "vendor", "file"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Commit(ctx, working, wc.CommitOptions{Depth: svn.DepthInfinity, IncludeExternals: true, RevisionProperties: svn.Props{"svn:log": []byte("external change")}}); err != nil {
		t.Fatal(err)
	}
	var contents bytes.Buffer
	if err := instance.Cat(ctx, rootURL+"/library/file", &contents, client.CatOptions{IgnoreKeywords: true}); err != nil || contents.String() != "two\n" {
		t.Fatalf("committed external = %q, error = %v", contents.String(), err)
	}
}
