package wc

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/otuschhoff/go-svn/config"
	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

type StatusKind string

const (
	StatusNone        StatusKind = "none"
	StatusNormal      StatusKind = "normal"
	StatusModified    StatusKind = "modified"
	StatusAdded       StatusKind = "added"
	StatusDeleted     StatusKind = "deleted"
	StatusReplaced    StatusKind = "replaced"
	StatusMissing     StatusKind = "missing"
	StatusObstructed  StatusKind = "obstructed"
	StatusConflicted  StatusKind = "conflicted"
	StatusIgnored     StatusKind = "ignored"
	StatusUnversioned StatusKind = "unversioned"
	StatusExternal    StatusKind = "external"
	StatusIncomplete  StatusKind = "incomplete"
)

type StatusOptions struct {
	Depth         svn.Depth
	Verbose       bool
	NoIgnore      bool
	GlobalIgnores []string
	Config        *config.Config
	ShowUpdates   bool
	Revision      *svn.Revnum
	Session       ra.Session
}

type Status struct {
	Path             string
	RelativePath     string
	Kind             svn.NodeKind
	NodeStatus       StatusKind
	TextStatus       StatusKind
	PropertyStatus   StatusKind
	Revision         svn.Revnum
	ChangedRevision  svn.Revnum
	ChangedAuthor    string
	RepositoryPath   string
	Copied           bool
	Switched         bool
	FileExternal     bool
	MovedFrom        string
	MovedTo          string
	WCInfoLocked     bool
	TreeConflicted   bool
	Conflicted       bool
	Changelist       string
	Lock             *svn.Lock
	RepositoryStatus StatusKind
	RepositoryLock   *svn.Lock
}

func (database *Database) Status(ctx context.Context, targetPath string, options StatusOptions, callback func(*Status) error) error {
	if options.Depth == svn.DepthUnknown {
		options.Depth = svn.DepthInfinity
	}
	if options.GlobalIgnores == nil {
		clientConfig := options.Config
		if clientConfig == nil {
			clientConfig = config.New()
		}
		options.GlobalIgnores = clientConfig.GetList("miscellany", "global-ignores", nil)
	}
	targetRelpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	versioned, err := database.versionedPaths(ctx, targetRelpath, options.Depth)
	if err != nil {
		return err
	}
	statuses := make(map[string]*Status, len(versioned))
	for _, relpath := range versioned {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(relpath)))
		if err != nil {
			return err
		}
		if info.Presence == PresenceExcluded || info.Presence == PresenceServerExcluded || info.Presence == PresenceNotPresent {
			continue
		}
		status, err := database.statusForInfo(ctx, info)
		if err != nil {
			return err
		}
		statuses[relpath] = status
	}
	if err := database.addUnversioned(ctx, targetRelpath, options, statuses); err != nil {
		return err
	}
	if options.ShowUpdates {
		if options.Session == nil {
			return fmt.Errorf("show updates requires an RA session")
		}
		if err := database.remoteStatus(ctx, targetPath, targetRelpath, options, statuses); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(statuses))
	for relpath := range statuses {
		paths = append(paths, relpath)
	}
	sort.Strings(paths)
	for _, relpath := range paths {
		status := statuses[relpath]
		if !options.Verbose && status.NodeStatus == StatusNormal && status.PropertyStatus == StatusNormal && !status.WCInfoLocked && !status.Switched && status.Lock == nil {
			continue
		}
		if err := callback(status); err != nil {
			return err
		}
	}
	return nil
}

func (database *Database) remoteStatus(ctx context.Context, targetPath, targetRelpath string, options StatusOptions, statuses map[string]*Status) error {
	editor := &statusEditor{root: database.wcRoot, target: targetRelpath, statuses: statuses}
	revision := svn.InvalidRevnum
	if options.Revision != nil {
		revision = *options.Revision
	}
	reporter, err := options.Session.DoStatus(ctx, "", revision, options.Depth, editor)
	if err != nil {
		return err
	}
	if err := database.Crawl(ctx, targetPath, options.Depth, reporter); err != nil {
		return err
	}
	locks, err := options.Session.GetLocks(ctx, "", options.Depth)
	if err != nil {
		return err
	}
	for _, lock := range locks {
		if lock == nil {
			continue
		}
		repositoryPath := strings.TrimPrefix(lock.Path, "/")
		for _, status := range statuses {
			if status.RepositoryPath == repositoryPath {
				status.RepositoryLock = lock
			}
		}
	}
	return nil
}

type statusEditor struct {
	root     string
	target   string
	statuses map[string]*Status
}

func (editor *statusEditor) SetTargetRevision(context.Context, svn.Revnum) error { return nil }
func (editor *statusEditor) OpenRoot(context.Context, svn.Revnum) (delta.DirEditor, error) {
	return &statusDir{editor: editor}, nil
}
func (editor *statusEditor) CloseEdit(context.Context) error { return nil }
func (editor *statusEditor) AbortEdit(context.Context) error { return nil }

type statusDir struct {
	editor *statusEditor
	path   string
}

func (directory *statusDir) DeleteEntry(_ context.Context, name string, _ svn.Revnum) error {
	directory.editor.mark(path.Join(directory.path, name), svn.NodeUnknown, StatusDeleted)
	return nil
}
func (directory *statusDir) AddDirectory(_ context.Context, name string, _ *delta.CopySource) (delta.DirEditor, error) {
	name = path.Join(directory.path, name)
	directory.editor.mark(name, svn.NodeDir, StatusAdded)
	return &statusDir{editor: directory.editor, path: name}, nil
}
func (directory *statusDir) OpenDirectory(_ context.Context, name string, _ svn.Revnum) (delta.DirEditor, error) {
	return &statusDir{editor: directory.editor, path: path.Join(directory.path, name)}, nil
}
func (directory *statusDir) ChangeProp(_ context.Context, _ string, _ []byte) error {
	directory.editor.mark(directory.path, svn.NodeDir, StatusModified)
	return nil
}
func (directory *statusDir) AbsentDirectory(context.Context, string) error { return nil }
func (directory *statusDir) AddFile(_ context.Context, name string, _ *delta.CopySource) (delta.FileEditor, error) {
	name = path.Join(directory.path, name)
	directory.editor.mark(name, svn.NodeFile, StatusAdded)
	return &statusFile{editor: directory.editor, path: name}, nil
}
func (directory *statusDir) OpenFile(_ context.Context, name string, _ svn.Revnum) (delta.FileEditor, error) {
	name = path.Join(directory.path, name)
	directory.editor.mark(name, svn.NodeFile, StatusModified)
	return &statusFile{editor: directory.editor, path: name}, nil
}
func (directory *statusDir) AbsentFile(context.Context, string) error { return nil }
func (directory *statusDir) Close(context.Context) error              { return nil }

type statusFile struct {
	editor *statusEditor
	path   string
}

func (file *statusFile) ApplyTextDelta(context.Context, *svn.Checksum) (delta.WindowHandler, error) {
	file.editor.mark(file.path, svn.NodeFile, StatusModified)
	return delta.WindowHandlerFunc(func(*delta.Window) error { return nil }), nil
}
func (file *statusFile) ChangeProp(context.Context, string, []byte) error {
	file.editor.mark(file.path, svn.NodeFile, StatusModified)
	return nil
}
func (file *statusFile) Close(context.Context, *svn.Checksum) error { return nil }

func (editor *statusEditor) mark(name string, kind svn.NodeKind, item StatusKind) {
	relpath := path.Join(editor.target, name)
	status := editor.statuses[relpath]
	if status == nil {
		status = &Status{Path: filepath.Join(editor.root, filepath.FromSlash(relpath)), RelativePath: relpath, Kind: kind, NodeStatus: StatusNone, TextStatus: StatusNone, PropertyStatus: StatusNone, RepositoryStatus: StatusNone}
		editor.statuses[relpath] = status
	} else if status.Kind == svn.NodeUnknown && kind != svn.NodeUnknown {
		status.Kind = kind
	}
	status.RepositoryStatus = item
}

func (database *Database) versionedPaths(ctx context.Context, target string, depth svn.Depth) ([]string, error) {
	rows, err := database.sql.QueryContext(ctx, `SELECT local_relpath, kind FROM NODES_CURRENT WHERE wc_id = ? ORDER BY local_relpath`, database.wcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var candidate, kind string
		if err := rows.Scan(&candidate, &kind); err != nil {
			return nil, err
		}
		if withinDepth(target, candidate, depth, kind != svn.NodeDir.String()) {
			result = append(result, candidate)
		}
	}
	return result, rows.Err()
}

func (database *Database) statusForInfo(ctx context.Context, info *Info) (*Status, error) {
	status := &Status{
		Path: info.Path, RelativePath: info.RelativePath, Kind: info.Kind,
		NodeStatus: StatusNormal, TextStatus: StatusNormal, PropertyStatus: StatusNormal,
		Revision: info.Revision, ChangedRevision: info.ChangedRevision, ChangedAuthor: info.ChangedAuthor,
		RepositoryPath: info.RepositoryPath, RepositoryStatus: StatusNone,
		Copied: info.Copied, FileExternal: info.FileExternal, MovedFrom: info.MovedFrom, MovedTo: info.MovedTo,
		Changelist: info.Changelist, Lock: info.Lock,
	}
	switch info.Schedule {
	case ScheduleAdd:
		status.NodeStatus, status.TextStatus = StatusAdded, StatusAdded
	case ScheduleDelete:
		status.NodeStatus, status.TextStatus = StatusDeleted, StatusDeleted
	case ScheduleReplace:
		status.NodeStatus, status.TextStatus = StatusReplaced, StatusReplaced
	}
	if info.Presence == PresenceIncomplete {
		status.NodeStatus = StatusIncomplete
	}
	if info.Conflict != nil {
		status.Conflicted = true
		status.TreeConflicted = info.Conflict.Tree
		if status.NodeStatus == StatusNormal {
			status.NodeStatus = StatusConflicted
		}
	}
	status.PropertyStatus = propertyStatus(info.BaseProperties, info.WorkingProperties)
	if info.Schedule != ScheduleDelete && info.Presence != PresenceNotPresent && info.Presence != PresenceExcluded && info.Presence != PresenceServerExcluded {
		filesystemStatus, err := database.filesystemStatus(ctx, info)
		if err != nil {
			return nil, err
		}
		if filesystemStatus != StatusNormal {
			status.TextStatus = filesystemStatus
			if status.NodeStatus == StatusNormal || filesystemStatus == StatusMissing || filesystemStatus == StatusObstructed {
				status.NodeStatus = filesystemStatus
			}
		}
	}
	status.Switched = database.isSwitched(ctx, info)
	status.WCInfoLocked, _ = database.isWCLocked(ctx, info.RelativePath)
	return status, nil
}

func (database *Database) filesystemStatus(ctx context.Context, info *Info) (StatusKind, error) {
	stat, err := os.Lstat(info.Path)
	if os.IsNotExist(err) {
		return StatusMissing, nil
	}
	if err != nil {
		return StatusNone, err
	}
	if info.Kind == svn.NodeDir {
		if !stat.IsDir() {
			return StatusObstructed, nil
		}
		return StatusNormal, nil
	}
	wantSymlink := len(info.WorkingProperties[props.Special]) != 0
	if stat.IsDir() || stat.Mode()&os.ModeSymlink != 0 != wantSymlink {
		return StatusObstructed, nil
	}
	if info.Schedule == ScheduleAdd && !info.Copied {
		return StatusAdded, nil
	}
	if info.Checksum == nil {
		return StatusNormal, nil
	}
	if !wantSymlink && info.TranslatedSize >= 0 && stat.Size() == info.TranslatedSize && !info.LastModified.IsZero() && stat.ModTime().UnixMicro() == info.LastModified.UnixMicro() {
		return StatusNormal, nil
	}
	equal, err := database.matchesPristine(ctx, info, wantSymlink)
	if err != nil {
		return StatusNone, err
	}
	if equal {
		return StatusNormal, nil
	}
	return StatusModified, nil
}

func (database *Database) matchesPristine(ctx context.Context, info *Info, special bool) (bool, error) {
	pristine, err := database.OpenPristine(ctx, *info.Checksum)
	if err != nil {
		return false, err
	}
	defer pristine.Close()
	expected := sha1.New()
	if _, err := io.Copy(expected, pristine); err != nil {
		return false, err
	}
	actual := sha1.New()
	if special {
		target, err := os.Readlink(info.Path)
		if err != nil {
			return false, err
		}
		_, _ = actual.Write(props.EncodeSpecial(target))
	} else {
		file, err := os.Open(info.Path)
		if err != nil {
			return false, err
		}
		defer file.Close()
		keywordSpec := string(info.WorkingProperties[props.Keywords])
		eolStyle := string(info.WorkingProperties[props.EOLStyle])
		if keywordSpec == "" && eolStyle == "" {
			_, err = io.Copy(actual, file)
			return err == nil && bytes.Equal(expected.Sum(nil), actual.Sum(nil)), err
		}
		keywords := props.ParseKeywords(keywordSpec, props.KeywordContext{})
		if err := props.DetranslateFile(file, actual, "LF", keywords, true); err != nil {
			return false, err
		}
	}
	return bytes.Equal(expected.Sum(nil), actual.Sum(nil)), nil
}

func propertyStatus(base, working svn.Props) StatusKind {
	if len(base) != len(working) {
		return StatusModified
	}
	for name, value := range base {
		if !bytes.Equal(value, working[name]) {
			return StatusModified
		}
	}
	return StatusNormal
}

func (database *Database) addUnversioned(ctx context.Context, target string, options StatusOptions, statuses map[string]*Status) error {
	root := filepath.Join(database.wcRoot, filepath.FromSlash(target))
	return filepath.WalkDir(root, func(localPath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == ".svn" {
			return filepath.SkipDir
		}
		relative, err := filepath.Rel(database.wcRoot, localPath)
		if err != nil {
			return err
		}
		relpath := filepath.ToSlash(relative)
		if relpath == "." {
			relpath = ""
		}
		if !withinDepth(target, relpath, options.Depth, !entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := statuses[relpath]; ok {
			return nil
		}
		if entry.IsDir() && relpath != target {
			if info, err := os.Stat(filepath.Join(localPath, ".svn", "wc.db")); err == nil && !info.IsDir() {
				statuses[relpath] = &Status{Path: localPath, RelativePath: relpath, Kind: svn.NodeDir, NodeStatus: StatusExternal, TextStatus: StatusExternal, PropertyStatus: StatusNone, RepositoryStatus: StatusNone}
				return filepath.SkipDir
			}
		}
		ignored, err := database.isIgnored(ctx, relpath, options.GlobalIgnores)
		if err != nil {
			return err
		}
		if ignored && !options.NoIgnore {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		kind := svn.NodeFile
		if entry.IsDir() {
			kind = svn.NodeDir
		} else if entry.Type()&os.ModeSymlink != 0 {
			kind = svn.NodeSymlink
		}
		item := StatusUnversioned
		if ignored {
			item = StatusIgnored
		}
		statuses[relpath] = &Status{Path: localPath, RelativePath: relpath, Kind: kind, NodeStatus: item, TextStatus: item, PropertyStatus: StatusNone, RepositoryStatus: StatusNone}
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
}

func (database *Database) isIgnored(ctx context.Context, relpath string, global []string) (bool, error) {
	name := path.Base(relpath)
	patterns := append([]string(nil), global...)
	parent := path.Dir(relpath)
	if parent == "." {
		parent = ""
	}
	if parentInfo, err := database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(parent))); err == nil {
		patterns = append(patterns, propertyIgnorePatterns(parentInfo.WorkingProperties[props.Ignore])...)
	}
	for ancestor := parent; ; ancestor = path.Dir(ancestor) {
		if info, err := database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(ancestor))); err == nil {
			patterns = append(patterns, propertyIgnorePatterns(info.WorkingProperties[props.GlobalIgnores])...)
		}
		if ancestor == "" || ancestor == "." {
			break
		}
	}
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, name); matched {
			return true, nil
		}
	}
	return false, nil
}

func propertyIgnorePatterns(value []byte) []string {
	lines := strings.Split(strings.ReplaceAll(string(value), "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if pattern := strings.TrimSpace(strings.TrimSuffix(line, "\r")); pattern != "" {
			result = append(result, pattern)
		}
	}
	return result
}

func (database *Database) isSwitched(ctx context.Context, info *Info) bool {
	if info.RelativePath == "" || info.Copied || info.Schedule == ScheduleAdd {
		return false
	}
	parent := path.Dir(info.RelativePath)
	if parent == "." {
		parent = ""
	}
	parentInfo, err := database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(parent)))
	if err != nil {
		return false
	}
	return info.RepositoryPath != path.Join(parentInfo.RepositoryPath, path.Base(info.RelativePath))
}

func (database *Database) isWCLocked(ctx context.Context, relpath string) (bool, error) {
	rows, err := database.sql.QueryContext(ctx, `SELECT local_dir_relpath, locked_levels FROM WC_LOCK WHERE wc_id = ?`, database.wcID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var lockPath string
		var levels int
		if err := rows.Scan(&lockPath, &levels); err != nil {
			return false, err
		}
		if relpath == lockPath {
			return true, nil
		}
		prefix := lockPath
		if prefix != "" {
			prefix += "/"
		}
		if strings.HasPrefix(relpath, prefix) {
			remainder := strings.TrimPrefix(relpath, prefix)
			distance := strings.Count(remainder, "/") + 1
			if levels < 0 || distance <= levels {
				return true, nil
			}
		}
	}
	return false, rows.Err()
}

func withinDepth(target, candidate string, depth svn.Depth, file bool) bool {
	if candidate == target {
		return true
	}
	prefix := target
	if prefix != "" {
		prefix += "/"
	}
	if !strings.HasPrefix(candidate, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(candidate, prefix)
	level := strings.Count(remainder, "/") + 1
	switch depth {
	case svn.DepthEmpty, svn.DepthExclude:
		return false
	case svn.DepthFiles:
		return level == 1 && file
	case svn.DepthImmediates:
		return level == 1
	default:
		return true
	}
}
