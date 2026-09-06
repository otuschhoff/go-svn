package client

import (
	"context"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/wc"
)

func (client *Client) Commit(ctx context.Context, targetPath string, options wc.CommitOptions) (*ra.CommitInfo, error) {
	database, session, err := client.openWorkingCopy(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	defer session.Close()
	return database.Commit(ctx, session, targetPath, options)
}
