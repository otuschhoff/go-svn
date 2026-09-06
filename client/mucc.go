package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
)

type ActionKind uint8

const (
	ActionMkdir ActionKind = iota + 1
	ActionPut
	ActionDelete
	ActionCopy
	ActionSetProperty
)

type Action struct {
	Kind          ActionKind
	Path          string
	Source        string
	Revision      svn.Revnum
	Content       []byte
	PropertyName  string
	PropertyValue []byte
	NodeKind      svn.NodeKind
	AllowExisting bool
	RequireNew    bool
}

type MuccOptions struct {
	RevisionProperties svn.Props
	BaseRevision       svn.Revnum
	LockTokens         map[string]string
	KeepLocks          bool
}

type muccNode struct {
	actions []Action
	kind    svn.NodeKind
}

func (client *Client) Mucc(ctx context.Context, repositoryURL string, actions []Action, options MuccOptions) (*ra.CommitInfo, error) {
	if len(actions) == 0 {
		return nil, fmt.Errorf("%w: no repository actions", svn.ErrIncorrectParams)
	}
	session, _, err := ra.Open(ctx, repositoryURL, client.Callbacks)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	root, err := session.RepositoryRoot(ctx)
	if err != nil {
		return nil, err
	}
	base := options.BaseRevision
	if !base.IsValid() {
		base, err = session.LatestRevision(ctx)
		if err != nil {
			return nil, err
		}
	}
	grouped := make(map[string]*muccNode)
	for _, action := range actions {
		name, err := repositoryActionPath(root, action.Path)
		if err != nil {
			return nil, err
		}
		action.Path = name
		if action.Kind == ActionCopy {
			if action.Source == "" || !action.Revision.IsValid() {
				return nil, fmt.Errorf("%w: copy requires source and revision", svn.ErrIncorrectParams)
			}
		}
		if grouped[name] == nil {
			grouped[name] = &muccNode{}
		}
		grouped[name].actions = append(grouped[name].actions, action)
	}
	for name, node := range grouped {
		if name == "" {
			continue
		}
		node.kind, err = session.CheckPath(ctx, name, base)
		if err != nil {
			return nil, err
		}
	}
	var committed *ra.CommitInfo
	editor, err := session.GetCommitEditor(ctx, options.RevisionProperties, options.LockTokens, options.KeepLocks, func(info *ra.CommitInfo) error {
		copy := *info
		committed = &copy
		return nil
	})
	if err != nil {
		return nil, err
	}
	directory, err := editor.OpenRoot(ctx, base)
	if err == nil {
		if rootNode := grouped[""]; rootNode != nil {
			for _, action := range rootNode.actions {
				if action.Kind != ActionSetProperty {
					err = fmt.Errorf("%w: unsupported repository-root action %d", svn.ErrIncorrectParams, action.Kind)
					break
				}
				if err = directory.ChangeProp(ctx, action.PropertyName, action.PropertyValue); err != nil {
					break
				}
			}
			delete(grouped, "")
		}
	}
	if err == nil {
		err = delta.PathDriver(ctx, directory, sortedMuccPaths(grouped), base, func(ctx context.Context, parent delta.DirEditor, name string) (delta.DirEditor, error) {
			return driveMuccNode(ctx, parent, name, base, grouped[name])
		})
	}
	if err == nil {
		err = directory.Close(ctx)
	}
	if err == nil {
		err = editor.CloseEdit(ctx)
	} else {
		_ = editor.AbortEdit(ctx)
	}
	if err != nil {
		return nil, err
	}
	if committed == nil {
		return nil, fmt.Errorf("commit completed without revision information")
	}
	for _, action := range actions {
		notifyAction := notify.ActionCommitModified
		switch action.Kind {
		case ActionMkdir, ActionCopy:
			notifyAction = notify.ActionCommitAdded
		case ActionDelete:
			notifyAction = notify.ActionCommitDeleted
		}
		client.notify(notify.Notify{Action: notifyAction, Path: action.Path, Kind: action.NodeKind, Revision: committed.Revision})
	}
	client.notify(notify.Notify{Action: notify.ActionCommitPostfixTxdelta, Revision: committed.Revision})
	return committed, nil
}

func repositoryActionPath(root, value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" {
		name := strings.TrimPrefix(path.Clean("/"+value), "/")
		if name == "." {
			return "", nil
		}
		return name, nil
	}
	repository, err := url.Parse(root)
	if err != nil || parsed.Scheme != repository.Scheme || parsed.Host != repository.Host {
		return "", fmt.Errorf("%w: %s", svn.ErrRAIllegalURL, value)
	}
	rootPath := strings.TrimSuffix(repository.Path, "/")
	if parsed.Path != rootPath && !strings.HasPrefix(parsed.Path, rootPath+"/") {
		return "", fmt.Errorf("%w: %s", svn.ErrRAIllegalURL, value)
	}
	return strings.TrimPrefix(strings.TrimPrefix(parsed.Path, rootPath), "/"), nil
}

func sortedMuccPaths(nodes map[string]*muccNode) []string {
	paths := make([]string, 0, len(nodes))
	for name := range nodes {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths
}

func driveMuccNode(ctx context.Context, parent delta.DirEditor, name string, base svn.Revnum, node *muccNode) (delta.DirEditor, error) {
	if node == nil {
		return parent.OpenDirectory(ctx, name, base)
	}
	var directory delta.DirEditor
	var file delta.FileEditor
	for _, action := range node.actions {
		switch action.Kind {
		case ActionMkdir:
			if directory != nil || file != nil {
				return nil, fmt.Errorf("%w: duplicate add for %s", svn.ErrIncorrectParams, name)
			}
			var err error
			if node.kind == svn.NodeDir && action.AllowExisting {
				directory, err = parent.OpenDirectory(ctx, name, base)
			} else if node.kind != svn.NodeNone {
				return nil, fmt.Errorf("%w: %s already exists", svn.ErrFSAlreadyExists, name)
			} else {
				directory, err = parent.AddDirectory(ctx, name, nil)
			}
			if err != nil {
				return nil, err
			}
		case ActionPut:
			if file == nil {
				var err error
				if action.RequireNew && node.kind != svn.NodeNone {
					return nil, fmt.Errorf("%w: %s already exists", svn.ErrFSAlreadyExists, name)
				}
				if node.kind == svn.NodeFile {
					file, err = parent.OpenFile(ctx, name, base)
				} else {
					file, err = parent.AddFile(ctx, name, nil)
				}
				if err != nil {
					return nil, err
				}
			}
			if err := sendMuccContent(ctx, file, action.Content); err != nil {
				return nil, err
			}
		case ActionDelete:
			if directory != nil || file != nil {
				return nil, fmt.Errorf("%w: delete must precede other actions for %s", svn.ErrIncorrectParams, name)
			}
			if err := parent.DeleteEntry(ctx, name, action.Revision); err != nil {
				return nil, err
			}
			node.kind = svn.NodeNone
		case ActionCopy:
			source := &delta.CopySource{Path: action.Source, Rev: action.Revision}
			if action.NodeKind == svn.NodeDir {
				var err error
				directory, err = parent.AddDirectory(ctx, name, source)
				if err != nil {
					return nil, err
				}
			} else {
				var err error
				file, err = parent.AddFile(ctx, name, source)
				if err != nil {
					return nil, err
				}
			}
		case ActionSetProperty:
			if directory == nil && file == nil {
				var err error
				if node.kind == svn.NodeDir {
					directory, err = parent.OpenDirectory(ctx, name, base)
				} else if node.kind == svn.NodeFile {
					file, err = parent.OpenFile(ctx, name, base)
				} else {
					return nil, fmt.Errorf("%w: %s", svn.ErrFSNotFound, name)
				}
				if err != nil {
					return nil, err
				}
			}
			if directory != nil {
				if err := directory.ChangeProp(ctx, action.PropertyName, action.PropertyValue); err != nil {
					return nil, err
				}
			} else if err := file.ChangeProp(ctx, action.PropertyName, action.PropertyValue); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: unknown repository action %d", svn.ErrIncorrectParams, action.Kind)
		}
	}
	if file != nil {
		if err := file.Close(ctx, nil); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return directory, nil
}

func sendMuccContent(ctx context.Context, file delta.FileEditor, content []byte) error {
	handler, err := file.ApplyTextDelta(ctx, nil)
	if err != nil {
		return err
	}
	windows := delta.NewTxDeltaStream(bytes.NewReader(nil), bytes.NewReader(content))
	for {
		window, err := windows.NextWindow()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := handler.Window(window); err != nil {
			return err
		}
	}
	return handler.Close()
}

func (client *Client) MkdirURL(ctx context.Context, repositoryURL string, paths []string, parents bool, revprops svn.Props) (*ra.CommitInfo, error) {
	actions := make([]Action, 0, len(paths))
	seen := make(map[string]bool)
	for _, name := range paths {
		name = strings.TrimPrefix(path.Clean("/"+name), "/")
		if parents {
			parts := strings.Split(name, "/")
			for index := range parts {
				parent := strings.Join(parts[:index+1], "/")
				if !seen[parent] {
					actions = append(actions, Action{Kind: ActionMkdir, Path: parent, AllowExisting: true})
					seen[parent] = true
				}
			}
		} else if !seen[name] {
			actions = append(actions, Action{Kind: ActionMkdir, Path: name})
			seen[name] = true
		}
	}
	return client.Mucc(ctx, repositoryURL, actions, MuccOptions{RevisionProperties: revprops, BaseRevision: svn.InvalidRevnum})
}

func (client *Client) DeleteURL(ctx context.Context, repositoryURL string, paths []string, revision svn.Revnum, revprops svn.Props) (*ra.CommitInfo, error) {
	actions := make([]Action, len(paths))
	for index, name := range paths {
		actions[index] = Action{Kind: ActionDelete, Path: name, Revision: revision}
	}
	return client.Mucc(ctx, repositoryURL, actions, MuccOptions{RevisionProperties: revprops, BaseRevision: revision})
}

func (client *Client) CopyURL(ctx context.Context, repositoryURL, source, destination string, revision svn.Revnum, kind svn.NodeKind, revprops svn.Props) (*ra.CommitInfo, error) {
	return client.Mucc(ctx, repositoryURL, []Action{{Kind: ActionCopy, Path: destination, Source: source, Revision: revision, NodeKind: kind}}, MuccOptions{RevisionProperties: revprops, BaseRevision: revision})
}

func (client *Client) MoveURL(ctx context.Context, repositoryURL, source, destination string, revision svn.Revnum, kind svn.NodeKind, revprops svn.Props) (*ra.CommitInfo, error) {
	actions := []Action{{Kind: ActionDelete, Path: source, Revision: revision}, {Kind: ActionCopy, Path: destination, Source: source, Revision: revision, NodeKind: kind}}
	return client.Mucc(ctx, repositoryURL, actions, MuccOptions{RevisionProperties: revprops, BaseRevision: revision})
}
