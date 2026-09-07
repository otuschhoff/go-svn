//go:build integration

package client_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/internal/testutil"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

func TestDiffMatchesReference(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	revprops := func(message string) svn.Props { return svn.Props{"svn:log": []byte(message)} }
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"},
		{Kind: client.ActionPut, Path: "trunk/all-space", Content: []byte("ab\n")},
		{Kind: client.ActionPut, Path: "trunk/deleted", Content: []byte("old\n")},
		{Kind: client.ActionPut, Path: "trunk/eol", Content: []byte("one\ntwo\n")},
		{Kind: client.ActionPut, Path: "trunk/modified", Content: []byte("one\ntwo\nthree\nfour\nbefore\nsix\nseven\neight\nnine\n")},
		{Kind: client.ActionPut, Path: "trunk/space-change", Content: []byte("a b\n")},
	}, client.MuccOptions{RevisionProperties: revprops("seed")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionPut, Path: "trunk/all-space", Content: []byte("a b\n")},
		{Kind: client.ActionPut, Path: "trunk/added", Content: []byte("new\n")},
		{Kind: client.ActionCopy, Path: "trunk/copied", Source: rootURL + "/trunk/modified", Revision: 1, NodeKind: svn.NodeFile},
		{Kind: client.ActionDelete, Path: "trunk/deleted", Revision: 1},
		{Kind: client.ActionPut, Path: "trunk/eol", Content: []byte("one\r\ntwo\r\n")},
		{Kind: client.ActionPut, Path: "trunk/modified", Content: []byte("one\ntwo\nthree\nfour\nafter\nsix\nseven\neight\nnine\n")},
		{Kind: client.ActionSetProperty, Path: "trunk/modified", PropertyName: "custom:p", PropertyValue: []byte("alpha\nbeta")},
		{Kind: client.ActionPut, Path: "trunk/space-change", Content: []byte("a  b\n")},
	}, client.MuccOptions{RevisionProperties: revprops("change"), BaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	svnTool := testutil.RequireTool(t, "svn", "GOSVN_SVN")
	cases := []struct {
		name      string
		arguments []string
		options   client.DiffOptions
	}{
		{name: "default"},
		{name: "properties-only", arguments: []string{"--properties-only"}, options: client.DiffOptions{PropertiesOnly: true}},
		{name: "ignore-properties", arguments: []string{"--ignore-properties"}, options: client.DiffOptions{IgnoreProperties: true}},
		{name: "no-diff-added", arguments: []string{"--no-diff-added"}, options: client.DiffOptions{NoDiffAdded: true}},
		{name: "no-diff-deleted", arguments: []string{"--no-diff-deleted"}, options: client.DiffOptions{NoDiffDeleted: true}},
		{name: "git", arguments: []string{"--git"}, options: client.DiffOptions{Git: true}},
		{name: "patch-compatible", arguments: []string{"--patch-compatible"}, options: client.DiffOptions{PatchCompatible: true}},
		{name: "show-copies-as-adds", arguments: []string{"--show-copies-as-adds"}, options: client.DiffOptions{ShowCopiesAsAdds: true}},
		{name: "context-zero", arguments: []string{"-x", "-U0"}, options: client.DiffOptions{ContextSet: true, ContextLines: 0}},
		{name: "context-one", arguments: []string{"-x", "-U1"}, options: client.DiffOptions{ContextSet: true, ContextLines: 1}},
		{name: "ignore-space-change", arguments: []string{"-x", "-b"}, options: client.DiffOptions{IgnoreSpaceChange: true}},
		{name: "ignore-all-space", arguments: []string{"-x", "-w"}, options: client.DiffOptions{IgnoreAllSpace: true}},
		{name: "ignore-eol", arguments: []string{"-x", "--ignore-eol-style"}, options: client.DiffOptions{IgnoreEOL: true}},
		{name: "no-added-or-deleted", arguments: []string{"--no-diff-added", "--no-diff-deleted"}, options: client.DiffOptions{NoDiffAdded: true, NoDiffDeleted: true}},
		{name: "context-two", arguments: []string{"-x", "-U2"}, options: client.DiffOptions{ContextSet: true, ContextLines: 2}},
		{name: "context-four", arguments: []string{"-x", "-U4"}, options: client.DiffOptions{ContextSet: true, ContextLines: 4}},
		{name: "context-eight", arguments: []string{"-x", "-U8"}, options: client.DiffOptions{ContextSet: true, ContextLines: 8}},
		{name: "ignore-space-and-eol", arguments: []string{"-x", "-b --ignore-eol-style"}, options: client.DiffOptions{IgnoreSpaceChange: true, IgnoreEOL: true}},
		{name: "ignore-all-space-and-eol", arguments: []string{"-x", "-w --ignore-eol-style"}, options: client.DiffOptions{IgnoreAllSpace: true, IgnoreEOL: true}},
		{name: "git-properties-only", arguments: []string{"--git", "--properties-only"}, options: client.DiffOptions{Git: true, PropertiesOnly: true}},
		{name: "git-ignore-properties", arguments: []string{"--git", "--ignore-properties"}, options: client.DiffOptions{Git: true, IgnoreProperties: true}},
		{name: "git-no-diff-added", arguments: []string{"--git", "--no-diff-added"}, options: client.DiffOptions{Git: true, NoDiffAdded: true}},
		{name: "git-no-diff-deleted", arguments: []string{"--git", "--no-diff-deleted"}, options: client.DiffOptions{Git: true, NoDiffDeleted: true}},
		{name: "git-context-zero", arguments: []string{"--git", "-x", "-U0"}, options: client.DiffOptions{Git: true, ContextSet: true, ContextLines: 0}},
		{name: "git-context-one", arguments: []string{"--git", "-x", "-U1"}, options: client.DiffOptions{Git: true, ContextSet: true, ContextLines: 1}},
		{name: "git-ignore-space-change", arguments: []string{"--git", "-x", "-b"}, options: client.DiffOptions{Git: true, IgnoreSpaceChange: true}},
		{name: "git-ignore-all-space", arguments: []string{"--git", "-x", "-w"}, options: client.DiffOptions{Git: true, IgnoreAllSpace: true}},
		{name: "git-ignore-eol", arguments: []string{"--git", "-x", "--ignore-eol-style"}, options: client.DiffOptions{Git: true, IgnoreEOL: true}},
		{name: "properties-only-no-added", arguments: []string{"--properties-only", "--no-diff-added"}, options: client.DiffOptions{PropertiesOnly: true, NoDiffAdded: true}},
		{name: "properties-only-no-deleted", arguments: []string{"--properties-only", "--no-diff-deleted"}, options: client.DiffOptions{PropertiesOnly: true, NoDiffDeleted: true}},
		{name: "ignore-properties-no-added", arguments: []string{"--ignore-properties", "--no-diff-added"}, options: client.DiffOptions{IgnoreProperties: true, NoDiffAdded: true}},
		{name: "ignore-properties-no-deleted", arguments: []string{"--ignore-properties", "--no-diff-deleted"}, options: client.DiffOptions{IgnoreProperties: true, NoDiffDeleted: true}},
		{name: "patch-compatible-context-zero", arguments: []string{"--patch-compatible", "-x", "-U0"}, options: client.DiffOptions{PatchCompatible: true, ContextSet: true, ContextLines: 0}},
		{name: "patch-compatible-ignore-space", arguments: []string{"--patch-compatible", "-x", "-w"}, options: client.DiffOptions{PatchCompatible: true, IgnoreAllSpace: true}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			options := testCase.options
			options.Depth = svn.DepthInfinity
			var actual bytes.Buffer
			if err := instance.Diff(ctx,
				client.DiffTarget{Target: rootURL + "/trunk", Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 1}},
				client.DiffTarget{Target: rootURL + "/trunk", Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 2}},
				&actual, options); err != nil {
				t.Fatal(err)
			}
			arguments := append([]string{"diff"}, testCase.arguments...)
			arguments = append(arguments, "--old", rootURL+"/trunk@1", "--new", rootURL+"/trunk@2")
			expected, err := exec.Command(svnTool, arguments...).CombinedOutput()
			if err != nil {
				t.Fatalf("reference svn diff: %v\n%s", err, expected)
			}
			if !bytes.Equal(actual.Bytes(), expected) {
				t.Fatalf("go-svn diff:\n%s\nreference diff:\n%s", actual.Bytes(), expected)
			}
		})
	}
}

func TestWorkingCopyDiffMatchesReference(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	_, err = instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"},
		{Kind: client.ActionPut, Path: "trunk/copied-source", Content: []byte("copied\n")},
		{Kind: client.ActionPut, Path: "trunk/deleted", Content: []byte("deleted\n")},
		{Kind: client.ActionPut, Path: "trunk/modified", Content: []byte("before\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{"svn:log": []byte("seed")}})
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "working")
	if _, err := instance.Checkout(ctx, rootURL+"/trunk", working, 1, wc.UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	if err := instance.Copy(ctx, filepath.Join(working, "copied-source"), filepath.Join(working, "copied")); err != nil {
		t.Fatal(err)
	}
	if err := instance.Delete(ctx, filepath.Join(working, "deleted"), wc.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(working, "modified"), []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := instance.SetProperty(ctx, filepath.Join(working, "modified"), "custom:p", []byte("value"), false); err != nil {
		t.Fatal(err)
	}
	var actual bytes.Buffer
	if err := instance.Diff(ctx,
		client.DiffTarget{Target: working, Revision: svn.Revision{Kind: svn.RevisionBase}},
		client.DiffTarget{Target: working, Revision: svn.Revision{Kind: svn.RevisionWorking}},
		&actual, client.DiffOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	svnTool := testutil.RequireTool(t, "svn", "GOSVN_SVN")
	expected, err := exec.Command(svnTool, "diff", working).CombinedOutput()
	if err != nil {
		t.Fatalf("reference svn diff: %v\n%s", err, expected)
	}
	if !bytes.Equal(actual.Bytes(), expected) {
		t.Fatalf("go-svn diff:\n%s\nreference diff:\n%s", actual.Bytes(), expected)
	}
}
