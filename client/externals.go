package client

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

func (client *Client) processExternals(ctx context.Context, database *wc.Database, options wc.UpdateOptions, visited map[string]bool) error {
	externals, err := database.Externals(ctx)
	if err != nil {
		return err
	}
	for _, external := range externals {
		if external.DefinitionReposPath == "" {
			continue
		}
		destination := filepath.Join(database.RootPath(), filepath.FromSlash(external.LocalPath))
		repositoryURL, err := resolveExternalURL(database.RepositoryRoot(), external.DefinitionReposPath)
		if err != nil {
			return err
		}
		key := repositoryURL + "\x00" + destination
		if visited[key] {
			return fmt.Errorf("%w: recursive external %s", svn.ErrClientCycleDetected, destination)
		}
		visited[key] = true
		if err := client.updateExternal(ctx, database, external, repositoryURL, destination, options, visited); err != nil {
			return err
		}
		delete(visited, key)
	}
	return nil
}

func (client *Client) reconcileExternals(ctx context.Context, database *wc.Database, previous []wc.External, options wc.UpdateOptions) error {
	current, err := database.Externals(ctx)
	if err != nil {
		return err
	}
	present := make(map[string]bool, len(current))
	for _, external := range current {
		present[external.LocalPath] = true
	}
	for _, external := range previous {
		if !present[external.LocalPath] {
			if err := database.RemoveExternal(ctx, external, options.Force); err != nil {
				return err
			}
		}
	}
	return client.processExternals(ctx, database, options, make(map[string]bool))
}

func (client *Client) updateExternal(ctx context.Context, parent *wc.Database, external wc.External, repositoryURL, destination string, options wc.UpdateOptions, visited map[string]bool) error {
	operative := external.Operative
	if operative.Kind == svn.RevisionUnspecified {
		operative = svn.Revision{Kind: svn.RevisionHead}
	}
	target, err := client.resolveTarget(ctx, repositoryURL, InfoOptions{PegRevision: external.Peg, Revision: operative})
	if err != nil {
		return err
	}
	defer target.close()
	revision := target.revision
	kind, err := target.session.CheckPath(ctx, target.path, revision)
	if err != nil {
		return err
	}
	if kind == svn.NodeFile {
		var contents bytes.Buffer
		_, properties, err := target.session.GetFile(ctx, target.path, revision, &contents, true)
		if err != nil {
			return err
		}
		entry, err := target.session.Stat(ctx, target.path, revision)
		if err != nil {
			return err
		}
		if entry == nil {
			return fmt.Errorf("%w: %s", svn.ErrFSNotFound, repositoryURL)
		}
		uuid, err := target.session.UUID(ctx)
		if err != nil {
			return err
		}
		repositoryPath := strings.TrimPrefix(strings.TrimSuffix(target.url, "/"), strings.TrimSuffix(target.repositoryRoot, "/"))
		return parent.InstallFileExternal(ctx, destination, target.repositoryRoot, uuid, repositoryPath, revision, properties, contents.Bytes(), entry.CreatedRev, entry.Time, entry.LastAuthor, options.Force)
	}
	if kind != svn.NodeDir {
		return fmt.Errorf("%w: %s", svn.ErrFSNotFound, repositoryURL)
	}
	return client.updateDirectoryExternal(ctx, target.url, destination, revision, options, visited)
}

func resolveExternalURL(repositoryRoot, location string) (string, error) {
	root, err := url.Parse(repositoryRoot)
	if err != nil {
		return "", err
	}
	switch {
	case strings.Contains(location, "://"):
		return location, nil
	case strings.HasPrefix(location, "//"):
		return root.Scheme + ":" + location, nil
	case strings.HasPrefix(location, "/"):
		return root.Scheme + "://" + root.Host + path.Clean(location), nil
	default:
		return joinRepositoryURL(repositoryRoot, location), nil
	}
}

func (client *Client) updateDirectoryExternal(ctx context.Context, repositoryURL, destination string, revision svn.Revnum, options wc.UpdateOptions, visited map[string]bool) error {
	session, _, err := ra.Open(ctx, repositoryURL, client.Callbacks)
	if err != nil {
		return err
	}
	defer session.Close()
	externalOptions := options
	externalOptions.SetDepth = nil
	externalOptions.Depth = svn.DepthInfinity
	externalOptions.Notify = client.combineNotify(options.Notify)
	databasePath := filepath.Join(destination, ".svn", "wc.db")
	if _, err := os.Stat(databasePath); err == nil {
		database, err := wc.Open(ctx, destination, wc.Options{Writable: true})
		if err != nil {
			return err
		}
		defer database.Close()
		info, err := database.Info(ctx, destination)
		if err != nil {
			return err
		}
		if info.URL != repositoryURL {
			if _, err := database.Switch(ctx, session, destination, repositoryURL, revision, externalOptions); err != nil {
				return err
			}
		} else if _, err := database.Update(ctx, session, destination, revision, externalOptions); err != nil {
			return err
		}
		return client.processExternals(ctx, database, externalOptions, visited)
	} else if !os.IsNotExist(err) {
		return err
	}
	database, err := wc.Checkout(ctx, session, destination, revision, externalOptions)
	if err != nil {
		return err
	}
	defer database.Close()
	return client.processExternals(ctx, database, externalOptions, visited)
}
