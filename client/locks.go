package client

import (
	"context"
	"fmt"
	"os"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
	"github.com/otuschhoff/go-svn/wc"
)

type LockOptions struct {
	Comment string
	Force   bool
}

func (client *Client) Lock(ctx context.Context, targetValue string, options LockOptions) (*svn.Lock, error) {
	target, err := client.resolveTarget(ctx, targetValue, InfoOptions{})
	if err != nil {
		return nil, err
	}
	defer target.close()
	if target.workingCopy != nil {
		_ = target.workingCopy.Close()
		target.workingCopy, err = wc.Open(ctx, target.workingInfo.Path, wc.Options{Writable: true})
		if err != nil {
			return nil, err
		}
	}
	var result *svn.Lock
	err = target.session.Lock(ctx, map[string]svn.Revnum{target.path: target.revision}, options.Comment, options.Force, func(_ string, lock *svn.Lock, lockErr error) error {
		result = lock
		return lockErr
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("lock operation returned no lock")
	}
	if target.workingCopy != nil {
		if err := target.workingCopy.RecordLock(ctx, target.workingInfo.RepositoryPath, result); err != nil {
			return nil, err
		}
		_ = setNeedsLockWritable(target.workingInfo, true)
	}
	client.notify(notify.Notify{Action: notify.ActionLocked, Path: targetValue, Lock: result})
	return result, nil
}

func (client *Client) Unlock(ctx context.Context, targetValue string, force bool) error {
	target, err := client.resolveTarget(ctx, targetValue, InfoOptions{})
	if err != nil {
		return err
	}
	defer target.close()
	if target.workingCopy != nil {
		_ = target.workingCopy.Close()
		target.workingCopy, err = wc.Open(ctx, target.workingInfo.Path, wc.Options{Writable: true})
		if err != nil {
			return err
		}
	}
	token := ""
	if target.workingInfo != nil && target.workingInfo.Lock != nil {
		token = target.workingInfo.Lock.Token
	}
	if token == "" && !force {
		lock, err := target.session.GetLock(ctx, target.path)
		if err != nil {
			return err
		}
		if lock == nil {
			return fmt.Errorf("%w: %s", svn.ErrRANotLocked, targetValue)
		}
		token = lock.Token
	}
	err = target.session.Unlock(ctx, map[string]string{target.path: token}, force, func(_ string, _ *svn.Lock, unlockErr error) error { return unlockErr })
	if err != nil {
		return err
	}
	if target.workingCopy != nil {
		if err := target.workingCopy.RemoveLock(ctx, target.workingInfo.RepositoryPath); err != nil {
			return err
		}
		_ = setNeedsLockWritable(target.workingInfo, false)
	}
	client.notify(notify.Notify{Action: notify.ActionUnlocked, Path: targetValue})
	return nil
}

func setNeedsLockWritable(info *wc.Info, writable bool) error {
	if info == nil || info.Kind != svn.NodeFile || len(info.WorkingProperties["svn:needs-lock"]) == 0 {
		return nil
	}
	stat, err := os.Stat(info.Path)
	if err != nil {
		return err
	}
	mode := stat.Mode()
	if writable {
		mode |= 0o200
	} else {
		mode &^= 0o222
	}
	return os.Chmod(info.Path, mode)
}

func (client *Client) Relocate(ctx context.Context, targetPath, repositoryRoot string) error {
	database, err := wc.Open(ctx, targetPath, wc.Options{Writable: true})
	if err != nil {
		return err
	}
	defer database.Close()
	session, _, err := ra.Open(ctx, repositoryRoot, client.Callbacks)
	if err != nil {
		return err
	}
	defer session.Close()
	uuid, err := session.UUID(ctx)
	if err != nil {
		return err
	}
	if uuid != database.RepositoryUUID() {
		return fmt.Errorf("%w: repository UUID %s does not match working copy UUID %s", svn.ErrClientInvalidRelocation, uuid, database.RepositoryUUID())
	}
	return database.Relocate(ctx, repositoryRoot)
}
