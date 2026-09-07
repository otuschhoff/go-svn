package client

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/wc"
)

type InfoOptions struct {
	PegRevision svn.Revision
	Revision    svn.Revision
}

type Info struct {
	Path            string
	URL             string
	RepositoryRoot  string
	RepositoryUUID  string
	Kind            svn.NodeKind
	Revision        svn.Revnum
	Size            int64
	HasProperties   bool
	ChangedRevision svn.Revnum
	ChangedDate     time.Time
	ChangedAuthor   string
	Lock            *svn.Lock
	WorkingCopy     *wc.Info
}

type resolvedTarget struct {
	session        ra.Session
	path           string
	url            string
	repositoryRoot string
	revision       svn.Revnum
	workingCopy    *wc.Database
	workingInfo    *wc.Info
}

func (target *resolvedTarget) close() {
	if target.session != nil {
		_ = target.session.Close()
	}
	if target.workingCopy != nil {
		_ = target.workingCopy.Close()
	}
}

func splitPegTarget(value string) (string, svn.Revision, error) {
	index := strings.LastIndex(value, "@")
	if index < 0 {
		return value, svn.Revision{}, nil
	}
	if index == len(value)-1 {
		return value[:index], svn.Revision{}, nil
	}
	peg, err := svn.ParseRevision(value[index+1:])
	if err != nil {
		return value, svn.Revision{}, nil
	}
	return value[:index], peg, nil
}

func resolveRARevision(ctx context.Context, session ra.Session, revision svn.Revision) (svn.Revnum, error) {
	switch revision.Kind {
	case svn.RevisionUnspecified, svn.RevisionHead:
		return session.LatestRevision(ctx)
	case svn.RevisionNumber:
		return revision.Number, nil
	case svn.RevisionDate:
		return session.DatedRevision(ctx, revision.Date)
	default:
		return svn.InvalidRevnum, fmt.Errorf("%w: revision %s requires a working copy", svn.ErrClientBadRevision, revision.String())
	}
}

func resolveWCRevision(ctx context.Context, session ra.Session, info *wc.Info, revision svn.Revision) (svn.Revnum, error) {
	switch revision.Kind {
	case svn.RevisionUnspecified, svn.RevisionWorking, svn.RevisionBase:
		return info.Revision, nil
	case svn.RevisionCommitted:
		return info.ChangedRevision, nil
	case svn.RevisionPrevious:
		if info.ChangedRevision <= 0 {
			return svn.InvalidRevnum, fmt.Errorf("%w: no previous revision for %s", svn.ErrClientBadRevision, info.Path)
		}
		return info.ChangedRevision - 1, nil
	default:
		return resolveRARevision(ctx, session, revision)
	}
}

func (client *Client) resolveTarget(ctx context.Context, value string, options InfoOptions) (*resolvedTarget, error) {
	targetValue, parsedPeg, err := splitPegTarget(value)
	if err != nil {
		return nil, err
	}
	peg := options.PegRevision
	if peg.Kind == svn.RevisionUnspecified {
		peg = parsedPeg
	}
	if targetIsURL(targetValue, runtime.GOOS) {
		session, corrected, err := ra.Open(ctx, targetValue, client.Callbacks)
		if err != nil {
			return nil, err
		}
		result := &resolvedTarget{session: session, url: targetValue}
		if corrected != "" {
			result.url = strings.TrimSuffix(corrected, "/")
		}
		result.repositoryRoot, err = session.RepositoryRoot(ctx)
		if err != nil {
			result.close()
			return nil, err
		}
		result.revision, err = resolveRARevision(ctx, session, options.Revision)
		if err != nil {
			result.close()
			return nil, err
		}
		pegRevision := result.revision
		if peg.Kind == svn.RevisionUnspecified {
			pegRevision, err = session.LatestRevision(ctx)
		} else {
			pegRevision, err = resolveRARevision(ctx, session, peg)
		}
		if err != nil {
			result.close()
			return nil, err
		}
		if pegRevision != result.revision {
			locations, err := session.GetLocations(ctx, "", pegRevision, []svn.Revnum{result.revision})
			if err != nil {
				result.close()
				return nil, err
			}
			location, ok := locations[result.revision]
			if !ok {
				result.close()
				return nil, fmt.Errorf("%w: %s has no location in revision %d", svn.ErrFSNotFound, targetValue, result.revision)
			}
			result.url = joinRepositoryURL(result.repositoryRoot, location)
			if err := session.Reparent(ctx, result.url); err != nil {
				result.close()
				return nil, err
			}
		}
		return result, nil
	}

	database, err := wc.Open(ctx, targetValue, wc.Options{})
	if err != nil {
		return nil, err
	}
	info, err := database.Info(ctx, targetValue)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	infoURL := info.URL
	if infoURL == "" {
		infoURL = joinRepositoryURL(info.RepositoryRoot, info.RepositoryPath)
	}
	session, _, err := ra.Open(ctx, infoURL, client.Callbacks)
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	result := &resolvedTarget{session: session, url: infoURL, repositoryRoot: info.RepositoryRoot, workingCopy: database, workingInfo: info}
	result.revision, err = resolveWCRevision(ctx, session, info, options.Revision)
	if err != nil {
		result.close()
		return nil, err
	}
	pegRevision := info.Revision
	if peg.Kind != svn.RevisionUnspecified {
		pegRevision, err = resolveWCRevision(ctx, session, info, peg)
		if err != nil {
			result.close()
			return nil, err
		}
	}
	if pegRevision != result.revision {
		locations, err := session.GetLocations(ctx, "", pegRevision, []svn.Revnum{result.revision})
		if err != nil {
			result.close()
			return nil, err
		}
		if location, ok := locations[result.revision]; ok {
			result.url = joinRepositoryURL(info.RepositoryRoot, location)
			if err := session.Reparent(ctx, result.url); err != nil {
				result.close()
				return nil, err
			}
		}
	}
	return result, nil
}

func (client *Client) Info(ctx context.Context, targetValue string, options InfoOptions) (*Info, error) {
	target, err := client.resolveTarget(ctx, targetValue, options)
	if err != nil {
		return nil, err
	}
	defer target.close()
	entry, err := target.session.Stat(ctx, target.path, target.revision)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotFound, targetValue)
	}
	uuid, err := target.session.UUID(ctx)
	if err != nil {
		return nil, err
	}
	lock, err := target.session.GetLock(ctx, target.path)
	if err != nil {
		return nil, err
	}
	result := &Info{
		Path: targetValue, URL: target.url, RepositoryRoot: target.repositoryRoot, RepositoryUUID: uuid,
		Kind: entry.Kind, Revision: target.revision, Size: entry.Size, HasProperties: entry.HasProps,
		ChangedRevision: entry.CreatedRev, ChangedDate: entry.Time, ChangedAuthor: entry.LastAuthor,
		Lock: lock, WorkingCopy: target.workingInfo,
	}
	if target.workingInfo != nil {
		result.Path = target.workingInfo.Path
		result.RepositoryUUID = target.workingInfo.RepositoryUUID
	}
	return result, nil
}

func targetIsURL(value, goos string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return false
	}
	if goos == "windows" && len(value) >= 2 && value[1] == ':' && (value[0] >= 'A' && value[0] <= 'Z' || value[0] >= 'a' && value[0] <= 'z') {
		return false
	}
	return true
}

func joinRepositoryURL(root, repositoryPath string) string {
	name := strings.TrimPrefix(path.Clean("/"+repositoryPath), "/")
	if name == "" || name == "." {
		return strings.TrimSuffix(root, "/")
	}
	return strings.TrimSuffix(root, "/") + "/" + name
}

func numericRevision(revision svn.Revnum) svn.Revision {
	return svn.Revision{Kind: svn.RevisionNumber, Number: revision}
}

func revisionString(revision svn.Revnum) string {
	return strconv.FormatInt(int64(revision), 10)
}
