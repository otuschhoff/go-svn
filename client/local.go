package client

import (
	"context"

	"github.com/otuschhoff/go-svn/wc"
)

func (client *Client) Add(ctx context.Context, targetPath string, options wc.AddOptions) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.Add(ctx, targetPath, options) })
}

func (client *Client) Mkdir(ctx context.Context, targetPath string, parents bool) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.Mkdir(ctx, targetPath, parents) })
}

func (client *Client) Delete(ctx context.Context, targetPath string, options wc.DeleteOptions) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.Delete(ctx, targetPath, options) })
}

func (client *Client) Copy(ctx context.Context, sourcePath, destinationPath string) error {
	return withDatabase(ctx, sourcePath, func(database *wc.Database) error { return database.Copy(ctx, sourcePath, destinationPath) })
}

func (client *Client) Move(ctx context.Context, sourcePath, destinationPath string, force bool) error {
	return withDatabase(ctx, sourcePath, func(database *wc.Database) error { return database.Move(ctx, sourcePath, destinationPath, force) })
}

func (client *Client) SetProperty(ctx context.Context, targetPath, name string, value []byte, force bool) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.SetProperty(ctx, targetPath, name, value, force) })
}

func (client *Client) SetChangelist(ctx context.Context, targetPath, changelist string) error {
	return withDatabase(ctx, targetPath, func(database *wc.Database) error { return database.SetChangelist(ctx, targetPath, changelist) })
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
