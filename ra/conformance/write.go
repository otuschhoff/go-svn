package conformance

import (
	"bytes"
	"context"
	"testing"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

type WriteFixture struct {
	Open    any
	RootURL string
}

func RunWrites(t *testing.T, fixture WriteFixture) {
	t.Helper()
	session := open(t, Fixture{Open: fixture.Open})
	defer session.Close()
	ctx := context.Background()
	large := make([]byte, 96*1024)
	for index := range large {
		large[index] = byte(index % 251)
	}

	var firstInfo *ra.CommitInfo
	commit(t, ctx, session, 0, svn.Props{"svn:log": []byte("write conformance add"), "custom:revision": []byte("one")}, nil, false, func(root delta.DirEditor) error {
		directory, err := root.AddDirectory(ctx, "project", nil)
		if err != nil {
			return err
		}
		if err := directory.ChangeProp(ctx, "custom:directory", []byte("set")); err != nil {
			return err
		}
		file, err := directory.AddFile(ctx, "project/data.bin", nil)
		if err != nil {
			return err
		}
		if err := file.ChangeProp(ctx, "custom:file", []byte("set")); err != nil {
			return err
		}
		if err := writeFulltext(ctx, file, nil, large); err != nil {
			return err
		}
		replace, err := directory.AddFile(ctx, "project/replace.txt", nil)
		if err != nil {
			return err
		}
		if err := writeFulltext(ctx, replace, nil, []byte("old")); err != nil {
			return err
		}
		return directory.Close(ctx)
	}, func(info *ra.CommitInfo) error { firstInfo = info; return nil })
	if firstInfo == nil || firstInfo.Revision != 1 || firstInfo.ReposRoot != fixture.RootURL {
		t.Fatalf("first commit info=%#v", firstInfo)
	}
	assertFile(t, ctx, session, "project/data.bin", 1, large)
	_, _, directoryProps, err := session.GetDir(ctx, "project", 1, 0)
	if err != nil || string(directoryProps["custom:directory"]) != "set" {
		t.Fatalf("directory props=%v error=%v", directoryProps, err)
	}

	commit(t, ctx, session, 1, svn.Props{"svn:log": []byte("write conformance changes")}, nil, false, func(root delta.DirEditor) error {
		directory, err := root.OpenDirectory(ctx, "project", 1)
		if err != nil {
			return err
		}
		copy, err := directory.AddFile(ctx, "project/moved.bin", &delta.CopySource{Path: fixture.RootURL + "/project/data.bin", Rev: 1})
		if err != nil {
			return err
		}
		if err := copy.Close(ctx, nil); err != nil {
			return err
		}
		if err := directory.DeleteEntry(ctx, "project/data.bin", 1); err != nil {
			return err
		}
		if err := directory.DeleteEntry(ctx, "project/replace.txt", 1); err != nil {
			return err
		}
		replace, err := directory.AddFile(ctx, "project/replace.txt", nil)
		if err != nil {
			return err
		}
		if err := writeFulltext(ctx, replace, nil, []byte("new")); err != nil {
			return err
		}
		return directory.Close(ctx)
	}, nil)
	assertFile(t, ctx, session, "project/moved.bin", 2, large)
	assertFile(t, ctx, session, "project/replace.txt", 2, []byte("new"))
	if kind, err := session.CheckPath(ctx, "project/data.bin", 2); err != nil || kind != svn.NodeNone {
		t.Fatalf("deleted path kind=%v error=%v", kind, err)
	}

	editor, err := session.GetCommitEditor(ctx, svn.Props{"svn:log": []byte("abort")}, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.AddFile(ctx, "aborted.txt", nil); err != nil {
		t.Fatal(err)
	}
	if err := editor.AbortEdit(ctx); err != nil {
		t.Fatal(err)
	}
	if latest, err := session.LatestRevision(ctx); err != nil || latest != 2 {
		t.Fatalf("latest after abort=%d error=%v", latest, err)
	}

	if err := session.ChangeRevProp(ctx, 2, "custom:cas", []byte("one"), nil, false); err != nil {
		t.Fatal(err)
	}
	if err := session.ChangeRevProp(ctx, 2, "custom:cas", []byte("two"), []byte("wrong"), false); err == nil {
		t.Fatal("atomic revision property mismatch succeeded")
	}
	if err := session.ChangeRevProp(ctx, 2, "custom:cas", nil, []byte("one"), false); err != nil {
		t.Fatal(err)
	}

	var lock *svn.Lock
	if err := session.Lock(ctx, map[string]svn.Revnum{"project/moved.bin": 2}, "conformance", false, func(_ string, value *svn.Lock, callbackErr error) error {
		lock = value
		return callbackErr
	}); err != nil || lock == nil {
		t.Fatalf("lock=%#v error=%v", lock, err)
	}
	commit(t, ctx, session, 2, svn.Props{"svn:log": []byte("keep lock")}, map[string]string{"project/moved.bin": lock.Token}, true, func(root delta.DirEditor) error {
		directory, err := root.OpenDirectory(ctx, "project", 2)
		if err != nil {
			return err
		}
		file, err := directory.OpenFile(ctx, "project/moved.bin", 2)
		if err != nil {
			return err
		}
		if err := writeFulltext(ctx, file, &svn.Checksum{Kind: svn.ChecksumMD5, Digest: svn.Sum(svn.ChecksumMD5, large).Digest}, []byte("locked")); err != nil {
			return err
		}
		return directory.Close(ctx)
	}, nil)
	if current, err := session.GetLock(ctx, "project/moved.bin"); err != nil || current == nil || current.Token != lock.Token {
		t.Fatalf("kept lock=%#v error=%v", current, err)
	}
	if err := session.Unlock(ctx, map[string]string{"project/moved.bin": lock.Token}, false, nil); err != nil {
		t.Fatal(err)
	}
	if current, err := session.GetLock(ctx, "project/moved.bin"); err != nil || current != nil {
		t.Fatalf("lock after unlock=%#v error=%v", current, err)
	}
}

func commit(t *testing.T, ctx context.Context, session ra.Session, base svn.Revnum, props svn.Props, tokens map[string]string, keepLocks bool, edit func(delta.DirEditor) error, callback func(*ra.CommitInfo) error) {
	t.Helper()
	editor, err := session.GetCommitEditor(ctx, props, tokens, keepLocks, callback)
	if err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(ctx, base)
	if err == nil {
		err = edit(root)
	}
	if err == nil {
		err = root.Close(ctx)
	}
	if err == nil {
		err = editor.CloseEdit(ctx)
	} else {
		_ = editor.AbortEdit(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func writeFulltext(ctx context.Context, file delta.FileEditor, base *svn.Checksum, content []byte) error {
	windows, err := file.ApplyTextDelta(ctx, base)
	if err != nil {
		return err
	}
	for offset := 0; offset < len(content); offset += 32 * 1024 {
		end := offset + 32*1024
		if end > len(content) {
			end = len(content)
		}
		chunk := content[offset:end]
		if err := windows.Window(&delta.Window{TargetLength: len(chunk), Ops: []delta.Op{{Kind: delta.OpNew, Length: len(chunk)}}, NewData: chunk}); err != nil {
			return err
		}
	}
	if err := windows.Close(); err != nil {
		return err
	}
	checksum := svn.Sum(svn.ChecksumMD5, content)
	return file.Close(ctx, &checksum)
}

func assertFile(t *testing.T, ctx context.Context, session ra.Session, name string, revision svn.Revnum, want []byte) {
	t.Helper()
	var content bytes.Buffer
	if _, _, err := session.GetFile(ctx, name, revision, &content, false); err != nil || !bytes.Equal(content.Bytes(), want) {
		t.Fatalf("file %s length=%d error=%v", name, content.Len(), err)
	}
}
