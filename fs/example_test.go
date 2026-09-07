package fs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/otuschhoff/go-svn/fs"
	_ "github.com/otuschhoff/go-svn/fs/fsfs"
	"github.com/otuschhoff/go-svn/repos"
)

func ExampleOpen() {
	ctx := context.Background()
	directory, err := os.MkdirTemp("", "go-svn-example-")
	if err != nil {
		return
	}
	defer os.RemoveAll(directory)
	repository, err := repos.Create(ctx, filepath.Join(directory, "repository"), repos.CreateOptions{})
	if err != nil {
		return
	}
	filesystem, err := fs.Open(ctx, repository.Path())
	if err != nil {
		return
	}
	revision, err := filesystem.YoungestRevision(ctx)
	if err != nil {
		return
	}
	fmt.Println(revision)
	// Output: 0
}
