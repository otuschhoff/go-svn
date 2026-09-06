package repos

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

func TestCommitHooks(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell is not installed")
	}
	t.Run("pre-commit veto removes transaction", func(t *testing.T) {
		repositoryPath := filepath.Join(t.TempDir(), "repository")
		repository, err := Create(context.Background(), repositoryPath, CreateOptions{Format: 8})
		if err != nil {
			t.Fatal(err)
		}
		writeHook(t, repositoryPath, "pre-commit", "#!/bin/sh\necho rejected >&2\nexit 1\n")
		err = commitHookFile(repository, nil)
		if err == nil || !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("commit error = %v", err)
		}
		entries, err := os.ReadDir(filepath.Join(repositoryPath, "db", "transactions"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("transactions after veto = %v, error = %v", entries, err)
		}
	})

	t.Run("arguments environment and post warning", func(t *testing.T) {
		repositoryPath := filepath.Join(t.TempDir(), "repository")
		repository, err := Create(context.Background(), repositoryPath, CreateOptions{Format: 8})
		if err != nil {
			t.Fatal(err)
		}
		environment := "[default]\nHOOK_DEFAULT = inherited\nHOOK_OVERRIDE = default\n[ start-commit ]\nHOOK_OVERRIDE = selected\n"
		if err := os.WriteFile(filepath.Join(repositoryPath, "conf", "hooks-env"), []byte(environment), 0o666); err != nil {
			t.Fatal(err)
		}
		startOutput := filepath.Join(repositoryPath, "start-hook-output")
		writeHook(t, repositoryPath, "start-commit", "#!/bin/sh\nprintf '%s|%s|%s|%s|%s|%s' \"$#\" \"$1\" \"$2\" \"$4\" \"$HOOK_DEFAULT\" \"$HOOK_OVERRIDE\" > \"$1/start-hook-output\"\n")
		writeHook(t, repositoryPath, "post-commit", "#!/bin/sh\necho post-warning >&2\nexit 1\n")
		var info *ra.CommitInfo
		if err := commitHookFile(repository, func(value *ra.CommitInfo) error { info = value; return nil }); err != nil {
			t.Fatal(err)
		}
		output, err := os.ReadFile(startOutput)
		if err != nil {
			t.Fatal(err)
		}
		if fields := strings.Split(string(output), "|"); len(fields) != 6 || fields[0] != "4" || fields[1] != repositoryPath || fields[2] != "author" || fields[4] != "inherited" || fields[5] != "selected" {
			t.Fatalf("start-commit output = %q", output)
		}
		if info == nil || !strings.Contains(info.PostCommitError, "post-warning") {
			t.Fatalf("commit info = %#v", info)
		}
		if youngest, err := repository.Youngest(context.Background()); err != nil || youngest != 1 {
			t.Fatalf("youngest = %d, error = %v", youngest, err)
		}
	})
}

func commitHookFile(repository *Repository, callback func(*ra.CommitInfo) error) error {
	ctx := context.Background()
	editor, err := repository.GetCommitEditor(ctx, CommitOptions{RepositoryURL: "file://" + repository.Path(), Properties: svn.Props{"svn:author": []byte("author"), "svn:log": []byte("hook test")}, Callback: callback})
	if err != nil {
		return err
	}
	root, err := editor.OpenRoot(ctx, 0)
	if err != nil {
		return err
	}
	file, err := root.AddFile(ctx, "file", nil)
	if err != nil {
		return err
	}
	handler, err := file.ApplyTextDelta(ctx, nil)
	if err != nil {
		return err
	}
	content := []byte("hook content\n")
	if err := handler.Window(&delta.Window{TargetLength: len(content), Ops: []delta.Op{{Kind: delta.OpNew, Length: len(content)}}, NewData: content}); err != nil {
		return err
	}
	if err := handler.Close(); err != nil {
		return err
	}
	if err := file.Close(ctx, nil); err != nil {
		return err
	}
	if err := root.Close(ctx); err != nil {
		return err
	}
	return editor.CloseEdit(ctx)
}

func writeHook(t *testing.T, repositoryPath, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repositoryPath, "hooks", name), []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
}
