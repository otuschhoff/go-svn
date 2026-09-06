//go:build integration

package client_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/auth"
	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/internal/testutil"
	"github.com/otuschhoff/go-svn/internal/testutil/servers"
	"github.com/otuschhoff/go-svn/ra"
	_ "github.com/otuschhoff/go-svn/radav"
	_ "github.com/otuschhoff/go-svn/rasvn"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
)

func TestProtocolClientOperations(t *testing.T) {
	testutil.SkipUnlessIntegration(t)
	t.Run("svn", func(t *testing.T) {
		repositoryPath := filepath.Join(t.TempDir(), "repository")
		if _, err := repos.Create(context.Background(), repositoryPath, repos.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		server := servers.StartSvnserve(t, repositoryPath, servers.SvnserveOptions{AnonymousAccess: servers.AccessWrite})
		defer server.Close()
		runProtocolClientOperations(t, server.URL, protocolClientCallbacks(server.Username, server.Password))
	})
	t.Run("dav", func(t *testing.T) {
		root := t.TempDir()
		if _, err := repos.Create(context.Background(), filepath.Join(root, "repository"), repos.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		server := servers.StartDockerServers(t, root, servers.DockerOptions{})
		defer server.Close()
		callbacks := protocolClientCallbacks(server.Username, server.Password)
		runProtocolClientOperations(t, server.HTTPURL+"repository", callbacks)
		if t.Failed() {
			t.Logf("Docker integration server output:\n%s", server.Output())
		}
	})
}

func runProtocolClientOperations(t *testing.T, rootURL string, callbacks *ra.Callbacks) {
	t.Helper()
	ctx := context.Background()
	instance := client.New(callbacks)
	revprops := func(message string) svn.Props {
		return svn.Props{"svn:log": []byte(message)}
	}
	if info, err := instance.MkdirURL(ctx, rootURL, []string{"trunk"}, false, revprops("mkdir")); err != nil || info.Revision != 1 {
		t.Fatalf("mkdir info=%#v error=%v", info, err)
	}
	if info, err := instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionPut, Path: "trunk/file", Content: []byte("one\ntwo\n")}}, client.MuccOptions{RevisionProperties: revprops("put"), BaseRevision: 1}); err != nil || info.Revision != 2 {
		t.Fatalf("put info=%#v error=%v", info, err)
	}
	var contents bytes.Buffer
	if err := instance.Cat(ctx, rootURL+"/trunk/file", &contents, client.CatOptions{IgnoreKeywords: true}); err != nil || contents.String() != "one\ntwo\n" {
		t.Fatalf("cat=%q error=%v", contents.String(), err)
	}
	if info, err := instance.CopyURL(ctx, rootURL, rootURL+"/trunk/file", "trunk/copied", 2, svn.NodeFile, revprops("copy")); err != nil || info.Revision != 3 {
		t.Fatalf("copy info=%#v error=%v", info, err)
	}
	if info, err := instance.MoveURL(ctx, rootURL, rootURL+"/trunk/copied", "trunk/moved", 3, svn.NodeFile, revprops("move")); err != nil || info.Revision != 4 {
		t.Fatalf("move info=%#v error=%v", info, err)
	}
	if info, err := instance.Mucc(ctx, rootURL, []client.Action{{Kind: client.ActionPut, Path: "trunk/file", Content: []byte("one\nchanged\n")}}, client.MuccOptions{RevisionProperties: revprops("modify"), BaseRevision: 4}); err != nil || info.Revision != 5 {
		t.Fatalf("modify info=%#v error=%v", info, err)
	}
	left := client.DiffTarget{Target: rootURL + "/trunk/file", Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 2}}
	right := client.DiffTarget{Target: rootURL + "/trunk/file", Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 5}}
	var difference bytes.Buffer
	if err := instance.Diff(ctx, left, right, &difference, client.DiffOptions{}); err != nil || !bytes.Contains(difference.Bytes(), []byte("-two\n+changed\n")) {
		t.Fatalf("diff=%q error=%v", difference.String(), err)
	}
	var blame []client.BlameLine
	if err := instance.Blame(ctx, rootURL+"/trunk/file", client.BlameOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 5}}, Start: svn.Revision{Kind: svn.RevisionNumber, Number: 2}, End: svn.Revision{Kind: svn.RevisionNumber, Number: 5}}, func(line client.BlameLine) error {
		blame = append(blame, line)
		return nil
	}); err != nil || len(blame) != 2 || blame[0].Revision != 2 || blame[1].Revision != 5 {
		t.Fatalf("blame=%#v error=%v", blame, err)
	}
	if info, err := instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionDelete, Path: "trunk/moved", Revision: 5},
		{Kind: client.ActionPut, Path: "trunk/moved", Content: []byte("replacement\n")},
	}, client.MuccOptions{RevisionProperties: revprops("replace"), BaseRevision: 5}); err != nil || info.Revision != 6 {
		t.Fatalf("replace info=%#v error=%v", info, err)
	}
	contents.Reset()
	if err := instance.Cat(ctx, rootURL+"/trunk/moved", &contents, client.CatOptions{IgnoreKeywords: true}); err != nil || contents.String() != "replacement\n" {
		t.Fatalf("replacement cat=%q error=%v", contents.String(), err)
	}
	lock, err := instance.Lock(ctx, rootURL+"/trunk/file", client.LockOptions{Comment: "protocol"})
	if err != nil || lock == nil {
		t.Fatalf("lock=%#v error=%v", lock, err)
	}
	if err := instance.Unlock(ctx, rootURL+"/trunk/file", false); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "export")
	if _, err := instance.Export(ctx, rootURL+"/trunk", destination, client.ExportOptions{InfoOptions: client.InfoOptions{Revision: svn.Revision{Kind: svn.RevisionNumber, Number: 5}}}); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(destination, "file")); err != nil || string(content) != "one\nchanged\n" {
		t.Fatalf("export=%q error=%v", content, err)
	}
}

func protocolClientCallbacks(username, password string) *ra.Callbacks {
	return &ra.Callbacks{Auth: auth.Baton{Prompt: auth.Prompt{
		Simple: func(context.Context, string, string, bool) (*auth.Credentials, error) {
			return &auth.Credentials{Username: username, Password: password}, nil
		},
		Username: func(context.Context, string, string, bool) (*auth.Credentials, error) {
			return &auth.Credentials{Username: username}, nil
		},
	}}}
}
