package client

import (
	"context"

	"github.com/otuschhoff/go-svn/ra"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	_ "github.com/otuschhoff/go-svn/radav"
	_ "github.com/otuschhoff/go-svn/rasvn"
	"github.com/otuschhoff/go-svn/svn/notify"
	"github.com/otuschhoff/go-svn/wc"
)

type Client struct {
	Callbacks *ra.Callbacks
}

func New(callbacks *ra.Callbacks) *Client { return &Client{Callbacks: callbacks} }

func (client *Client) notify(event notify.Notify) {
	if client.Callbacks != nil && client.Callbacks.Notify != nil {
		client.Callbacks.Notify(event)
	}
}

func (client *Client) openWorkingCopy(ctx context.Context, targetPath string) (*wc.Database, ra.Session, error) {
	database, err := wc.Open(ctx, targetPath, wc.Options{Writable: true})
	if err != nil {
		return nil, nil, err
	}
	info, err := database.Info(ctx, database.RootPath())
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	session, _, err := ra.Open(ctx, info.URL, client.Callbacks)
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	return database, session, nil
}
