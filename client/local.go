package client

import (
	"context"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
	"github.com/otuschhoff/go-svn/wc"
)

func (client *Client) Add(ctx context.Context, targetPath string, options wc.AddOptions) error {
	err := withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.Add(ctx, targetPath, options) })
	if err == nil {
		client.notify(notify.Notify{Action: notify.ActionCommitAdded, Path: targetPath})
	}
	return err
}

func (client *Client) Mkdir(ctx context.Context, targetPath string, parents bool) error {
	err := withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.Mkdir(ctx, targetPath, parents) })
	if err == nil {
		client.notify(notify.Notify{Action: notify.ActionCommitAdded, Path: targetPath, Kind: svn.NodeDir})
	}
	return err
}

func (client *Client) Delete(ctx context.Context, targetPath string, options wc.DeleteOptions) error {
	err := withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.Delete(ctx, targetPath, options) })
	if err == nil {
		client.notify(notify.Notify{Action: notify.ActionCommitDeleted, Path: targetPath})
	}
	return err
}

func (client *Client) Copy(ctx context.Context, sourcePath, destinationPath string) error {
	err := withDatabase(ctx, sourcePath, func(database *wc.Database) error { return database.Copy(ctx, sourcePath, destinationPath) })
	if err == nil {
		client.notify(notify.Notify{Action: notify.ActionCommitAdded, Path: destinationPath})
	}
	return err
}

func (client *Client) Move(ctx context.Context, sourcePath, destinationPath string, force bool) error {
	err := withDatabase(ctx, sourcePath, func(database *wc.Database) error { return database.Move(ctx, sourcePath, destinationPath, force) })
	if err == nil {
		client.notify(notify.Notify{Action: notify.ActionCommitDeleted, Path: sourcePath})
		client.notify(notify.Notify{Action: notify.ActionCommitAdded, Path: destinationPath})
	}
	return err
}

func (client *Client) SetProperty(ctx context.Context, targetPath, name string, value []byte, force bool) error {
	err := withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.SetProperty(ctx, targetPath, name, value, force) })
	if err == nil {
		action := notify.ActionPropertyModified
		if value == nil {
			action = notify.ActionPropertyDeleted
		}
		client.notify(notify.Notify{Action: action, Path: targetPath})
	}
	return err
}

func (client *Client) SetChangelist(ctx context.Context, targetPath, changelist string) error {
	err := withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.SetChangelist(ctx, targetPath, changelist) })
	if err == nil {
		action := notify.ActionChangelistSet
		if changelist == "" {
			action = notify.ActionChangelistClear
		}
		client.notify(notify.Notify{Action: action, Path: targetPath})
	}
	return err
}

func (client *Client) Revert(ctx context.Context, targetPath string, options wc.RevertOptions) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.Revert(ctx, targetPath, options) })
}

func (client *Client) Resolve(ctx context.Context, targetPath string) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.Resolve(ctx, targetPath) })
}

func (client *Client) ResolveWithChoice(ctx context.Context, targetPath string, choice wc.ConflictChoice) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error {
		return database.ResolveWithChoice(ctx, targetPath, choice)
	})
}

func (client *Client) Cleanup(ctx context.Context, targetPath string) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.Cleanup(ctx) })
}

func (client *Client) CleanupWithOptions(ctx context.Context, targetPath string, options wc.CleanupOptions) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.CleanupWithOptions(ctx, options) })
}

func withDatabase(ctx context.Context, targetPath string, operation func(*wc.Database) error) error {
	database, err := wc.Open(ctx, targetPath, wc.Options{Writable: true})
	if err != nil {
		return err
	}
	defer database.Close()
	return operation(database)
}
