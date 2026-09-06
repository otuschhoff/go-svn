package repos

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

type CommitOptions struct {
	BasePath      string
	RepositoryURL string
	Properties    svn.Props
	LockTokens    map[string]string
	KeepLocks     bool
	Callback      func(*ra.CommitInfo) error
}

type commitEditor struct {
	repository  *Repository
	transaction fs.Txn
	root        fs.TxnRoot
	options     CommitOptions
	base        svn.Revnum
	done        bool
}

type commitDir struct {
	editor   *commitEditor
	nodePath string
}

type commitFile struct {
	editor   *commitEditor
	nodePath string
}

func (repository *Repository) GetCommitEditor(ctx context.Context, options CommitOptions) (delta.Editor, error) {
	base, err := repository.Youngest(ctx)
	if err != nil {
		return nil, err
	}
	transaction, err := repository.fs.BeginTxn(ctx, base)
	if err != nil {
		return nil, err
	}
	for name, value := range options.Properties {
		if err := transaction.ChangeProperty(ctx, name, value); err != nil {
			_ = transaction.Abort(ctx)
			return nil, err
		}
	}
	root, err := transaction.Root(ctx)
	if err != nil {
		_ = transaction.Abort(ctx)
		return nil, err
	}
	author := string(options.Properties["svn:author"])
	if _, err := repository.RunHook(ctx, "start-commit", []string{repository.path, author, "", transaction.Name()}, nil); err != nil {
		_ = transaction.Abort(ctx)
		return nil, err
	}
	return &commitEditor{repository: repository, transaction: transaction, root: root, options: options, base: base}, nil
}

func (*commitEditor) SetTargetRevision(context.Context, svn.Revnum) error { return nil }

func (editor *commitEditor) OpenRoot(ctx context.Context, revision svn.Revnum) (delta.DirEditor, error) {
	if editor.done || !revision.IsValid() || revision > editor.base {
		return nil, fmt.Errorf("%w: invalid commit root revision", svn.ErrIncorrectParams)
	}
	basePath := cleanCommitPath(editor.options.BasePath)
	kind, err := editor.root.CheckPath(ctx, basePath)
	if err != nil {
		return nil, err
	}
	if kind != svn.NodeDir {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, basePath)
	}
	return &commitDir{editor: editor, nodePath: basePath}, nil
}

func (editor *commitEditor) CloseEdit(ctx context.Context) error {
	if editor.done {
		return fmt.Errorf("%w: commit editor complete", svn.ErrIncorrectParams)
	}
	editor.done = true
	if _, err := editor.repository.RunHook(ctx, "pre-commit", []string{editor.repository.path, editor.transaction.Name()}, nil); err != nil {
		_ = editor.transaction.Abort(ctx)
		return err
	}
	revision, err := editor.transaction.Commit(ctx, editor.options.LockTokens, editor.options.KeepLocks)
	if err != nil {
		return err
	}
	_, postCommitErr := editor.repository.RunHook(ctx, "post-commit", []string{editor.repository.path, fmt.Sprint(revision), editor.transaction.Name()}, nil)
	if editor.options.Callback == nil {
		return nil
	}
	properties, err := editor.repository.RevisionProps(ctx, revision)
	if err != nil {
		return err
	}
	date, _ := revisionDate(properties)
	info := &ra.CommitInfo{Revision: revision, Date: date, Author: string(properties["svn:author"]), ReposRoot: editor.options.RepositoryURL}
	if postCommitErr != nil {
		info.PostCommitError = postCommitErr.Error()
	}
	return editor.options.Callback(info)
}

func (editor *commitEditor) AbortEdit(ctx context.Context) error {
	if editor.done {
		return nil
	}
	editor.done = true
	return editor.transaction.Abort(ctx)
}

func (directory *commitDir) DeleteEntry(ctx context.Context, name string, _ svn.Revnum) error {
	return directory.editor.root.Delete(ctx, directory.editor.fullPath(name))
}

func (directory *commitDir) AddDirectory(ctx context.Context, name string, source *delta.CopySource) (delta.DirEditor, error) {
	nodePath := directory.editor.fullPath(name)
	var err error
	if source == nil {
		err = directory.editor.root.MakeDir(ctx, nodePath)
	} else {
		sourcePath, pathErr := directory.editor.copySourcePath(source.Path)
		if pathErr != nil {
			return nil, pathErr
		}
		err = directory.editor.root.Copy(ctx, source.Rev, sourcePath, nodePath)
	}
	if err != nil {
		return nil, err
	}
	return &commitDir{editor: directory.editor, nodePath: nodePath}, nil
}

func (directory *commitDir) OpenDirectory(ctx context.Context, name string, _ svn.Revnum) (delta.DirEditor, error) {
	nodePath := directory.editor.fullPath(name)
	kind, err := directory.editor.root.CheckPath(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	if kind != svn.NodeDir {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, nodePath)
	}
	return &commitDir{editor: directory.editor, nodePath: nodePath}, nil
}

func (directory *commitDir) ChangeProp(ctx context.Context, name string, value []byte) error {
	return directory.editor.root.ChangeNodeProp(ctx, directory.nodePath, name, value)
}

func (*commitDir) AbsentDirectory(context.Context, string) error { return nil }

func (directory *commitDir) AddFile(ctx context.Context, name string, source *delta.CopySource) (delta.FileEditor, error) {
	nodePath := directory.editor.fullPath(name)
	var err error
	if source == nil {
		err = directory.editor.root.MakeFile(ctx, nodePath)
	} else {
		sourcePath, pathErr := directory.editor.copySourcePath(source.Path)
		if pathErr != nil {
			return nil, pathErr
		}
		err = directory.editor.root.Copy(ctx, source.Rev, sourcePath, nodePath)
	}
	if err != nil {
		return nil, err
	}
	return &commitFile{editor: directory.editor, nodePath: nodePath}, nil
}

func (directory *commitDir) OpenFile(ctx context.Context, name string, _ svn.Revnum) (delta.FileEditor, error) {
	nodePath := directory.editor.fullPath(name)
	kind, err := directory.editor.root.CheckPath(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	if kind != svn.NodeFile {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotFile, nodePath)
	}
	return &commitFile{editor: directory.editor, nodePath: nodePath}, nil
}

func (*commitDir) AbsentFile(context.Context, string) error { return nil }
func (*commitDir) Close(context.Context) error              { return nil }

func (file *commitFile) ApplyTextDelta(ctx context.Context, checksum *svn.Checksum) (delta.WindowHandler, error) {
	return file.editor.root.ApplyTextDelta(ctx, file.nodePath, checksum)
}

func (file *commitFile) ChangeProp(ctx context.Context, name string, value []byte) error {
	return file.editor.root.ChangeNodeProp(ctx, file.nodePath, name, value)
}

func (file *commitFile) Close(ctx context.Context, checksum *svn.Checksum) error {
	if checksum == nil {
		return nil
	}
	var content bytes.Buffer
	if err := file.editor.root.FileContents(ctx, file.nodePath, &content); err != nil {
		return err
	}
	if !svn.Sum(checksum.Kind, content.Bytes()).Equal(*checksum) {
		return fmt.Errorf("%w: result checksum", svn.ErrChecksumMismatch)
	}
	return nil
}

func (editor *commitEditor) fullPath(name string) string {
	return cleanCommitPath(path.Join(editor.options.BasePath, name))
}

func (editor *commitEditor) copySourcePath(value string) (string, error) {
	root := strings.TrimSuffix(editor.options.RepositoryURL, "/")
	if value == root {
		return "/", nil
	}
	if !strings.HasPrefix(value, root+"/") {
		return "", fmt.Errorf("%w: copy source is outside repository", svn.ErrRAIllegalURL)
	}
	return cleanCommitPath(strings.TrimPrefix(value, root)), nil
}

func cleanCommitPath(value string) string {
	return path.Clean("/" + strings.TrimPrefix(value, "/"))
}

var _ delta.Editor = (*commitEditor)(nil)
