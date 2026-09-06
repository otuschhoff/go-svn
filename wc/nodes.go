package wc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/svn"
)

type Presence string

const (
	PresenceNormal         Presence = "normal"
	PresenceIncomplete     Presence = "incomplete"
	PresenceNotPresent     Presence = "not-present"
	PresenceExcluded       Presence = "excluded"
	PresenceServerExcluded Presence = "server-excluded"
	PresenceBaseDeleted    Presence = "base-deleted"
)

type Schedule string

const (
	ScheduleNormal  Schedule = "normal"
	ScheduleAdd     Schedule = "add"
	ScheduleDelete  Schedule = "delete"
	ScheduleReplace Schedule = "replace"
)

type Info struct {
	Path              string
	RelativePath      string
	Kind              svn.NodeKind
	Presence          Presence
	Schedule          Schedule
	Revision          svn.Revnum
	RepositoryPath    string
	URL               string
	RepositoryRoot    string
	RepositoryUUID    string
	Depth             svn.Depth
	OperationDepth    int
	ChangedRevision   svn.Revnum
	ChangedDate       time.Time
	ChangedAuthor     string
	Checksum          *svn.Checksum
	SymlinkTarget     string
	TranslatedSize    int64
	LastModified      time.Time
	Copied            bool
	CopyFromPath      string
	CopyFromRevision  svn.Revnum
	MovedFrom         string
	MovedTo           string
	FileExternal      bool
	WorkingProperties svn.Props
	BaseProperties    svn.Props
	Changelist        string
	Conflict          *Conflict
	Lock              *svn.Lock
}

type nodeRow struct {
	relpath, reposPath, presence, kind, properties, depth, checksum string
	opDepth                                                         int
	revision, changedRevision                                       svn.Revnum
	symlinkTarget, changedAuthor, movedTo                           string
	changedDate, translatedSize, lastModified                       int64
	movedHere, fileExternal                                         bool
	reposID                                                         int64
}

const nodeColumns = `local_relpath, op_depth, COALESCE(repos_id, 0), COALESCE(repos_path, ''),
	COALESCE(revision, -1), presence, COALESCE(moved_here, 0), COALESCE(moved_to, ''), kind,
	COALESCE(properties, X''), COALESCE(depth, ''), COALESCE(checksum, ''), COALESCE(symlink_target, ''),
	COALESCE(changed_revision, -1), COALESCE(changed_date, 0), COALESCE(changed_author, ''),
	COALESCE(translated_size, -1), COALESCE(last_mod_time, 0), COALESCE(file_external, 0)`

func (database *Database) Info(ctx context.Context, targetPath string) (*Info, error) {
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return nil, err
	}
	current, err := database.readNode(ctx, "NODES_CURRENT", relpath)
	if err != nil {
		return nil, err
	}
	base, baseErr := database.readNode(ctx, "NODES_BASE", relpath)
	if baseErr != nil && !errors.Is(baseErr, svn.ErrWCPathNotFound) {
		return nil, baseErr
	}
	actual, err := database.readActual(ctx, relpath)
	if err != nil {
		return nil, err
	}
	return database.infoFromRows(ctx, current, base, actual)
}

func (database *Database) readNode(ctx context.Context, table, relpath string) (*nodeRow, error) {
	query := `SELECT ` + nodeColumns + ` FROM ` + table + ` WHERE wc_id = ? AND local_relpath = ?`
	row := &nodeRow{}
	err := database.sql.QueryRowContext(ctx, query, database.wcID, relpath).Scan(
		&row.relpath, &row.opDepth, &row.reposID, &row.reposPath, &row.revision, &row.presence,
		&row.movedHere, &row.movedTo, &row.kind, &row.properties, &row.depth, &row.checksum,
		&row.symlinkTarget, &row.changedRevision, &row.changedDate, &row.changedAuthor,
		&row.translatedSize, &row.lastModified, &row.fileExternal,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s", svn.ErrWCPathNotFound, relpath)
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (database *Database) infoFromRows(ctx context.Context, current, base *nodeRow, actual *actualRow) (*Info, error) {
	metadata := current
	if current.presence == string(PresenceBaseDeleted) && base != nil {
		metadata = base
	}
	kind, err := svn.ParseNodeKind(metadata.kind)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrWCCorrupt, err)
	}
	depth := svn.DepthInfinity
	if metadata.depth != "" {
		depth, err = svn.ParseDepth(metadata.depth)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", svn.ErrWCCorrupt, err)
		}
	}
	properties, err := parseProperties([]byte(metadata.properties))
	if err != nil {
		return nil, err
	}
	baseProperties := properties.Clone()
	if base != nil {
		baseProperties, err = parseProperties([]byte(base.properties))
		if err != nil {
			return nil, err
		}
	}
	if actual != nil && actual.properties != nil {
		properties, err = parseProperties(actual.properties)
		if err != nil {
			return nil, err
		}
	}
	copied := current.opDepth > 0 && current.revision.IsValid()
	info := &Info{
		Path: filepath.Join(database.wcRoot, filepath.FromSlash(current.relpath)), RelativePath: current.relpath,
		Kind: kind, Presence: Presence(current.presence), Revision: metadata.revision,
		RepositoryPath: metadata.reposPath, RepositoryRoot: database.repository.Root, RepositoryUUID: database.repository.UUID,
		Depth: depth, OperationDepth: current.opDepth, ChangedRevision: metadata.changedRevision,
		ChangedAuthor: metadata.changedAuthor, SymlinkTarget: metadata.symlinkTarget,
		TranslatedSize: metadata.translatedSize, Copied: copied,
		CopyFromRevision: svn.InvalidRevnum,
		FileExternal:     metadata.fileExternal, WorkingProperties: properties, BaseProperties: baseProperties,
	}
	if copied {
		info.CopyFromPath, info.CopyFromRevision = metadata.reposPath, metadata.revision
	}
	info.Schedule = scheduleFor(current, base)
	if metadata.changedDate != 0 {
		info.ChangedDate = time.UnixMicro(metadata.changedDate).UTC()
	}
	if metadata.lastModified != 0 {
		info.LastModified = time.UnixMicro(metadata.lastModified)
	}
	if metadata.checksum != "" {
		checksum, err := svn.ParseChecksum(metadata.checksum)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", svn.ErrWCCorrupt, err)
		}
		info.Checksum = &checksum
	}
	if metadata.reposPath != "" {
		baseURL, err := url.Parse(database.repository.Root)
		if err != nil {
			return nil, err
		}
		baseURL.Path = strings.TrimSuffix(baseURL.Path, "/") + "/" + strings.TrimPrefix(metadata.reposPath, "/")
		info.URL = baseURL.String()
	}
	if current.movedTo != "" {
		info.MovedTo = filepath.Join(database.wcRoot, filepath.FromSlash(current.movedTo))
	}
	if current.movedHere {
		var movedFrom string
		err := database.sql.QueryRowContext(ctx, `SELECT local_relpath FROM NODES WHERE wc_id = ? AND moved_to = ? ORDER BY op_depth DESC LIMIT 1`, database.wcID, current.relpath).Scan(&movedFrom)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if movedFrom != "" {
			info.MovedFrom = filepath.Join(database.wcRoot, filepath.FromSlash(movedFrom))
		}
	}
	if actual != nil {
		info.Changelist, info.Conflict = actual.changelist, actual.conflict
		if info.Conflict != nil {
			parent := filepath.Dir(info.Path)
			info.Conflict.OldPath = conflictMarkerPath(parent, info.Conflict.OldPath)
			info.Conflict.NewPath = conflictMarkerPath(parent, info.Conflict.NewPath)
			info.Conflict.WorkingPath = conflictMarkerPath(parent, info.Conflict.WorkingPath)
			info.Conflict.PropertyPath = conflictMarkerPath(parent, info.Conflict.PropertyPath)
		}
	}
	info.Lock, err = database.readLock(ctx, metadata.reposID, metadata.reposPath)
	return info, err
}

func conflictMarkerPath(parent, marker string) string {
	if marker == "" || filepath.IsAbs(marker) {
		return marker
	}
	return filepath.Join(parent, filepath.FromSlash(marker))
}

func scheduleFor(current, base *nodeRow) Schedule {
	if current.presence == string(PresenceBaseDeleted) {
		return ScheduleDelete
	}
	if current.opDepth == 0 {
		return ScheduleNormal
	}
	if base != nil && base.presence != string(PresenceNotPresent) {
		return ScheduleReplace
	}
	return ScheduleAdd
}
