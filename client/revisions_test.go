package client_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/client"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

func TestInfoResolvesURLAndWorkingCopyRevisions(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := "file://" + repository.Path()
	editor, err := repository.GetCommitEditor(ctx, repos.CommitOptions{
		RepositoryURL: rootURL,
		Properties:    svn.Props{"svn:author": []byte("alice"), "svn:log": []byte("seed")},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.ChangeProp(ctx, "custom:root", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := editor.CloseEdit(ctx); err != nil {
		t.Fatal(err)
	}

	instance := client.New(nil)
	urlInfo, err := instance.Info(ctx, rootURL+"@1", client.InfoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if urlInfo.URL != rootURL || urlInfo.Revision != 1 || urlInfo.Kind != svn.NodeDir || urlInfo.ChangedAuthor != "alice" || !urlInfo.HasProperties {
		t.Fatalf("URL info = %#v", urlInfo)
	}

	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL, working, 1, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	wcInfo, err := instance.Info(ctx, working, client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionBase}})
	if err != nil {
		t.Fatal(err)
	}
	if wcInfo.WorkingCopy == nil || wcInfo.URL != rootURL || wcInfo.Revision != 1 || wcInfo.RepositoryUUID != urlInfo.RepositoryUUID {
		t.Fatalf("working-copy info = %#v", wcInfo)
	}
}

func TestInfoTracesImplicitPegAcrossMove(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := "file://" + repository.Path()
	instance := client.New(nil)
	revprops := func(message string) svn.Props { return svn.Props{"svn:log": []byte(message)} }
	if _, err := instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"},
		{Kind: client.ActionPut, Path: "trunk/old", Content: []byte("content\n")},
	}, client.MuccOptions{RevisionProperties: revprops("seed")}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.MoveURL(ctx, rootURL, rootURL+"/trunk/old", "trunk/new", 1, svn.NodeFile, revprops("move")); err != nil {
		t.Fatal(err)
	}
	oldURL := rootURL + "/trunk/old"
	urlInfo, err := instance.Info(ctx, rootURL+"/trunk/new", client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 1}})
	if err != nil || urlInfo.URL != oldURL || urlInfo.Revision != 1 {
		t.Fatalf("historical URL info=%#v error=%v", urlInfo, err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/trunk", working, 2, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	wcInfo, err := instance.Info(ctx, filepath.Join(working, "new"), client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionPrevious}})
	if err != nil || wcInfo.URL != oldURL || wcInfo.Revision != 1 {
		t.Fatalf("historical WC info=%#v error=%v", wcInfo, err)
	}
}
