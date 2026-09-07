package client_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

func TestWorkingCopyMergeinfoInheritanceAndElision(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"}, {Kind: client.ActionMkdir, Path: "trunk/child"},
		{Kind: client.ActionSetProperty, Path: "trunk", PropertyName: props.Mergeinfo, PropertyValue: []byte("/source:1-3,5*")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/trunk", working, 1, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(working, "child")
	inherited, err := instance.WorkingCopyMergeinfo(ctx, child, mergeinfo.InheritanceInherited)
	if err != nil {
		t.Fatal(err)
	}
	if got := mergeinfo.String(inherited.Mergeinfo); got != "/source/child:1-3" || !inherited.Inherited {
		t.Fatalf("inherited mergeinfo = %q, inherited=%v", got, inherited.Inherited)
	}
	nearest, err := instance.WorkingCopyMergeinfo(ctx, child, mergeinfo.InheritanceNearestAncestor)
	if err != nil {
		t.Fatal(err)
	}
	if got := mergeinfo.String(nearest.Mergeinfo); got != "/source/child:1-3,5*" {
		t.Fatalf("nearest mergeinfo = %q", got)
	}
	if err := instance.SetWorkingCopyMergeinfo(ctx, child, inherited.Mergeinfo, true); err != nil {
		t.Fatal(err)
	}
	database, err := wc.Open(ctx, working, wc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	info, err := database.Info(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.WorkingProperties[props.Mergeinfo]) != 0 {
		t.Fatalf("redundant explicit mergeinfo was not elided: %q", info.WorkingProperties[props.Mergeinfo])
	}
}
