package client

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

func (client *Client) Commit(ctx context.Context, targetPath string, options wc.CommitOptions) (*ra.CommitInfo, error) {
	database, session, err := client.openWorkingCopy(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	defer session.Close()
	var externalCommit *ra.CommitInfo
	if options.IncludeExternals {
		externals, err := database.Externals(ctx)
		if err != nil {
			return nil, err
		}
		externalOptions := options
		externalOptions.IncludeExternals = false
		for _, external := range externals {
			if external.Kind != svn.NodeDir {
				continue
			}
			externalPath := filepath.Join(database.RootPath(), filepath.FromSlash(external.LocalPath))
			committed, err := client.Commit(ctx, externalPath, externalOptions)
			if err != nil {
				return nil, err
			}
			if committed != nil {
				externalCommit = committed
			}
		}
	}
	committed, err := database.Commit(ctx, session, targetPath, options)
	if err != nil && externalCommit != nil && errors.Is(err, svn.ErrClientNoVersionedParent) {
		return externalCommit, nil
	}
	return committed, err
}
