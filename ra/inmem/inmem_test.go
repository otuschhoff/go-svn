package inmem_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/ra/conformance"
	"github.com/otuschhoff/go-svn/ra/inmem"
	"github.com/otuschhoff/go-svn/svn"
)

func TestConformance(t *testing.T) {
	const rootURL = "memory://fixture"
	const uuid = "9a160628-1f61-42df-a452-d2ecbd30690f"
	repository := inmem.NewRepository(rootURL, uuid)
	first := inmem.Directory(map[string]*inmem.Node{"trunk": withProps(inmem.Directory(map[string]*inmem.Node{
		"README.txt": withProps(inmem.File([]byte("first\n")), svn.Props{"svn:eol-style": []byte("LF")}),
		"old.txt":    inmem.File([]byte("old\n")),
	}), svn.Props{"svn:mergeinfo": []byte("/branches:1-2"), "custom:inherited": []byte("yes")})})
	repository.AddRevision(inmem.Revision{Root: first, Time: time.Unix(100, 0), Props: svn.Props{"svn:author": []byte("alice"), "svn:log": []byte("initial")}, Changes: []svn.ChangedPath{{Path: "/trunk", Action: svn.LogAdded, NodeKind: svn.NodeDir}}})
	second := inmem.Directory(map[string]*inmem.Node{"trunk": withProps(inmem.Directory(map[string]*inmem.Node{
		"README.txt": withProps(inmem.File([]byte("first\nsecond\n")), svn.Props{"svn:eol-style": []byte("LF")}),
	}), svn.Props{"svn:mergeinfo": []byte("/branches:1-2"), "custom:inherited": []byte("yes")})})
	repository.AddRevision(inmem.Revision{Root: second, Time: time.Unix(200, 0), Props: svn.Props{"svn:author": []byte("bob"), "svn:log": []byte("update")}, Changes: []svn.ChangedPath{{Path: "/trunk/README.txt", Action: svn.LogModified, NodeKind: svn.NodeFile}, {Path: "/trunk/old.txt", Action: svn.LogDeleted, NodeKind: svn.NodeFile}}})

	conformance.Run(t, conformance.Fixture{Open: func(t *testing.T) interfaceSession {
		session, err := repository.Open(rootURL)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}, RootURL: rootURL, UUID: uuid, Latest: 2, FilePath: "trunk/README.txt", OldContent: "first\n", Content: "first\nsecond\n", Deleted: "trunk/old.txt",
		LatestTime: time.Unix(200, 0), RevProp: "svn:author", RevValue: "bob",
		MergePath: "trunk", InheritKey: "custom:inherited", InheritVal: "yes"})
}

func TestWriteConformance(t *testing.T) {
	const rootURL = "memory://write-conformance"
	repository := inmem.NewRepository(rootURL, "write-uuid")
	conformance.RunWrites(t, conformance.WriteFixture{
		Open: func(t *testing.T) interfaceSession {
			session, err := repository.Open(rootURL)
			if err != nil {
				t.Fatal(err)
			}
			return session
		},
		RootURL: rootURL,
	})
}

type interfaceSession = interface {
	URL() string
}

func withProps(node *inmem.Node, props svn.Props) *inmem.Node { node.Props = props; return node }

func TestWrites(t *testing.T) {
	ctx := context.Background()
	repository := inmem.NewRepository("memory://writes", "uuid")
	repository.AddRevision(inmem.Revision{Root: inmem.Directory(map[string]*inmem.Node{
		"trunk": inmem.Directory(map[string]*inmem.Node{"base.txt": inmem.File([]byte("base"))}),
	}), Props: svn.Props{"svn:author": []byte("alice")}})
	session, err := repository.Open("memory://writes")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var info *ra.CommitInfo
	editor, err := session.GetCommitEditor(ctx, svn.Props{"svn:author": []byte("bob"), "svn:log": []byte("write")}, nil, false, func(value *ra.CommitInfo) error {
		info = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	trunk, err := root.OpenDirectory(ctx, "trunk", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := trunk.ChangeProp(ctx, "custom:dir", []byte("yes")); err != nil {
		t.Fatal(err)
	}
	file, err := trunk.AddFile(ctx, "trunk/new.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	windows, err := file.ApplyTextDelta(ctx, &svn.EmptyMD5)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"hello ", "world"} {
		if err := windows.Window(&delta.Window{TargetLength: len(value), Ops: []delta.Op{{Kind: delta.OpNew, Length: len(value)}}, NewData: []byte(value)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := windows.Close(); err != nil {
		t.Fatal(err)
	}
	wantChecksum := svn.Sum(svn.ChecksumMD5, []byte("hello world"))
	if err := file.Close(ctx, &wantChecksum); err != nil {
		t.Fatal(err)
	}
	copy, err := trunk.AddFile(ctx, "trunk/copy.txt", &delta.CopySource{Path: "trunk/base.txt", Rev: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := copy.Close(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := trunk.DeleteEntry(ctx, "trunk/base.txt", 1); err != nil {
		t.Fatal(err)
	}
	if err := trunk.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := editor.CloseEdit(ctx); err != nil {
		t.Fatal(err)
	}
	if info == nil || info.Revision != 2 || info.Author != "bob" || info.ReposRoot != "memory://writes" {
		t.Fatalf("commit info=%#v", info)
	}
	var content bytes.Buffer
	if _, _, err := session.GetFile(ctx, "trunk/new.txt", 2, &content, false); err != nil || content.String() != "hello world" {
		t.Fatalf("new content=%q error=%v", content.String(), err)
	}
	content.Reset()
	if _, _, err := session.GetFile(ctx, "trunk/copy.txt", 2, &content, false); err != nil || content.String() != "base" {
		t.Fatalf("copy content=%q error=%v", content.String(), err)
	}
	if _, _, err := session.GetFile(ctx, "trunk/base.txt", 2, io.Discard, false); !errors.Is(err, svn.ErrFSNotFound) {
		t.Fatalf("deleted file error=%v", err)
	}

	if err := session.ChangeRevProp(ctx, 2, "custom:p", []byte("one"), nil, false); err != nil {
		t.Fatal(err)
	}
	if err := session.ChangeRevProp(ctx, 2, "custom:p", []byte("two"), []byte("wrong"), false); !errors.Is(err, svn.ErrFSPropBasevalueMismatch) {
		t.Fatalf("CAS mismatch error=%v", err)
	}
	if err := session.ChangeRevProp(ctx, 2, "custom:p", nil, []byte("one"), false); err != nil {
		t.Fatal(err)
	}

	aborted, err := session.GetCommitEditor(ctx, svn.Props{"svn:log": []byte("abort")}, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aborted.OpenRoot(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if err := aborted.AbortEdit(ctx); err != nil {
		t.Fatal(err)
	}
	if latest, err := session.LatestRevision(ctx); err != nil || latest != 2 {
		t.Fatalf("latest after abort=%d error=%v", latest, err)
	}
}

func TestCommitConsumesLock(t *testing.T) {
	ctx := context.Background()
	repository := inmem.NewRepository("memory://locks", "uuid")
	repository.AddRevision(inmem.Revision{Root: inmem.Directory(map[string]*inmem.Node{"file": inmem.File([]byte("old"))})})
	session, err := repository.Open("memory://locks")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var lock *svn.Lock
	if err := session.Lock(ctx, map[string]svn.Revnum{"file": 1}, "", false, func(_ string, value *svn.Lock, callbackErr error) error {
		lock = value
		return callbackErr
	}); err != nil {
		t.Fatal(err)
	}
	editor, err := session.GetCommitEditor(ctx, svn.Props{"svn:log": []byte("locked")}, map[string]string{"file": lock.Token}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile(ctx, "file", 1)
	if err != nil {
		t.Fatal(err)
	}
	windows, err := file.ApplyTextDelta(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.Window(&delta.Window{TargetLength: 3, Ops: []delta.Op{{Kind: delta.OpNew, Length: 3}}, NewData: []byte("new")}); err != nil {
		t.Fatal(err)
	}
	if err := windows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := editor.CloseEdit(ctx); err != nil {
		t.Fatal(err)
	}
	locks, err := session.GetLocks(ctx, "", svn.DepthInfinity)
	if err != nil || len(locks) != 0 {
		t.Fatalf("locks=%v error=%v", locks, err)
	}
}
