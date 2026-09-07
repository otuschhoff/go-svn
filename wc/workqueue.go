package wc

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/skel"
)

const (
	workFileInstall    = "file-install"
	workFileRemove     = "file-remove"
	workDirRemove      = "dir-remove"
	workFileMove       = "file-move"
	workDirInstall     = "dir-install"
	workSyncFlags      = "sync-file-flags"
	workPrejInstall    = "prej-install"
	workRecordFileInfo = "record-fileinfo"
	workPostUpgrade    = "postupgrade"
)

func WorkItem(operation string, arguments ...string) *skel.Node {
	children := make([]*skel.Node, 0, len(arguments)+1)
	children = append(children, skel.NewString(operation))
	for _, argument := range arguments {
		children = append(children, skel.NewString(argument))
	}
	return skel.NewList(children...)
}

func FileInstallWork(relpath string, useCommitTimes, recordFileInfo bool, sourceRelpath string) *skel.Node {
	arguments := []string{relpath, boolString(useCommitTimes), boolString(recordFileInfo)}
	if sourceRelpath != "" {
		arguments = append(arguments, sourceRelpath)
	}
	return WorkItem(workFileInstall, arguments...)
}

func FileRemoveWork(relpath string) *skel.Node { return WorkItem(workFileRemove, relpath) }
func DirectoryRemoveWork(relpath string, recursive bool) *skel.Node {
	if recursive {
		return WorkItem(workDirRemove, relpath, "1")
	}
	return WorkItem(workDirRemove, relpath)
}
func FileMoveWork(source, destination string) *skel.Node {
	return WorkItem(workFileMove, source, destination)
}
func DirectoryInstallWork(relpath string) *skel.Node { return WorkItem(workDirInstall, relpath) }
func SyncFileFlagsWork(relpath string) *skel.Node    { return WorkItem(workSyncFlags, relpath) }
func PropertyRejectInstallWork(relpath string) *skel.Node {
	return WorkItem(workPrejInstall, relpath)
}
func RecordFileInfoWork(relpath string) *skel.Node { return WorkItem(workRecordFileInfo, relpath) }
func PostUpgradeWork() *skel.Node                  { return WorkItem(workPostUpgrade) }

func (database *Database) Enqueue(ctx context.Context, item *skel.Node) (int64, error) {
	if !database.writable {
		return 0, fmt.Errorf("%w: database is read-only", svn.ErrWCNotLocked)
	}
	data, err := item.MarshalBinary()
	if err != nil {
		return 0, err
	}
	result, err := database.sql.ExecContext(ctx, `INSERT INTO WORK_QUEUE (work) VALUES (?)`, data)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (database *Database) RunWorkQueue(ctx context.Context) error {
	if !database.writable {
		return fmt.Errorf("%w: database is read-only", svn.ErrWCNotLocked)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var id int64
		var data []byte
		err := database.sql.QueryRowContext(ctx, `SELECT id, work FROM WORK_QUEUE ORDER BY id LIMIT 1`).Scan(&id, &data)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		item, err := skel.Parse(data)
		if err != nil {
			return fmt.Errorf("%w: work item %d: %v", svn.ErrWCBadAdmLog, id, err)
		}
		if err := database.runWorkItem(ctx, item); err != nil {
			return fmt.Errorf("%w: work item %d: %v", svn.ErrWCBadAdmLog, id, err)
		}
		if _, err := database.sql.ExecContext(ctx, `DELETE FROM WORK_QUEUE WHERE id = ?`, id); err != nil {
			return err
		}
	}
}

func (database *Database) runWorkItem(ctx context.Context, item *skel.Node) error {
	if !item.IsList() || len(item.Children) == 0 {
		return fmt.Errorf("invalid work item")
	}
	if item.Children[0].IsList() {
		for _, child := range item.Children {
			if err := database.runWorkItem(ctx, child); err != nil {
				return err
			}
		}
		return nil
	}
	if !item.Children[0].IsAtom() {
		return fmt.Errorf("invalid work item operation")
	}
	arguments := item.Children[1:]
	operation := string(item.Children[0].Atom)
	switch operation {
	case workFileInstall:
		if len(arguments) < 3 || len(arguments) > 4 {
			return fmt.Errorf("invalid file-install")
		}
		useCommitTimes, err := workBool(arguments[1])
		if err != nil {
			return err
		}
		record, err := workBool(arguments[2])
		if err != nil {
			return err
		}
		source := ""
		if len(arguments) == 4 {
			source = string(arguments[3].Atom)
		}
		return database.installFile(ctx, string(arguments[0].Atom), source, useCommitTimes, record)
	case workFileRemove:
		return removeFile(database.workPath(arguments, 1, 0))
	case workDirRemove:
		if len(arguments) < 1 || len(arguments) > 2 {
			return fmt.Errorf("invalid dir-remove")
		}
		recursive := false
		var err error
		if len(arguments) == 2 {
			recursive, err = workBool(arguments[1])
		}
		if err != nil {
			return err
		}
		name, err := database.resolveWorkPath(string(arguments[0].Atom))
		if err != nil {
			return err
		}
		return removeDirectory(name, recursive)
	case workFileMove:
		if len(arguments) != 2 {
			return fmt.Errorf("invalid file-move")
		}
		return movePath(database.workPath(arguments, 2, 0), database.workPath(arguments, 2, 1))
	case workDirInstall:
		return os.MkdirAll(database.workPath(arguments, 1, 0), 0o755)
	case workSyncFlags:
		return database.syncFileFlags(ctx, string(arguments[0].Atom))
	case workPrejInstall:
		return database.installPropertyReject(ctx, string(arguments[0].Atom))
	case workRecordFileInfo:
		if len(arguments) < 1 || len(arguments) > 2 {
			return fmt.Errorf("invalid record-fileinfo")
		}
		if len(arguments) == 2 {
			microseconds, err := strconv.ParseInt(string(arguments[1].Atom), 10, 64)
			if err != nil {
				return err
			}
			name, err := database.resolveWorkPath(string(arguments[0].Atom))
			if err != nil {
				return err
			}
			stamp := time.UnixMicro(microseconds)
			if err := os.Chtimes(name, stamp, stamp); err != nil {
				return err
			}
		}
		return database.recordFileInfo(ctx, string(arguments[0].Atom))
	case workPostUpgrade:
		for _, name := range []string{"format", "entries"} {
			if err := os.WriteFile(filepath.Join(database.wcRoot, ".svn", name), []byte("12\n"), 0o644); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unrecognized operation %q", operation)
	}
}

func (database *Database) workPath(arguments []*skel.Node, count, index int) string {
	if len(arguments) != count || !arguments[index].IsAtom() {
		return filepath.Join(database.wcRoot, ".invalid-work-item")
	}
	resolved, err := database.resolveWorkPath(string(arguments[index].Atom))
	if err != nil {
		return filepath.Join(database.wcRoot, ".invalid-work-item")
	}
	return resolved
}

func (database *Database) resolveWorkPath(relpath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relpath))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("work path %q escapes working copy", relpath)
	}
	resolved := filepath.Join(database.wcRoot, clean)
	current := database.wcRoot
	parent, err := filepath.Rel(database.wcRoot, filepath.Dir(resolved))
	if err != nil {
		return "", err
	}
	if parent != "." {
		for _, component := range strings.Split(parent, string(filepath.Separator)) {
			current = filepath.Join(current, component)
			info, err := os.Lstat(current)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return "", err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("work path %q traverses symlink %s", relpath, current)
			}
		}
	}
	return resolved, nil
}

func workBool(node *skel.Node) (bool, error) {
	value, err := strconv.ParseInt(string(node.Atom), 10, 64)
	return value != 0, err
}

func removeFile(name string) error {
	err := os.Remove(name)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func removeDirectory(name string, recursive bool) error {
	var err error
	if recursive {
		err = os.RemoveAll(name)
	} else {
		err = os.Remove(name)
	}
	if os.IsNotExist(err) || (!recursive && err != nil) {
		return nil
	}
	return err
}

func movePath(source, destination string) error {
	if _, err := os.Lstat(source); os.IsNotExist(err) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(destination)
	return os.Rename(source, destination)
}

func (database *Database) installFile(ctx context.Context, relpath, sourceRelpath string, useCommitTimes, record bool) error {
	info, err := database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(relpath)))
	if err != nil {
		return err
	}
	var source io.ReadCloser
	if sourceRelpath != "" {
		source, err = os.Open(filepath.Join(database.wcRoot, filepath.FromSlash(sourceRelpath)))
	} else {
		source, err = database.OpenTextBase(ctx, info.Path)
	}
	if err != nil {
		return err
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(info.Path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Join(database.wcRoot, ".svn", "tmp"), "install-")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	special := len(info.WorkingProperties[props.Special]) != 0
	if special {
		data, readErr := io.ReadAll(source)
		if readErr == nil {
			_, readErr = temporary.Write(data)
		}
		err = readErr
	} else {
		keywords := props.ParseKeywords(string(info.WorkingProperties[props.Keywords]), props.KeywordContext{
			Author: info.ChangedAuthor, Basename: filepath.Base(info.Path), Date: info.ChangedDate,
			Path: info.RepositoryPath, Revision: info.ChangedRevision, RootURL: info.RepositoryRoot, URL: info.URL,
		})
		err = props.Translate(source, temporary, string(info.WorkingProperties[props.EOLStyle]), keywords, true, true)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	_ = os.Remove(info.Path)
	if special {
		data, err := os.ReadFile(temporaryName)
		if err != nil {
			return err
		}
		target, err := props.DecodeSpecial(data)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, info.Path); err != nil {
			return err
		}
	} else if err := os.Rename(temporaryName, info.Path); err != nil {
		return err
	}
	if useCommitTimes && !info.ChangedDate.IsZero() && !special {
		_ = os.Chtimes(info.Path, info.ChangedDate, info.ChangedDate)
	}
	if err := database.syncFileFlags(ctx, relpath); err != nil {
		return err
	}
	if record {
		return database.recordFileInfo(ctx, relpath)
	}
	return nil
}

func (database *Database) syncFileFlags(ctx context.Context, relpath string) error {
	info, err := database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(relpath)))
	if err != nil {
		return err
	}
	if len(info.WorkingProperties[props.Special]) != 0 {
		return nil
	}
	mode := os.FileMode(0o644)
	if len(info.WorkingProperties[props.Executable]) != 0 {
		mode = 0o755
	}
	if len(info.WorkingProperties[props.NeedsLock]) != 0 && info.Lock == nil {
		mode &^= 0o222
	}
	return os.Chmod(info.Path, mode)
}

func (database *Database) recordFileInfo(ctx context.Context, relpath string) error {
	stat, err := os.Lstat(filepath.Join(database.wcRoot, filepath.FromSlash(relpath)))
	if err != nil {
		return err
	}
	_, err = database.sql.ExecContext(ctx, `UPDATE NODES SET translated_size = ?, last_mod_time = ? WHERE wc_id = ? AND local_relpath = ? AND op_depth = (SELECT MAX(op_depth) FROM NODES WHERE wc_id = ? AND local_relpath = ?)`, stat.Size(), stat.ModTime().UnixMicro(), database.wcID, relpath, database.wcID, relpath)
	return err
}

func (database *Database) installPropertyReject(ctx context.Context, relpath string) error {
	actual, err := database.readActual(ctx, relpath)
	if err != nil || actual == nil || actual.conflict == nil || actual.conflict.PropertyPath == "" {
		return err
	}
	name := actual.conflict.PropertyPath
	if !filepath.IsAbs(name) {
		name = filepath.Join(filepath.Dir(filepath.Join(database.wcRoot, filepath.FromSlash(relpath))), name)
	}
	return os.WriteFile(name, []byte("Property conflicts remain unresolved.\n"), 0o644)
}

type CleanupOptions struct {
	RemoveUnversioned bool
	RemoveIgnored     bool
	VacuumPristines   bool
	IncludeExternals  bool
}

func (database *Database) Cleanup(ctx context.Context) error {
	return database.CleanupWithOptions(ctx, CleanupOptions{VacuumPristines: true})
}

func (database *Database) CleanupWithOptions(ctx context.Context, options CleanupOptions) error {
	if !database.writable {
		return fmt.Errorf("%w: database is read-only", svn.ErrWCNotLocked)
	}
	if err := database.RunWorkQueue(ctx); err != nil {
		return err
	}
	if _, err := database.sql.ExecContext(ctx, `DELETE FROM WC_LOCK WHERE wc_id = ?`, database.wcID); err != nil {
		return err
	}
	if options.RemoveUnversioned || options.RemoveIgnored || options.IncludeExternals {
		var removals []string
		var externals []string
		err := database.Status(ctx, database.wcRoot, StatusOptions{Depth: svn.DepthInfinity, NoIgnore: true}, func(status *Status) error {
			switch status.NodeStatus {
			case StatusUnversioned:
				if options.RemoveUnversioned {
					removals = append(removals, status.Path)
				}
			case StatusIgnored:
				if options.RemoveIgnored {
					removals = append(removals, status.Path)
				}
			case StatusExternal:
				if options.IncludeExternals {
					externals = append(externals, status.Path)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		sort.Slice(removals, func(left, right int) bool { return len(removals[left]) > len(removals[right]) })
		for _, name := range removals {
			if err := os.RemoveAll(name); err != nil {
				return err
			}
		}
		for _, name := range externals {
			external, err := Open(ctx, name, Options{Writable: true})
			if err != nil {
				return err
			}
			err = external.CleanupWithOptions(ctx, options)
			external.Close()
			if err != nil {
				return err
			}
		}
	}
	if !options.VacuumPristines {
		return nil
	}
	rows, err := database.sql.QueryContext(ctx, `SELECT checksum FROM PRISTINE WHERE refcount <= 0`)
	if err != nil {
		return err
	}
	var checksums []string
	for rows.Next() {
		var checksum string
		if err := rows.Scan(&checksum); err != nil {
			rows.Close()
			return err
		}
		checksums = append(checksums, checksum)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, serialized := range checksums {
		checksum, err := svn.ParseChecksum(serialized)
		if err == nil {
			hexValue := checksum.Hex()
			_ = os.Remove(filepath.Join(database.wcRoot, ".svn", "pristine", hexValue[:2], hexValue+".svn-base"))
		}
		_, _ = database.sql.ExecContext(ctx, `DELETE FROM PRISTINE WHERE checksum = ? AND refcount <= 0`, serialized)
	}
	return nil
}

func propertyBytes(properties svn.Props) []byte {
	data, _ := skel.PropsToProplist(properties).MarshalBinary()
	return data
}
