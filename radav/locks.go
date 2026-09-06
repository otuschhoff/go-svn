package radav

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

type lockResponse struct {
	Discovery struct {
		Locks []struct {
			Timeout string `xml:"timeout"`
			Token   string `xml:"locktoken>href"`
			Owner   string `xml:"owner"`
		} `xml:"activelock"`
	} `xml:"lockdiscovery"`
}

func (session *Session) lock(ctx context.Context, pathRevisions map[string]svn.Revnum, comment string, steal bool, callback ra.LockCallback) error {
	paths := sortedDAVKeys(pathRevisions)
	var result error
	for _, name := range paths {
		if err := validateRelpath(name); err != nil {
			return err
		}
		lock, err := session.lockPath(ctx, name, pathRevisions[name], comment, steal)
		if callback != nil {
			if callbackErr := callback(name, lock, err); callbackErr != nil {
				return callbackErr
			}
		} else if err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (session *Session) lockPath(ctx context.Context, name string, revision svn.Revnum, comment string, steal bool) (*svn.Lock, error) {
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="utf-8"?><lockinfo xmlns="DAV:"><lockscope><exclusive/></lockscope><locktype><write/></locktype>`)
	if comment != "" {
		body.WriteString(`<owner>`)
		if err := xml.EscapeText(&body, []byte(comment)); err != nil {
			return nil, err
		}
		body.WriteString(`</owner>`)
	}
	body.WriteString(`</lockinfo>`)
	headers := http.Header{"Content-Type": []string{contentXML}, "Timeout": []string{"Infinite"}}
	if revision.IsValid() {
		headers.Set("X-SVN-Version-Name", strconv.FormatInt(int64(revision), 10))
	}
	if steal {
		headers.Set("X-SVN-Options", "lock-steal")
	}
	response, err := session.transport.request(ctx, "LOCK", appendURLPath(session.url, name), []byte(body.String()), headers)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return nil, lockStatusError(response, name)
	}
	var parsed lockResponse
	if err := xml.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return nil, malformed("invalid LOCK response: %v", err)
	}
	if len(parsed.Discovery.Locks) != 1 || strings.TrimSpace(parsed.Discovery.Locks[0].Token) == "" {
		return nil, malformed("LOCK response contains no lock token")
	}
	value := parsed.Discovery.Locks[0]
	lock := &svn.Lock{Path: name, Token: strings.TrimSpace(value.Token), Owner: response.Header.Get("X-SVN-Lock-Owner"), Comment: value.Owner}
	if created := response.Header.Get("X-SVN-Creation-Date"); created != "" {
		lock.CreationDate, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, malformed("invalid lock creation date %q", created)
		}
	}
	if timeout := strings.TrimSpace(value.Timeout); timeout != "" && !strings.EqualFold(timeout, "Infinite") {
		if !strings.HasPrefix(strings.ToLower(timeout), "second-") {
			return nil, malformed("invalid lock timeout %q", timeout)
		}
		seconds, parseErr := time.ParseDuration(strings.TrimPrefix(strings.ToLower(timeout), "second-") + "s")
		if parseErr != nil {
			return nil, malformed("invalid lock timeout %q", timeout)
		}
		lock.ExpirationDate = time.Now().Add(seconds)
	}
	return lock, nil
}

func (session *Session) unlock(ctx context.Context, pathTokens map[string]string, breakLock bool, callback ra.LockCallback) error {
	paths := sortedDAVKeys(pathTokens)
	var result error
	for _, name := range paths {
		if err := validateRelpath(name); err != nil {
			return err
		}
		token := pathTokens[name]
		var err error
		if breakLock && token == "" {
			var lock *svn.Lock
			lock, err = session.GetLock(ctx, name)
			if err == nil && lock == nil {
				err = fmt.Errorf("%w: %s", svn.ErrRANotLocked, name)
			} else if err == nil {
				token = lock.Token
			}
		}
		if err == nil {
			err = session.unlockPath(ctx, name, token, breakLock)
		}
		if callback != nil {
			if callbackErr := callback(name, nil, err); callbackErr != nil {
				return callbackErr
			}
		} else if err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (session *Session) unlockPath(ctx context.Context, name, token string, breakLock bool) error {
	if token == "" {
		return fmt.Errorf("%w: empty lock token for %s", svn.ErrRANotLocked, name)
	}
	headers := http.Header{"Lock-Token": []string{"<" + strings.Trim(token, "<>") + ">"}}
	if breakLock {
		headers.Set("X-SVN-Options", "lock-break")
	}
	response, err := session.transport.request(ctx, "UNLOCK", appendURLPath(session.url, name), nil, headers)
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return lockStatusError(response, name)
	}
	return nil
}

func lockStatusError(response *http.Response, name string) error {
	err := responseError(response)
	if !errors.Is(err, svn.ErrRADAVRequestFailed) && !errors.Is(err, svn.ErrRANotAuthorized) && !errors.Is(err, svn.ErrRADAVMethodNotAllowed) {
		return err
	}
	var code svn.Code
	switch response.StatusCode {
	case http.StatusBadRequest:
		code = svn.ErrFSNoSuchLock
	case http.StatusForbidden:
		code = svn.ErrFSLockOwnerMismatch
	case http.StatusMethodNotAllowed:
		code = svn.ErrFSOutOfDate
	case http.StatusLocked:
		code = svn.ErrFSPathAlreadyLocked
	default:
		return err
	}
	return &svn.Error{Code: code, Message: fmt.Sprintf("lock operation failed for %s: %s", name, response.Status)}
}

func (session *Session) changeRevProp(ctx context.Context, revision svn.Revnum, name string, value, oldValue []byte, dontCare bool) error {
	if !revision.IsValid() || name == "" {
		return fmt.Errorf("%w: invalid revision property change", svn.ErrIncorrectParams)
	}
	atomic := session.info.Capabilities[ra.CapabilityAtomicRevprops]
	if !dontCare && !atomic {
		return fmt.Errorf("%w: server does not support atomic revision property changes", svn.ErrRANotImplemented)
	}
	if value == nil && dontCare && atomic {
		current, found, err := session.RevProp(ctx, revision, name)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		oldValue, dontCare = current, false
	}
	properties := svn.Props{name: append([]byte(nil), value...)}
	if value == nil {
		properties[name] = nil
	}
	var oldValues map[string]*[]byte
	if !dontCare {
		oldValues = make(map[string]*[]byte)
		if oldValue == nil {
			oldValues[name] = nil
		} else {
			copy := append([]byte(nil), oldValue...)
			oldValues[name] = &copy
		}
	}
	body, err := propertyUpdateXML(properties, oldValues)
	if err != nil {
		return err
	}
	resource, err := session.revisionPropURL(ctx, revision)
	if err != nil {
		return err
	}
	response, err := session.transport.request(ctx, "PROPPATCH", resource, body, http.Header{"Content-Type": []string{contentXML}})
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusPreconditionFailed {
		defer drainAndClose(response.Body)
		_ = responseError(response)
		return fmt.Errorf("%w: revision property %s", svn.ErrFSPropBasevalueMismatch, name)
	}
	return checkProppatchResponse(response)
}
