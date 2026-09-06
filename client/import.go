package client

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

type ImportOptions struct {
	RevisionProperties svn.Props
	Depth              svn.Depth
	Force              bool
	NoIgnore           bool
	Parents            bool
	GlobalIgnores      []string
	AutoProps          map[string]svn.Props
}

func (client *Client) Import(ctx context.Context, source, repositoryURL, destination string, options ImportOptions) (*ra.CommitInfo, error) {
	stat, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	actions := make([]Action, 0)
	seenDirectories := make(map[string]bool)
	addDirectory := func(name string) {
		name = strings.TrimPrefix(path.Clean("/"+name), "/")
		if name != "" && name != "." && !seenDirectories[name] {
			actions = append(actions, Action{Kind: ActionMkdir, Path: name, AllowExisting: true})
			seenDirectories[name] = true
		}
	}
	if options.Parents {
		parentDestination := destination
		if !stat.IsDir() {
			parentDestination = path.Dir(destination)
		}
		parts := strings.Split(strings.Trim(parentDestination, "/"), "/")
		for index := range parts {
			addDirectory(strings.Join(parts[:index+1], "/"))
		}
	} else if stat.IsDir() {
		addDirectory(destination)
	}
	err = filepath.WalkDir(source, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, filename)
		if err != nil || relative == "." {
			return err
		}
		if entry.Name() == ".svn" && entry.IsDir() {
			return filepath.SkipDir
		}
		if !options.NoIgnore && importIgnored(entry.Name(), options.GlobalIgnores) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		level := strings.Count(filepath.ToSlash(relative), "/") + 1
		if options.Depth == svn.DepthEmpty || options.Depth == svn.DepthFiles && (entry.IsDir() || level > 1) || options.Depth == svn.DepthImmediates && level > 1 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := path.Join(destination, filepath.ToSlash(relative))
		if entry.IsDir() {
			addDirectory(name)
			return nil
		}
		fileActions, err := importFileActions(filename, name, entry.Name(), options)
		if err != nil {
			return err
		}
		actions = append(actions, fileActions...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !stat.IsDir() {
		fileActions, err := importFileActions(source, destination, filepath.Base(source), options)
		if err != nil {
			return nil, err
		}
		actions = append(actions, fileActions...)
	}
	return client.Mucc(ctx, repositoryURL, actions, MuccOptions{RevisionProperties: options.RevisionProperties, BaseRevision: svn.InvalidRevnum})
}

func importFileActions(filename, destination, basename string, options ImportOptions) ([]Action, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink == 0 && !info.Mode().IsRegular() {
		if options.Force {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: unsupported filesystem node %s", svn.ErrNodeUnexpectedKind, filename)
	}
	var content []byte
	properties := make(svn.Props)
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(filename)
		if err != nil {
			return nil, err
		}
		content = []byte("link " + target)
		properties[props.Special] = []byte("*")
	} else {
		content, err = os.ReadFile(filename)
		if err != nil {
			return nil, err
		}
		if info.Mode()&0o111 != 0 {
			properties[props.Executable] = []byte("*")
		}
		if detected := props.DetectMIMEType(basename, content); detected != "" {
			properties[props.MIMEType] = []byte(detected)
		}
	}
	for pattern, values := range options.AutoProps {
		if matched, _ := path.Match(pattern, basename); matched {
			for propertyName, value := range values {
				properties[propertyName] = append([]byte(nil), value...)
			}
		}
	}
	actions := []Action{{Kind: ActionPut, Path: destination, Content: content, RequireNew: true}}
	for propertyName, value := range properties {
		actions = append(actions, Action{Kind: ActionSetProperty, Path: destination, PropertyName: propertyName, PropertyValue: value})
	}
	return actions, nil
}

func importIgnored(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
	}
	return false
}
