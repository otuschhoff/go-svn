package client_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

func ExampleClient_Checkout() {
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
	repositoryURL := repository.URL()
	svnClient := client.New(nil)
	_, err = svnClient.Mucc(ctx, repositoryURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"},
		{Kind: client.ActionPut, Path: "trunk/readme.txt", Content: []byte("hello\n")},
	}, client.MuccOptions{RevisionProperties: svn.Props{props.Log: []byte("seed")}})
	if err != nil {
		return
	}
	destination := filepath.Join(directory, "project")
	revision, err := svnClient.Checkout(ctx, repositoryURL+"/trunk", destination, svn.InvalidRevnum, wc.UpdateOptions{
		Depth: svn.DepthInfinity,
	})
	if err != nil {
		return
	}
	contents, err := os.ReadFile(filepath.Join(destination, "readme.txt"))
	if err != nil {
		return
	}
	fmt.Printf("r%d: %s", revision, contents)
	// Output: r1: hello
}
