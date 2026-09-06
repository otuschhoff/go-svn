package wc

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/otuschhoff/go-svn/svn"
)

type ExternalDefinition struct {
	LocalPath         string
	URL               string
	OperativeRevision svn.Revision
	PegRevision       svn.Revision
}

func (database *Database) InstallFileExternal(ctx context.Context, targetPath, repositoryRoot, repositoryUUID, repositoryPath string, revision svn.Revnum, properties svn.Props, contents []byte, changedRevision svn.Revnum, changedDate time.Time, changedAuthor string, force bool) error {
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	if _, err := database.Info(ctx, targetPath); err != nil {
		if _, statErr := os.Lstat(targetPath); statErr == nil && !force {
			return fmt.Errorf("%w: %s", svn.ErrWCObstructedUpdate, targetPath)
		}
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Join(database.wcRoot, ".svn", "tmp"), "file-external-")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err = temporary.Write(contents); err == nil {
		err = temporary.Close()
	} else {
		temporary.Close()
	}
	if err != nil {
		return err
	}
	sha1Checksum, md5Checksum, size, err := checksumsForFile(name)
	if err != nil {
		return err
	}
	if err := database.installPristine(ctx, name, sha1Checksum, md5Checksum, size); err != nil {
		return err
	}
	parent := path.Dir(relpath)
	if parent == "." {
		parent = ""
	}
	changedMicros := int64(0)
	if !changedDate.IsZero() {
		changedMicros = changedDate.UnixMicro()
	}
	if _, err := database.sql.ExecContext(ctx, `INSERT OR IGNORE INTO REPOSITORY (root, uuid) VALUES (?, ?)`, strings.TrimSuffix(repositoryRoot, "/"), repositoryUUID); err != nil {
		return err
	}
	var repositoryID int64
	if err := database.sql.QueryRowContext(ctx, `SELECT id FROM REPOSITORY WHERE root=?`, strings.TrimSuffix(repositoryRoot, "/")).Scan(&repositoryID); err != nil {
		return err
	}
	_, err = database.sql.ExecContext(ctx, `INSERT INTO NODES
		(wc_id, local_relpath, op_depth, parent_relpath, repos_id, repos_path, revision, presence, kind, properties,
		checksum, changed_revision, changed_date, changed_author, file_external)
		VALUES (?, ?, 0, ?, ?, ?, ?, 'normal', 'file', ?, ?, ?, NULLIF(?, 0), NULLIF(?, ''), 1)
		ON CONFLICT(wc_id, local_relpath, op_depth) DO UPDATE SET repos_id=excluded.repos_id, repos_path=excluded.repos_path,
		revision=excluded.revision, presence='normal', kind='file', properties=excluded.properties, checksum=excluded.checksum,
		changed_revision=excluded.changed_revision, changed_date=excluded.changed_date, changed_author=excluded.changed_author,
		file_external=1, translated_size=NULL, last_mod_time=NULL`, database.wcID, relpath, nullableParent(relpath, parent),
		repositoryID, strings.TrimPrefix(repositoryPath, "/"), revision, propertyBytes(properties), sha1Checksum.Serialize(),
		changedRevision, changedMicros, changedAuthor)
	if err != nil {
		return err
	}
	if _, err := database.sql.ExecContext(ctx, `UPDATE EXTERNALS SET kind='file', repos_id=? WHERE wc_id=? AND local_relpath=?`, repositoryID, database.wcID, relpath); err != nil {
		return err
	}
	if _, err := database.Enqueue(ctx, WorkItem(workFileInstall, relpath, "0", "1")); err != nil {
		return err
	}
	return database.RunWorkQueue(ctx)
}

func ParseExternals(value string) ([]ExternalDefinition, error) {
	var definitions []ExternalDefinition
	for lineNumber, rawLine := range strings.Split(value, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		definition, err := parseExternalLine(line)
		if err != nil {
			return nil, fmt.Errorf("invalid svn:externals line %d: %w", lineNumber+1, err)
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func parseExternalLine(line string) (ExternalDefinition, error) {
	fields, err := splitExternalFields(line)
	if err != nil {
		return ExternalDefinition{}, err
	}
	var operative svn.Revision
	for index := 0; index < len(fields); {
		if fields[index] == "-r" {
			if index+1 >= len(fields) {
				return ExternalDefinition{}, fmt.Errorf("-r requires a revision")
			}
			operative, err = svn.ParseRevision(fields[index+1])
			fields = append(fields[:index], fields[index+2:]...)
			break
		}
		if strings.HasPrefix(fields[index], "-r") && len(fields[index]) > 2 {
			operative, err = svn.ParseRevision(fields[index][2:])
			fields = append(fields[:index], fields[index+1:]...)
			break
		}
		index++
	}
	if err != nil {
		return ExternalDefinition{}, err
	}
	if len(fields) != 2 {
		return ExternalDefinition{}, fmt.Errorf("expected URL and local path")
	}
	remote, local := fields[0], fields[1]
	if !externalURLLike(remote) && externalURLLike(local) {
		local, remote = remote, local
	}
	local = path.Clean(local)
	if local == "." || path.IsAbs(local) || local == ".." || strings.HasPrefix(local, "../") {
		return ExternalDefinition{}, fmt.Errorf("invalid local path %q", local)
	}
	remote, peg, err := splitExternalPeg(remote)
	if err != nil {
		return ExternalDefinition{}, err
	}
	return ExternalDefinition{LocalPath: local, URL: remote, OperativeRevision: operative, PegRevision: peg}, nil
}

func splitExternalFields(line string) ([]string, error) {
	var fields []string
	var field strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if field.Len() != 0 {
			fields = append(fields, field.String())
			field.Reset()
		}
	}
	for _, character := range line {
		if escaped {
			field.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else {
				field.WriteRune(character)
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if unicode.IsSpace(character) {
			flush()
			continue
		}
		field.WriteRune(character)
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	flush()
	return fields, nil
}

func splitExternalPeg(remote string) (string, svn.Revision, error) {
	index := strings.LastIndex(remote, "@")
	if index < 0 {
		return remote, svn.Revision{}, nil
	}
	if index == len(remote)-1 {
		return remote[:index], svn.Revision{}, nil
	}
	peg, err := svn.ParseRevision(remote[index+1:])
	if err != nil {
		return "", svn.Revision{}, fmt.Errorf("invalid peg revision: %w", err)
	}
	return remote[:index], peg, nil
}

func externalURLLike(value string) bool {
	return strings.Contains(value, "://") || strings.HasPrefix(value, "^") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "./")
}

type External struct {
	LocalPath           string
	ParentPath          string
	RepositoryID        int64
	Presence            Presence
	Kind                svn.NodeKind
	DefinitionPath      string
	DefinitionReposPath string
	OperativeRevision   svn.Revnum
	PegRevision         svn.Revnum
	Operative           svn.Revision
	Peg                 svn.Revision
}

func (database *Database) Externals(ctx context.Context) ([]External, error) {
	rows, err := database.sql.QueryContext(ctx, `SELECT local_relpath, parent_relpath, COALESCE(repos_id, 0),
		presence, kind, def_local_relpath, def_repos_relpath,
		COALESCE(def_operational_revision, ''), COALESCE(def_revision, '')
		FROM EXTERNALS WHERE wc_id = ? ORDER BY local_relpath`, database.wcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []External
	for rows.Next() {
		var external External
		var kind string
		var operative, peg string
		if err := rows.Scan(&external.LocalPath, &external.ParentPath, &external.RepositoryID, &external.Presence, &kind, &external.DefinitionPath, &external.DefinitionReposPath, &operative, &peg); err != nil {
			return nil, err
		}
		if operative != "" {
			external.Operative, err = svn.ParseRevision(operative)
			if err != nil {
				return nil, fmt.Errorf("%w: external operative revision: %v", svn.ErrWCCorrupt, err)
			}
			if external.Operative.Kind == svn.RevisionNumber {
				external.OperativeRevision = external.Operative.Number
			} else {
				external.OperativeRevision = svn.InvalidRevnum
			}
		} else {
			external.OperativeRevision = svn.InvalidRevnum
		}
		if peg != "" {
			external.Peg, err = svn.ParseRevision(peg)
			if err != nil {
				return nil, fmt.Errorf("%w: external peg revision: %v", svn.ErrWCCorrupt, err)
			}
			if external.Peg.Kind == svn.RevisionNumber {
				external.PegRevision = external.Peg.Number
			} else {
				external.PegRevision = svn.InvalidRevnum
			}
		} else {
			external.PegRevision = svn.InvalidRevnum
		}
		external.Kind, err = svn.ParseNodeKind(kind)
		if err != nil {
			return nil, fmt.Errorf("%w: external kind: %v", svn.ErrWCCorrupt, err)
		}
		result = append(result, external)
	}
	return result, rows.Err()
}

func (database *Database) RemoveExternal(ctx context.Context, external External, force bool) error {
	targetPath := filepath.Join(database.wcRoot, filepath.FromSlash(external.LocalPath))
	if external.Kind == svn.NodeFile {
		if info, err := database.Info(ctx, targetPath); err == nil {
			status, statusErr := database.statusForInfo(ctx, info)
			if statusErr != nil {
				return statusErr
			}
			if !force && (status.TextStatus == StatusModified || status.PropertyStatus == StatusModified || status.Conflicted) {
				return fmt.Errorf("%w: modified file external %s", svn.ErrWCLeftLocalMod, targetPath)
			}
		}
		if _, err := database.sql.ExecContext(ctx, `DELETE FROM NODES WHERE wc_id=? AND local_relpath=? AND file_external=1`, database.wcID, external.LocalPath); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(targetPath); err != nil {
		return err
	}
	_, err := database.sql.ExecContext(ctx, `DELETE FROM EXTERNALS WHERE wc_id=? AND local_relpath=?`, database.wcID, external.LocalPath)
	return err
}
