package client

import (
	"context"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

func (client *Client) Checkout(ctx context.Context, repositoryURL, destination string, revision svn.Revnum, options wc.UpdateOptions) (svn.Revnum, error) {
	session, _, err := ra.Open(ctx, repositoryURL, client.Callbacks)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	defer session.Close()
	database, err := wc.Checkout(ctx, session, destination, revision, options)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	defer database.Close()
	info, err := database.Info(ctx, destination)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	return info.Revision, nil
}

func (client *Client) Update(ctx context.Context, targetPath string, revision svn.Revnum, options wc.UpdateOptions) (svn.Revnum, error) {
	database, session, err := client.openWorkingCopy(ctx, targetPath)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	defer database.Close()
	defer session.Close()
	return database.Update(ctx, session, targetPath, revision, options)
}

func (client *Client) Switch(ctx context.Context, targetPath, switchURL string, revision svn.Revnum, options wc.UpdateOptions) (svn.Revnum, error) {
	database, session, err := client.openWorkingCopy(ctx, targetPath)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	defer database.Close()
	defer session.Close()
	return database.Switch(ctx, session, targetPath, switchURL, revision, options)
}
