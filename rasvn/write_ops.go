package rasvn

import (
	"context"
	"errors"
	"sort"

	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

func (session *Session) lockMany(ctx context.Context, paths []string, comment Item, steal bool, entries Item, callback ra.LockCallback) (resultErr error) {
	conn := session.conn
	conn.mu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			conn.mu.Unlock()
		}
	}()
	defer func() { resultErr = normalizeContextError(ctx, resultErr) }()
	stop, err := conn.interruptOnCancel(ctx)
	if err != nil {
		return err
	}
	defer stop()
	if err := conn.write(List(Word("lock-many"), List(comment, Word(boolWord(steal)), entries))); err != nil {
		return err
	}
	if err := conn.authenticate(ctx); err != nil {
		return err
	}
	type result struct {
		lock *svn.Lock
		err  error
	}
	results := make([]result, 0, len(paths))
	for {
		item, err := conn.reader.Decode()
		if err != nil {
			return err
		}
		if item.Kind == WordKind && item.Word == "done" {
			break
		}
		if len(results) >= len(paths) || item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != WordKind || item.List[1].Kind != ListKind {
			return malformed("invalid lock-many response")
		}
		var lock *svn.Lock
		var itemErr error
		switch item.List[0].Word {
		case "success":
			lock, itemErr = parseLock(item.List[1].List)
		case "failure":
			itemErr = decodeFailure(item.List[1].List)
		default:
			return malformed("invalid lock-many status %q", item.List[0].Word)
		}
		results = append(results, result{lock: lock, err: itemErr})
	}
	if len(results) != len(paths) {
		return malformed("lock-many returned %d statuses for %d paths", len(results), len(paths))
	}
	_, finalErr := conn.readResponse()
	conn.mu.Unlock()
	unlocked = true
	var callbackErr error
	for index, result := range results {
		if callback != nil && callbackErr == nil {
			callbackErr = callback(paths[index], result.lock, result.err)
		} else if callback == nil && result.err != nil && callbackErr == nil {
			callbackErr = result.err
		}
	}
	return errors.Join(callbackErr, finalErr)
}

func (session *Session) unlockMany(ctx context.Context, paths []string, breakLock bool, entries Item, callback ra.LockCallback) (resultErr error) {
	conn := session.conn
	conn.mu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			conn.mu.Unlock()
		}
	}()
	defer func() { resultErr = normalizeContextError(ctx, resultErr) }()
	stop, err := conn.interruptOnCancel(ctx)
	if err != nil {
		return err
	}
	defer stop()
	if err := conn.write(List(Word("unlock-many"), List(Word(boolWord(breakLock)), entries))); err != nil {
		return err
	}
	if err := conn.authenticate(ctx); err != nil {
		return err
	}
	type result struct {
		path string
		err  error
	}
	results := make([]result, 0, len(paths))
	for {
		item, err := conn.reader.Decode()
		if err != nil {
			return err
		}
		if item.Kind == WordKind && item.Word == "done" {
			break
		}
		if len(results) >= len(paths) || item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != WordKind || item.List[1].Kind != ListKind {
			return malformed("invalid unlock-many response")
		}
		path := paths[len(results)]
		var itemErr error
		switch item.List[0].Word {
		case "success":
			if len(item.List[1].List) != 1 || item.List[1].List[0].Kind != StringKind {
				return malformed("invalid unlock-many success")
			}
			path = string(item.List[1].List[0].String)
		case "failure":
			itemErr = decodeFailure(item.List[1].List)
		default:
			return malformed("invalid unlock-many status %q", item.List[0].Word)
		}
		results = append(results, result{path: path, err: itemErr})
	}
	if len(results) != len(paths) {
		return malformed("unlock-many returned %d statuses for %d paths", len(results), len(paths))
	}
	_, finalErr := conn.readResponse()
	conn.mu.Unlock()
	unlocked = true
	var callbackErr error
	for _, result := range results {
		if callback != nil && callbackErr == nil {
			callbackErr = callback(result.path, nil, result.err)
		} else if callback == nil && result.err != nil && callbackErr == nil {
			callbackErr = result.err
		}
	}
	return errors.Join(callbackErr, finalErr)
}

func sortedRevisionPaths(values map[string]svn.Revnum) []string {
	paths := make([]string, 0, len(values))
	for path := range values {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func sortedStringPaths(values map[string]string) []string {
	paths := make([]string, 0, len(values))
	for path := range values {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
