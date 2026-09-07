package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	internaldiff "github.com/otuschhoff/go-svn/internal/diff"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

type DiffTarget struct {
	Target   string
	Revision svn.Revision
}

type DiffOptions struct {
	Depth             svn.Depth
	IgnoreAllSpace    bool
	IgnoreSpaceChange bool
	IgnoreEOL         bool
	ContextLines      int
	ContextSet        bool
	IgnoreProperties  bool
	PropertiesOnly    bool
	NoDiffAdded       bool
	NoDiffDeleted     bool
	Git               bool
	PatchCompatible   bool
	ShowCopiesAsAdds  bool
	workingCopy       bool
}

type DiffSummary struct {
	Path              string
	Kind              svn.NodeKind
	NodeStatus        byte
	TextModified      bool
	PropertiesChanged bool
}

type diffNode struct {
	kind           svn.NodeKind
	content        []byte
	properties     svn.Props
	displayPath    string
	repositoryPath string
	copied         bool
	copyFromPath   string
}

func (client *Client) Diff(ctx context.Context, left, right DiffTarget, output io.Writer, options DiffOptions) error {
	if output == nil {
		return fmt.Errorf("%w: nil diff output", svn.ErrIncorrectParams)
	}
	leftNodes, leftRevision, err := client.diffSnapshot(ctx, left, options.Depth)
	if err != nil {
		return err
	}
	rightNodes, rightRevision, err := client.diffSnapshot(ctx, right, options.Depth)
	if err != nil {
		return err
	}
	if !rightRevision.IsValid() {
		options.workingCopy = true
		if !options.ShowCopiesAsAdds && !options.PatchCompatible {
			applyWorkingCopyCopyBases(leftNodes, rightNodes)
		}
	}
	return emitDiff(output, leftNodes, rightNodes, leftRevision, rightRevision, options, nil)
}

func applyWorkingCopyCopyBases(left, right map[string]diffNode) {
	for name, destination := range right {
		if !destination.copied || destination.copyFromPath == "" {
			continue
		}
		for _, source := range left {
			if strings.Trim(source.repositoryPath, "/") != strings.Trim(destination.copyFromPath, "/") {
				continue
			}
			source.displayPath = destination.displayPath
			source.repositoryPath = destination.repositoryPath
			source.copied = true
			source.copyFromPath = destination.copyFromPath
			left[name] = source
			break
		}
	}
}

func (client *Client) DiffSummarize(ctx context.Context, left, right DiffTarget, options DiffOptions, callback func(DiffSummary) error) error {
	if callback == nil {
		return fmt.Errorf("%w: nil diff summary callback", svn.ErrIncorrectParams)
	}
	leftNodes, leftRevision, err := client.diffSnapshot(ctx, left, options.Depth)
	if err != nil {
		return err
	}
	rightNodes, rightRevision, err := client.diffSnapshot(ctx, right, options.Depth)
	if err != nil {
		return err
	}
	return emitDiff(io.Discard, leftNodes, rightNodes, leftRevision, rightRevision, options, callback)
}

func (client *Client) diffSnapshot(ctx context.Context, value DiffTarget, depth svn.Depth) (map[string]diffNode, svn.Revnum, error) {
	target, err := client.resolveTarget(ctx, value.Target, InfoOptions{Revision: value.Revision})
	if err != nil {
		return nil, svn.InvalidRevnum, err
	}
	defer target.close()
	if depth == svn.DepthUnknown {
		depth = svn.DepthInfinity
	}
	if target.workingInfo != nil && (value.Revision.Kind == svn.RevisionUnspecified || value.Revision.Kind == svn.RevisionWorking) {
		nodes, err := workingDiffSnapshot(ctx, target, depth)
		return nodes, svn.InvalidRevnum, err
	}
	nodes := make(map[string]diffNode)
	kind, err := target.session.CheckPath(ctx, target.path, target.revision)
	if err != nil {
		return nil, svn.InvalidRevnum, err
	}
	if kind == svn.NodeFile {
		var content bytes.Buffer
		_, properties, err := target.session.GetFile(ctx, target.path, target.revision, &content, true)
		if err != nil {
			return nil, svn.InvalidRevnum, err
		}
		displayPath := path.Base(target.url)
		repositoryPath := path.Base(target.url)
		if target.workingInfo != nil {
			displayPath = target.workingInfo.Path
			repositoryPath = target.workingInfo.RepositoryPath
		}
		nodes[path.Base(target.url)] = diffNode{kind: kind, content: content.Bytes(), properties: properties, displayPath: displayPath, repositoryPath: repositoryPath}
		return nodes, target.revision, nil
	}
	_, _, rootProperties, err := target.session.GetDir(ctx, target.path, target.revision, 0)
	if err != nil {
		return nil, svn.InvalidRevnum, err
	}
	displayPath := "."
	if target.workingInfo != nil {
		displayPath = target.workingInfo.Path
	}
	rootRepositoryPath := "."
	if target.workingInfo != nil {
		rootRepositoryPath = target.workingInfo.RepositoryPath
	}
	nodes["."] = diffNode{kind: svn.NodeDir, properties: rootProperties, displayPath: displayPath, repositoryPath: rootRepositoryPath}
	err = target.session.List(ctx, target.path, target.revision, nil, depth, svn.DirentKind, func(name string, entry *svn.Dirent) error {
		if entry == nil {
			return nil
		}
		nodeDisplayPath := name
		if target.workingInfo != nil {
			nodeDisplayPath = filepath.Join(target.workingInfo.Path, filepath.FromSlash(name))
		}
		nodeRepositoryPath := name
		if target.workingInfo != nil {
			nodeRepositoryPath = path.Join(rootRepositoryPath, name)
		}
		node := diffNode{kind: entry.Kind, displayPath: nodeDisplayPath, repositoryPath: nodeRepositoryPath}
		if entry.Kind == svn.NodeFile {
			var content bytes.Buffer
			_, properties, err := target.session.GetFile(ctx, path.Join(target.path, name), target.revision, &content, true)
			if err != nil {
				return err
			}
			node.content, node.properties = content.Bytes(), properties
		} else {
			_, _, properties, err := target.session.GetDir(ctx, path.Join(target.path, name), target.revision, 0)
			if err != nil {
				return err
			}
			node.properties = properties
		}
		nodes[name] = node
		return nil
	})
	return nodes, target.revision, err
}

func workingDiffSnapshot(ctx context.Context, target *resolvedTarget, depth svn.Depth) (map[string]diffNode, error) {
	root := target.workingInfo.Path
	rootStat, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	nodes := make(map[string]diffNode)
	if !rootStat.IsDir() {
		info, err := target.workingCopy.Info(ctx, root)
		if err != nil {
			return nil, err
		}
		content, err := canonicalDiffWorking(info)
		if err != nil {
			return nil, err
		}
		nodes[filepath.Base(root)] = diffNode{
			kind: svn.NodeFile, content: content, properties: info.WorkingProperties.Clone(), displayPath: root, repositoryPath: info.RepositoryPath,
			copied: info.Copied, copyFromPath: info.CopyFromPath,
		}
		return nodes, nil
	}
	rootInfo, err := target.workingCopy.Info(ctx, root)
	if err != nil {
		return nil, err
	}
	nodes["."] = diffNode{kind: svn.NodeDir, properties: rootInfo.WorkingProperties.Clone(), displayPath: root, repositoryPath: rootInfo.RepositoryPath}
	err = filepath.WalkDir(root, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, filename)
		if err != nil || relative == "." {
			return err
		}
		if entry.IsDir() && entry.Name() == ".svn" {
			return filepath.SkipDir
		}
		name := filepath.ToSlash(relative)
		level := strings.Count(name, "/") + 1
		if depth == svn.DepthEmpty || depth == svn.DepthFiles && entry.IsDir() || depth == svn.DepthImmediates && level > 1 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := target.workingCopy.Info(ctx, filename)
		if err != nil {
			return nil
		}
		node := diffNode{
			kind: info.Kind, properties: info.WorkingProperties.Clone(), displayPath: filename, repositoryPath: info.RepositoryPath,
			copied: info.Copied, copyFromPath: info.CopyFromPath,
		}
		if info.Kind == svn.NodeFile {
			node.content, err = canonicalDiffWorking(info)
			if err != nil {
				return err
			}
		}
		nodes[name] = node
		return nil
	})
	return nodes, err
}

func canonicalDiffWorking(info *wc.Info) ([]byte, error) {
	if len(info.WorkingProperties[props.Special]) != 0 {
		target, err := os.Readlink(info.Path)
		if err != nil {
			return nil, err
		}
		return props.EncodeSpecial(target), nil
	}
	contents, err := os.Open(info.Path)
	if err != nil {
		return nil, err
	}
	defer contents.Close()
	keywords := props.ParseKeywords(string(info.WorkingProperties[props.Keywords]), props.KeywordContext{
		Author: info.ChangedAuthor, Basename: filepath.Base(info.Path), Date: info.ChangedDate, Path: info.RepositoryPath,
		Revision: info.ChangedRevision, RootURL: info.RepositoryRoot, URL: info.URL,
	})
	var canonical bytes.Buffer
	if err := props.DetranslateFile(contents, &canonical, string(info.WorkingProperties[props.EOLStyle]), keywords, false); err != nil {
		return nil, err
	}
	return canonical.Bytes(), nil
}

func emitDiff(output io.Writer, left, right map[string]diffNode, leftRevision, rightRevision svn.Revnum, options DiffOptions, summary func(DiffSummary) error) error {
	if options.PatchCompatible {
		options.IgnoreProperties = true
		options.ShowCopiesAsAdds = true
	}
	paths := make(map[string]bool)
	for name := range left {
		paths[name] = true
	}
	for name := range right {
		paths[name] = true
	}
	names := make([]string, 0, len(paths))
	for name := range paths {
		names = append(names, name)
	}
	sort.Slice(names, func(first, second int) bool {
		if options.workingCopy {
			return names[first] < names[second]
		}
		firstRank := diffPathRank(names[first], left, right)
		secondRank := diffPathRank(names[second], left, right)
		if firstRank != secondRank {
			return firstRank < secondRank
		}
		return names[first] < names[second]
	})
	diffOptions := internaldiff.Options{
		IgnoreAllSpace: options.IgnoreAllSpace, IgnoreSpaceChange: options.IgnoreSpaceChange, IgnoreEOL: options.IgnoreEOL,
		ContextLines: options.ContextLines, ContextSet: options.ContextSet,
	}
	for _, name := range names {
		oldNode, oldExists := left[name]
		newNode, newExists := right[name]
		displayName := newNode.displayPath
		if displayName == "" {
			displayName = oldNode.displayPath
		}
		if displayName == "" {
			displayName = name
		}
		status := byte('M')
		if !oldExists {
			status = 'A'
		} else if !newExists {
			status = 'D'
		}
		propertiesChanged := !equalProps(oldNode.properties, newNode.properties)
		textChanged := !bytes.Equal(oldNode.content, newNode.content) || oldExists != newExists
		if !textChanged && !propertiesChanged {
			if options.workingCopy && newNode.copied {
				fmt.Fprintf(output, "Index: %s\n===================================================================\n", displayName)
			}
			continue
		}
		kind := newNode.kind
		if !newExists {
			kind = oldNode.kind
		}
		if summary != nil {
			if err := summary(DiffSummary{Path: displayName, Kind: kind, NodeStatus: status, TextModified: textChanged, PropertiesChanged: propertiesChanged}); err != nil {
				return err
			}
			continue
		}
		wroteHeader := false
		if kind == svn.NodeFile && textChanged && (status == 'A' && options.NoDiffAdded || status == 'D' && options.NoDiffDeleted) {
			marker := "added"
			if status == 'D' {
				marker = "deleted"
			}
			fmt.Fprintf(output, "Index: %s (%s)\n===================================================================\n", displayName, marker)
			wroteHeader = true
		}
		if kind == svn.NodeFile && textChanged && !options.PropertiesOnly && !(status == 'A' && options.NoDiffAdded) && !(status == 'D' && options.NoDiffDeleted) {
			oldPath, newPath := displayName, displayName
			if options.Git {
				repositoryPath := newNode.repositoryPath
				if repositoryPath == "" {
					repositoryPath = oldNode.repositoryPath
				}
				if repositoryPath == "" {
					repositoryPath = name
				}
				oldPath, newPath = "a/"+repositoryPath, "b/"+repositoryPath
			}
			oldLabel := oldPath + "\t(revision " + revisionString(leftRevision) + ")"
			newLabel := newPath + "\t(revision " + revisionString(rightRevision) + ")"
			if !oldExists {
				oldLabel = oldPath + "\t(nonexistent)"
			}
			if !newExists {
				newLabel = newPath + "\t(nonexistent)"
			} else if !rightRevision.IsValid() {
				newLabel = newPath + "\t(working copy)"
			}
			binary := props.IsBinaryMIMEType(string(oldNode.properties[props.MIMEType])) || props.IsBinaryMIMEType(string(newNode.properties[props.MIMEType]))
			textDiff := internaldiff.Unified(oldNode.content, newNode.content, oldLabel, newLabel, diffOptions)
			if binary || len(textDiff) != 0 || options.Git {
				fmt.Fprintf(output, "Index: %s\n===================================================================\n", displayName)
				if options.Git {
					fmt.Fprintf(output, "diff --git %s %s\n", oldPath, newPath)
					if status == 'A' {
						fmt.Fprintf(output, "new file mode %s\n", gitFileMode(newNode))
					} else if status == 'D' {
						fmt.Fprintf(output, "deleted file mode %s\n", gitFileMode(oldNode))
					}
				}
				if binary {
					fmt.Fprintln(output, "Cannot display: file marked as a binary type.")
				} else if _, err := output.Write(textDiff); err != nil {
					return err
				}
				wroteHeader = true
			}
		}
		if propertiesChanged && !options.IgnoreProperties {
			if !wroteHeader {
				oldPath, newPath := displayName, displayName
				fmt.Fprintf(output, "Index: %s\n===================================================================\n", displayName)
				if options.Git {
					repositoryPath := newNode.repositoryPath
					if repositoryPath == "" {
						repositoryPath = oldNode.repositoryPath
					}
					if repositoryPath == "" {
						repositoryPath = name
					}
					oldPath, newPath = "a/"+repositoryPath, "b/"+repositoryPath
					fmt.Fprintf(output, "diff --git %s %s\n", oldPath, newPath)
				}
				oldLabel := oldPath + "\t(revision " + revisionString(leftRevision) + ")"
				newLabel := newPath + "\t(revision " + revisionString(rightRevision) + ")"
				if !oldExists {
					oldLabel = oldPath + "\t(nonexistent)"
				}
				if !newExists {
					newLabel = newPath + "\t(nonexistent)"
				} else if !rightRevision.IsValid() {
					newLabel = newPath + "\t(working copy)"
				}
				fmt.Fprintf(output, "--- %s\n+++ %s\n", oldLabel, newLabel)
			}
			if err := emitPropertyDiff(output, displayName, oldNode.properties, newNode.properties); err != nil {
				return err
			}
		}
	}
	return nil
}

func diffPathRank(name string, left, right map[string]diffNode) int {
	_, oldExists := left[name]
	_, newExists := right[name]
	if oldExists && !newExists {
		return 0
	}
	return 1
}

func gitFileMode(node diffNode) string {
	if len(node.properties[props.Special]) != 0 {
		return "120000"
	}
	if len(node.properties[props.Executable]) != 0 {
		return "100755"
	}
	return "100644"
}

func equalProps(left, right svn.Props) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if !bytes.Equal(value, right[name]) {
			return false
		}
	}
	return true
}

func emitPropertyDiff(output io.Writer, name string, oldProperties, newProperties svn.Props) error {
	propertyNames := make(map[string]bool)
	for propertyName := range oldProperties {
		propertyNames[propertyName] = true
	}
	for propertyName := range newProperties {
		propertyNames[propertyName] = true
	}
	names := make([]string, 0, len(propertyNames))
	for propertyName := range propertyNames {
		names = append(names, propertyName)
	}
	sort.Strings(names)
	wroteHeader := false
	for _, propertyName := range names {
		oldValue, oldExists := oldProperties[propertyName]
		newValue, newExists := newProperties[propertyName]
		if oldExists == newExists && bytes.Equal(oldValue, newValue) {
			continue
		}
		if !wroteHeader {
			fmt.Fprintf(output, "\nProperty changes on: %s\n___________________________________________________________________\n", name)
			wroteHeader = true
		}
		action := "Modified"
		if !oldExists {
			action = "Added"
		} else if !newExists {
			action = "Deleted"
		}
		fmt.Fprintf(output, "%s: %s\n", action, propertyName)
		if diff := internaldiff.UnifiedProperty(oldValue, newValue); len(diff) > 0 {
			if _, err := output.Write(diff); err != nil {
				return err
			}
		}
	}
	return nil
}
