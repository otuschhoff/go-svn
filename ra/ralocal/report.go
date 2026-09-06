package ralocal

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
)

type report struct {
	session        *Session
	revision       svn.Revnum
	target         string
	source         string
	editor         delta.Editor
	baseRev        svn.Revnum
	empty          bool
	textDeltas     bool
	sendCopyfrom   bool
	ignoreAncestry bool
	explicitSource bool
	depth          svn.Depth
	paths          map[string]reportPath
	done           bool
}

type reportPath struct {
	revision svn.Revnum
	source   string
	depth    svn.Depth
	empty    bool
	deleted  bool
}

func (session *Session) DoUpdate(_ context.Context, revision svn.Revnum, target string, depth svn.Depth, sendCopyfrom bool, ignoreAncestry bool, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(revision, target, session.join(target), depth, true, sendCopyfrom, ignoreAncestry, false, editor)
}

func (session *Session) DoSwitch(_ context.Context, revision svn.Revnum, target string, depth svn.Depth, switchURL string, sendCopyfrom bool, ignoreAncestry bool, editor delta.Editor) (ra.Reporter, error) {
	source, ok := relativeURL(session.rootURL, switchURL)
	if !ok {
		return nil, fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, switchURL)
	}
	return session.newReporter(revision, target, source, depth, true, sendCopyfrom, ignoreAncestry, true, editor)
}

func (session *Session) DoStatus(_ context.Context, target string, revision svn.Revnum, depth svn.Depth, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(revision, target, session.join(target), depth, false, false, false, false, editor)
}

func (session *Session) DoDiff(_ context.Context, revision svn.Revnum, target string, depth svn.Depth, ignoreAncestry bool, textDeltas bool, versusURL string, editor delta.Editor) (ra.Reporter, error) {
	source, ok := relativeURL(session.rootURL, versusURL)
	if !ok {
		return nil, fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, versusURL)
	}
	return session.newReporter(revision, target, source, depth, textDeltas, false, ignoreAncestry, true, editor)
}

func (session *Session) newReporter(revision svn.Revnum, target, source string, depth svn.Depth, textDeltas, sendCopyfrom, ignoreAncestry, explicitSource bool, editor delta.Editor) (ra.Reporter, error) {
	if editor == nil {
		return nil, fmt.Errorf("%w: nil editor", svn.ErrIncorrectParams)
	}
	return &report{session: session, revision: revision, target: target, source: source, editor: editor, depth: depth, baseRev: svn.InvalidRevnum, textDeltas: textDeltas, sendCopyfrom: sendCopyfrom, ignoreAncestry: ignoreAncestry, explicitSource: explicitSource, paths: make(map[string]reportPath)}, nil
}

func (reporter *report) SetPath(_ context.Context, name string, revision svn.Revnum, depth svn.Depth, startEmpty bool, _ string) error {
	name = clean(name)
	reporter.paths[name] = reportPath{revision: revision, source: path.Join(reporter.session.join(reporter.target), name), depth: depth, empty: startEmpty}
	if name == "" {
		reporter.baseRev, reporter.empty = revision, startEmpty
	}
	return nil
}

func (reporter *report) LinkPath(ctx context.Context, name, rawURL string, revision svn.Revnum, depth svn.Depth, startEmpty bool, lockToken string) error {
	if _, ok := relativeURL(reporter.session.rootURL, rawURL); !ok {
		return fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, rawURL)
	}
	name = clean(name)
	source, _ := relativeURL(reporter.session.rootURL, rawURL)
	reporter.paths[name] = reportPath{revision: revision, source: source, depth: depth, empty: startEmpty}
	return nil
}

func (reporter *report) DeletePath(_ context.Context, name string) error {
	reporter.paths[clean(name)] = reportPath{deleted: true}
	return nil
}

func (reporter *report) FinishReport(ctx context.Context) error {
	if reporter.done {
		return fmt.Errorf("%w: report is complete", svn.ErrIncorrectParams)
	}
	reporter.done = true
	targetRoot, resolved, err := reporter.session.repository.Root(ctx, reporter.revision)
	if err != nil {
		return err
	}
	lookup := reporter.baseLookup()
	editor := delta.DepthFilter(reporter.editor, reporter.depth, "")
	rootPath := reporter.source
	rootReport, hasRootReport := reporter.paths[""]
	if hasRootReport && rootReport.source != "" && !reporter.explicitSource {
		rootPath = rootReport.source
	}
	kind, err := targetRoot.CheckPath(ctx, reporter.source)
	if err != nil {
		return err
	}
	currentKind := kind
	if kind == svn.NodeNone && reporter.baseRev.IsValid() {
		baseRoot, _, rootErr := reporter.session.repository.Root(ctx, reporter.baseRev)
		if rootErr != nil {
			return rootErr
		}
		kind, err = baseRoot.CheckPath(ctx, reporter.source)
		if err != nil {
			return err
		}
	}
	missingTarget := hasRootReport && rootReport.deleted && reporter.target != ""
	deletedTarget := currentKind == svn.NodeNone && kind != svn.NodeNone && reporter.target != ""
	if kind != svn.NodeDir || missingTarget || deletedTarget {
		name := path.Base(reporter.target)
		if reporter.target == "" {
			name = path.Base(reporter.source)
		}
		rootPath = path.Dir(reporter.source)
		editor = delta.DepthFilter(reporter.editor, reporter.depth, name)
		if missingTarget {
			previousRevision := reporter.revision - 1
			previousRoot, _, previousErr := reporter.session.repository.Root(ctx, previousRevision)
			lookup = func(_ context.Context, editorPath string) (fs.Root, string, svn.Revnum, error) {
				if previousErr != nil {
					return nil, "", svn.InvalidRevnum, previousErr
				}
				return previousRoot, path.Join(rootPath, editorPath), previousRevision, nil
			}
		} else {
			lookup = reporter.fileTargetLookup(ctx, name, rootPath)
		}
	}
	return driveRoots(ctx, editor, reporter.session.repository, lookup, reporter.excluded, targetRoot, rootPath, resolved, reporter.textDeltas, reporter.sendCopyfrom, reporter.ignoreAncestry, svn.InvalidRevnum)
}

func (reporter *report) AbortReport(context.Context) error {
	reporter.done = true
	return nil
}

func (session *Session) Replay(ctx context.Context, revision, lowWaterMark svn.Revnum, sendDeltas bool, editor delta.Editor) error {
	if !revision.IsValid() || !lowWaterMark.IsValid() || editor == nil {
		return fmt.Errorf("%w: invalid replay arguments", svn.ErrIncorrectParams)
	}
	current, resolved, err := session.repository.Root(ctx, revision)
	if err != nil {
		return err
	}
	var previous fs.Root
	if resolved > 0 {
		previous, _, err = session.repository.Root(ctx, resolved-1)
		if err != nil {
			return err
		}
	}
	lookup := fixedBaseLookup(previous, session.base, resolved-1)
	return driveRoots(ctx, editor, session.repository, lookup, nil, current, session.base, resolved, sendDeltas, true, false, lowWaterMark)
}

func (session *Session) ReplayRange(ctx context.Context, start, end, low svn.Revnum, send bool, begin func(svn.Revnum, svn.Props) (delta.Editor, error), finish func(svn.Revnum, svn.Props, delta.Editor) error) error {
	if !start.IsValid() || end < start || !low.IsValid() || begin == nil || finish == nil {
		return fmt.Errorf("%w: invalid replay range arguments", svn.ErrIncorrectParams)
	}
	for revision := start; revision <= end; revision++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		props, err := session.repository.RevisionProps(ctx, revision)
		if err != nil {
			return err
		}
		editor, err := begin(revision, props.Clone())
		if err != nil {
			return err
		}
		if err := session.Replay(ctx, revision, low, send, editor); err != nil {
			return err
		}
		if err := finish(revision, props.Clone(), editor); err != nil {
			return err
		}
	}
	return nil
}

type baseLookup func(context.Context, string) (fs.Root, string, svn.Revnum, error)

func fixedBaseLookup(root fs.Root, repositoryPath string, revision svn.Revnum) baseLookup {
	return func(_ context.Context, editorPath string) (fs.Root, string, svn.Revnum, error) {
		return root, path.Join(repositoryPath, editorPath), revision, nil
	}
}

func (reporter *report) baseLookup() baseLookup {
	return func(ctx context.Context, editorPath string) (fs.Root, string, svn.Revnum, error) {
		name := clean(editorPath)
		var selected *reportPath
		selectedName := ""
		for candidate, state := range reporter.paths {
			if candidate == name || candidate == "" || strings.HasPrefix(name, candidate+"/") {
				if selected == nil || len(candidate) > len(selectedName) {
					copy := state
					selected, selectedName = &copy, candidate
				}
			}
		}
		if selected == nil {
			selected = &reportPath{revision: reporter.baseRev, source: reporter.session.join(reporter.target), empty: reporter.empty}
		}
		if selected.deleted || selected.empty || !selected.revision.IsValid() {
			return nil, "", selected.revision, nil
		}
		root, _, err := reporter.session.repository.Root(ctx, selected.revision)
		if err != nil {
			return nil, "", svn.InvalidRevnum, err
		}
		remainder := strings.TrimPrefix(strings.TrimPrefix(name, selectedName), "/")
		basePath := path.Join(selected.source, remainder)
		if remainder != "" {
			level := strings.Count(remainder, "/") + 1
			included := selected.depth == svn.DepthInfinity || selected.depth == svn.DepthUnknown || selected.depth == svn.DepthImmediates && level == 1
			if selected.depth == svn.DepthFiles && level == 1 {
				kind, err := root.CheckPath(ctx, basePath)
				if err != nil {
					return nil, "", svn.InvalidRevnum, err
				}
				included = kind == svn.NodeFile
			}
			if !included {
				return nil, "", svn.InvalidRevnum, nil
			}
		}
		return root, basePath, selected.revision, nil
	}
}

func (reporter *report) fileTargetLookup(ctx context.Context, targetName, parentPath string) baseLookup {
	targetLookup := reporter.baseLookup()
	baseRoot, _, baseErr := reporter.session.repository.Root(ctx, reporter.baseRev)
	return func(ctx context.Context, editorPath string) (fs.Root, string, svn.Revnum, error) {
		if editorPath == targetName {
			return targetLookup(ctx, "")
		}
		if baseErr != nil {
			return nil, "", svn.InvalidRevnum, baseErr
		}
		return baseRoot, path.Join(parentPath, editorPath), reporter.baseRev, nil
	}
}

func (reporter *report) excluded(editorPath string) bool {
	name := clean(editorPath)
	for candidate, state := range reporter.paths {
		if state.depth == svn.DepthExclude && (candidate == name || strings.HasPrefix(name, candidate+"/")) {
			return true
		}
	}
	return false
}

func driveRoots(ctx context.Context, editor delta.Editor, repository *repos.Repository, lookup baseLookup, excluded func(string) bool, currentRoot fs.Root, rootPath string, revision svn.Revnum, textDeltas, sendCopyfrom, ignoreAncestry bool, copyFloor svn.Revnum) error {
	if editor == nil {
		return fmt.Errorf("%w: nil editor", svn.ErrIncorrectParams)
	}
	if err := editor.SetTargetRevision(ctx, revision); err != nil {
		return err
	}
	oldRoot, oldPath, baseRevision, err := lookup(ctx, "")
	if err != nil {
		return err
	}
	output, err := editor.OpenRoot(ctx, baseRevision)
	if err != nil {
		return err
	}
	if excluded == nil || !excluded("") {
		if err := changeNodeProps(ctx, output.ChangeProp, oldRoot, oldPath, currentRoot, rootPath); err != nil {
			_ = editor.AbortEdit(ctx)
			return fmt.Errorf("report root properties %s: %w", rootPath, err)
		}
	}
	changes, err := currentRoot.PathsChanged(ctx)
	if err != nil {
		_ = editor.AbortEdit(ctx)
		return fmt.Errorf("report changed paths: %w", err)
	}
	if excluded == nil || !excluded("") {
		if err := diffDirectory(ctx, output, repository, lookup, excluded, currentRoot, rootPath, "", revision, textDeltas, sendCopyfrom, ignoreAncestry, copyFloor, changes); err != nil {
			_ = editor.AbortEdit(ctx)
			return fmt.Errorf("report directory diff %s: %w", rootPath, err)
		}
	}
	if err := output.Close(ctx); err != nil {
		return err
	}
	return editor.CloseEdit(ctx)
}

func diffDirectory(ctx context.Context, output delta.DirEditor, repository *repos.Repository, lookup baseLookup, excluded func(string) bool, currentRoot fs.Root, repositoryPath, editorPath string, revision svn.Revnum, textDeltas, sendCopyfrom, ignoreAncestry bool, copyFloor svn.Revnum, changes map[string]fs.PathChange) error {
	oldRoot, oldRepositoryPath, _, err := lookup(ctx, editorPath)
	if err != nil {
		return err
	}
	oldEntries, err := entriesByName(ctx, oldRoot, oldRepositoryPath)
	if err != nil {
		return err
	}
	currentEntries, err := entriesByName(ctx, currentRoot, repositoryPath)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(oldEntries)+len(currentEntries))
	seen := make(map[string]bool)
	for name := range oldEntries {
		seen[name] = true
		names = append(names, name)
	}
	for name := range currentEntries {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		before, hadBefore := oldEntries[name]
		after, hasAfter := currentEntries[name]
		childRepositoryPath := path.Join(repositoryPath, name)
		childEditorPath := path.Join(editorPath, name)
		if excluded != nil && excluded(childEditorPath) {
			continue
		}
		childOldRoot, childOldPath, childBaseRev, lookupErr := lookup(ctx, childEditorPath)
		if lookupErr != nil {
			return lookupErr
		}
		if childOldRoot == nil {
			hadBefore = false
		} else {
			kind, checkErr := childOldRoot.CheckPath(ctx, childOldPath)
			if checkErr != nil {
				return checkErr
			}
			hadBefore = kind != svn.NodeNone
			before.Kind = kind
			if hadBefore && hasAfter && kind == after.Kind && !ignoreAncestry {
				oldID, idErr := childOldRoot.NodeID(ctx, childOldPath)
				if idErr != nil {
					return idErr
				}
				currentID, idErr := currentRoot.NodeID(ctx, childRepositoryPath)
				if idErr != nil {
					return idErr
				}
				if oldID != currentID {
					before.Kind = svn.NodeUnknown
				}
			}
		}
		if hadBefore && (!hasAfter || before.Kind != after.Kind) {
			if err := output.DeleteEntry(ctx, childEditorPath, childBaseRev); err != nil {
				return err
			}
			hadBefore = false
		}
		if !hasAfter {
			continue
		}
		if after.Kind == svn.NodeDir {
			var child delta.DirEditor
			if hadBefore {
				child, err = output.OpenDirectory(ctx, childEditorPath, childBaseRev)
			} else {
				var source *delta.CopySource
				source, err = copySource(ctx, repository, currentRoot, changes, childRepositoryPath, sendCopyfrom, copyFloor)
				if err == nil {
					child, err = output.AddDirectory(ctx, childEditorPath, source)
				}
			}
			if err != nil {
				return err
			}
			if !hadBefore {
				childOldRoot, childOldPath = nil, ""
			}
			if err := changeNodeProps(ctx, child.ChangeProp, childOldRoot, childOldPath, currentRoot, childRepositoryPath); err != nil {
				return err
			}
			if err := diffDirectory(ctx, child, repository, lookup, excluded, currentRoot, childRepositoryPath, childEditorPath, revision, textDeltas, sendCopyfrom, ignoreAncestry, copyFloor, changes); err != nil {
				return err
			}
			if err := child.Close(ctx); err != nil {
				return err
			}
			continue
		}
		var file delta.FileEditor
		if hadBefore {
			file, err = output.OpenFile(ctx, childEditorPath, childBaseRev)
		} else {
			var source *delta.CopySource
			source, err = copySource(ctx, repository, currentRoot, changes, childRepositoryPath, sendCopyfrom, copyFloor)
			if err == nil {
				file, err = output.AddFile(ctx, childEditorPath, source)
			}
		}
		if err != nil {
			return err
		}
		if !hadBefore {
			childOldRoot, childOldPath = nil, ""
		}
		if err := changeNodeProps(ctx, file.ChangeProp, childOldRoot, childOldPath, currentRoot, childRepositoryPath); err != nil {
			return err
		}
		changed, err := fileChanged(ctx, childOldRoot, childOldPath, currentRoot, childRepositoryPath)
		if err != nil {
			return err
		}
		if changed && textDeltas {
			var baseChecksum *svn.Checksum
			if childOldRoot != nil {
				baseChecksum, err = childOldRoot.FileChecksum(ctx, childOldPath, svn.ChecksumMD5)
				if err != nil {
					return err
				}
			}
			handler, err := file.ApplyTextDelta(ctx, baseChecksum)
			if err != nil {
				return err
			}
			writer := &windowWriter{handler: handler}
			if err := currentRoot.FileContents(ctx, childRepositoryPath, writer); err != nil {
				return err
			}
			if err := handler.Close(); err != nil {
				return err
			}
		}
		checksum, err := currentRoot.FileChecksum(ctx, childRepositoryPath, svn.ChecksumMD5)
		if err != nil {
			return err
		}
		if err := file.Close(ctx, checksum); err != nil {
			return err
		}
	}
	return nil
}

func copySource(ctx context.Context, repository *repos.Repository, currentRoot fs.Root, changes map[string]fs.PathChange, nodePath string, enabled bool, copyFloor svn.Revnum) (*delta.CopySource, error) {
	if !enabled {
		return nil, nil
	}
	change, found := changes["/"+strings.TrimPrefix(nodePath, "/")]
	if !found {
		change, found = changes[strings.TrimPrefix(nodePath, "/")]
	}
	if found && change.CopyFromRev.IsValid() && change.CopyFromPath != "" && (!copyFloor.IsValid() || change.CopyFromRev >= copyFloor) {
		return &delta.CopySource{Path: change.CopyFromPath, Rev: change.CopyFromRev}, nil
	}
	copyPath, copyRevision, err := currentRoot.ClosestCopy(ctx, nodePath)
	if err != nil || !copyRevision.IsValid() || clean(copyPath) != clean(nodePath) {
		return nil, err
	}
	copyRoot, _, err := repository.Root(ctx, copyRevision)
	if err != nil {
		return nil, err
	}
	copyChanges, err := copyRoot.PathsChanged(ctx)
	if err != nil {
		return nil, err
	}
	change, found = copyChanges["/"+strings.TrimPrefix(copyPath, "/")]
	if !found {
		change, found = copyChanges[strings.TrimPrefix(copyPath, "/")]
	}
	if !found || !change.CopyFromRev.IsValid() || change.CopyFromPath == "" || copyFloor.IsValid() && change.CopyFromRev < copyFloor {
		return nil, nil
	}
	return &delta.CopySource{Path: change.CopyFromPath, Rev: change.CopyFromRev}, nil
}

type windowWriter struct {
	handler delta.WindowHandler
}

func (writer *windowWriter) Write(data []byte) (int, error) {
	const windowSize = 64 * 1024
	written := 0
	for len(data) != 0 {
		size := min(len(data), windowSize)
		chunk := append([]byte(nil), data[:size]...)
		window := &delta.Window{TargetLength: size, Ops: []delta.Op{{Kind: delta.OpNew, Length: size}}, NewData: chunk}
		if err := writer.handler.Window(window); err != nil {
			return written, err
		}
		written += size
		data = data[size:]
	}
	return written, nil
}

func entriesByName(ctx context.Context, root fs.Root, nodePath string) (map[string]fs.DirEntry, error) {
	result := make(map[string]fs.DirEntry)
	if root == nil {
		return result, nil
	}
	kind, err := root.CheckPath(ctx, nodePath)
	if err != nil || kind == svn.NodeNone {
		return result, err
	}
	if kind != svn.NodeDir {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, nodePath)
	}
	entries, err := root.DirEntries(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		result[entry.Name] = entry
	}
	return result, nil
}

func changeNodeProps(ctx context.Context, change func(context.Context, string, []byte) error, oldRoot fs.Root, oldPath string, currentRoot fs.Root, nodePath string) error {
	oldProps := make(svn.Props)
	if oldRoot != nil {
		kind, err := oldRoot.CheckPath(ctx, oldPath)
		if err != nil {
			return err
		}
		if kind != svn.NodeNone {
			oldProps, err = oldRoot.NodeProps(ctx, oldPath)
			if err != nil {
				return err
			}
		}
	}
	currentProps, err := currentRoot.NodeProps(ctx, nodePath)
	if err != nil {
		return err
	}
	names := make(map[string]bool)
	for name := range oldProps {
		names[name] = true
	}
	for name := range currentProps {
		names[name] = true
	}
	for name := range names {
		if !bytes.Equal(oldProps[name], currentProps[name]) {
			if err := change(ctx, name, currentProps[name]); err != nil {
				return err
			}
		}
	}
	return nil
}

func fileChanged(ctx context.Context, oldRoot fs.Root, oldPath string, currentRoot fs.Root, nodePath string) (bool, error) {
	if oldRoot == nil {
		return true, nil
	}
	oldChecksum, err := oldRoot.FileChecksum(ctx, oldPath, svn.ChecksumMD5)
	if err != nil {
		return false, err
	}
	currentChecksum, err := currentRoot.FileChecksum(ctx, nodePath, svn.ChecksumMD5)
	if err != nil {
		return false, err
	}
	return !oldChecksum.Equal(*currentChecksum), nil
}
