package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

type ExportOptions struct {
	InfoOptions
	Force           bool
	IgnoreKeywords  bool
	NativeEOL       string
	IgnoreExternals bool
}

func (client *Client) Export(ctx context.Context, source, destination string, options ExportOptions) (svn.Revnum, error) {
	target, err := client.resolveTarget(ctx, source, options.InfoOptions)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	defer target.close()
	if target.workingInfo != nil && (options.Revision.Kind == svn.RevisionUnspecified || options.Revision.Kind == svn.RevisionWorking) {
		var excluded map[string]bool
		if options.IgnoreExternals {
			excluded = make(map[string]bool)
			externals, err := target.workingCopy.Externals(ctx)
			if err != nil {
				return svn.InvalidRevnum, err
			}
			for _, external := range externals {
				excluded[filepath.Clean(filepath.FromSlash(external.LocalPath))] = true
			}
		}
		return target.revision, exportWorkingTree(target.workingInfo.Path, destination, options.Force, excluded)
	}
	kind, err := target.session.CheckPath(ctx, target.path, target.revision)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	if kind == svn.NodeNone {
		return svn.InvalidRevnum, fmt.Errorf("%w: %s", svn.ErrFSNotFound, source)
	}
	if err := client.exportRepositoryNode(ctx, target, "", destination, kind, options, make(map[string]bool)); err != nil {
		return svn.InvalidRevnum, err
	}
	return target.revision, nil
}

func (client *Client) exportRepositoryNode(ctx context.Context, target *resolvedTarget, name, destination string, kind svn.NodeKind, options ExportOptions, visited map[string]bool) error {
	repositoryPath := path.Join(target.path, name)
	if kind == svn.NodeDir {
		if err := makeExportDirectory(destination, options.Force); err != nil {
			return err
		}
		entries, _, properties, err := target.session.GetDir(ctx, repositoryPath, target.revision, svn.DirentKind)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := client.exportRepositoryNode(ctx, target, path.Join(name, entry.Path), filepath.Join(destination, filepath.FromSlash(entry.Path)), entry.Kind, options, visited); err != nil {
				return err
			}
		}
		if !options.IgnoreExternals && len(properties[props.Externals]) != 0 {
			definitions, err := wc.ParseExternals(string(properties[props.Externals]))
			if err != nil {
				return err
			}
			definingURL := joinRepositoryURL(target.repositoryRoot, repositoryPath)
			for _, definition := range definitions {
				externalURL, err := resolveExportExternalURL(target.repositoryRoot, definingURL, definition.URL)
				if err != nil {
					return err
				}
				peg := definition.PegRevision
				if peg.Kind != svn.RevisionUnspecified {
					externalURL += "@" + peg.String()
				}
				key := externalURL + "\x00" + filepath.Join(destination, filepath.FromSlash(definition.LocalPath))
				if visited[key] {
					return fmt.Errorf("%w: recursive external %s", svn.ErrClientCycleDetected, externalURL)
				}
				visited[key] = true
				revision := definition.OperativeRevision
				if revision.Kind == svn.RevisionUnspecified {
					revision = svn.Revision{Kind: svn.RevisionHead}
				}
				resolved, err := client.resolveTarget(ctx, externalURL, InfoOptions{PegRevision: peg, Revision: revision})
				if err != nil {
					return err
				}
				kind, err := resolved.session.CheckPath(ctx, resolved.path, resolved.revision)
				if err == nil {
					err = client.exportRepositoryNode(ctx, resolved, "", filepath.Join(destination, filepath.FromSlash(definition.LocalPath)), kind, options, visited)
				}
				resolved.close()
				delete(visited, key)
				if err != nil {
					return err
				}
			}
		}
		return nil
	}
	var contents bytes.Buffer
	_, properties, err := target.session.GetFile(ctx, repositoryPath, target.revision, &contents, true)
	if err != nil {
		return err
	}
	if len(properties[props.Special]) != 0 && strings.HasPrefix(contents.String(), "link ") && runtime.GOOS != "windows" {
		if options.Force {
			_ = os.Remove(destination)
		}
		return os.Symlink(strings.TrimSuffix(strings.TrimPrefix(contents.String(), "link "), "\n"), destination)
	}
	flag := os.O_WRONLY | os.O_CREATE
	if options.Force {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_EXCL
	}
	output, err := os.OpenFile(destination, flag, 0o644)
	if err != nil {
		return err
	}
	keywords := props.KeywordValues(nil)
	if !options.IgnoreKeywords {
		entry, err := target.session.Stat(ctx, repositoryPath, target.revision)
		if err != nil {
			_ = output.Close()
			return err
		}
		if entry == nil {
			_ = output.Close()
			return fmt.Errorf("%w: %s", svn.ErrFSNotFound, repositoryPath)
		}
		keywords = props.ParseKeywords(string(properties[props.Keywords]), props.KeywordContext{
			Author: entry.LastAuthor, Basename: path.Base(repositoryPath), Date: entry.Time, Revision: entry.CreatedRev, Path: repositoryPath,
			RootURL: target.repositoryRoot, URL: joinRepositoryURL(target.repositoryRoot, repositoryPath),
		})
	}
	eol := options.NativeEOL
	if eol == "" {
		eol = string(properties[props.EOLStyle])
	}
	writeErr := props.Translate(&contents, output, eol, keywords, true, true)
	closeErr := output.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(properties[props.Executable]) != 0 {
		return os.Chmod(destination, 0o755)
	}
	return nil
}

func makeExportDirectory(destination string, force bool) error {
	if force {
		return os.MkdirAll(destination, 0o755)
	}
	return os.Mkdir(destination, 0o755)
}

func exportWorkingTree(source, destination string, force bool, excluded map[string]bool) error {
	stat, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !stat.IsDir() {
		return copyExportPath(source, destination, stat, force)
	}
	if err := makeExportDirectory(destination, force); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, name)
		if err != nil || relative == "." {
			return err
		}
		if entry.IsDir() && entry.Name() == ".svn" {
			return filepath.SkipDir
		}
		if excluded[filepath.Clean(relative)] {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		output := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(output, info.Mode().Perm())
		}
		return copyExportPath(name, output, info, force)
	})
}

func resolveExportExternalURL(repositoryRoot, definingURL, value string) (string, error) {
	root, err := url.Parse(repositoryRoot)
	if err != nil {
		return "", err
	}
	switch {
	case strings.HasPrefix(value, "^/"):
		return joinRepositoryURL(repositoryRoot, strings.TrimPrefix(value, "^/")), nil
	case strings.HasPrefix(value, "//"):
		return root.Scheme + ":" + value, nil
	case strings.HasPrefix(value, "/"):
		return root.Scheme + "://" + root.Host + path.Clean(value), nil
	case strings.Contains(value, "://"):
		return value, nil
	default:
		base, err := url.Parse(strings.TrimSuffix(definingURL, "/") + "/")
		if err != nil {
			return "", err
		}
		reference, err := url.Parse(value)
		if err != nil {
			return "", err
		}
		return base.ResolveReference(reference).String(), nil
	}
}

func copyExportPath(source, destination string, info os.FileInfo, force bool) error {
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if force {
			_ = os.Remove(destination)
		}
		return os.Symlink(target, destination)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	flag := os.O_WRONLY | os.O_CREATE
	if force {
		flag |= os.O_TRUNC
	} else {
		flag |= os.O_EXCL
	}
	output, err := os.OpenFile(destination, flag, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
