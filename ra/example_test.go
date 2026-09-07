package ra_test

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/otuschhoff/go-svn/ra"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	"github.com/otuschhoff/go-svn/repos"
)

func ExampleOpen() {
	ctx := context.Background()
	directory, err := os.MkdirTemp("", "go-svn-example-")
	if err != nil {
		return
	}
	defer os.RemoveAll(directory)
	repository, err := repos.Create(ctx, directory+"/repository", repos.CreateOptions{})
	if err != nil {
		return
	}
	repositoryURL := (&url.URL{Scheme: "file", Path: repository.Path()}).String()
	session, _, err := ra.Open(ctx, repositoryURL, nil)
	if err != nil {
		return
	}
	defer session.Close()
	revision, err := session.LatestRevision(ctx)
	if err != nil {
		return
	}
	fmt.Println(revision)
	// Output: 0
}
