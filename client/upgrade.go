package client

import (
	"context"

	"github.com/otuschhoff/go-svn/wc"
)

func (client *Client) Upgrade(ctx context.Context, targetPath string) error {
	database, err := wc.Open(ctx, targetPath, wc.Options{Writable: true})
	if err != nil {
		return err
	}
	return database.Close()
}
