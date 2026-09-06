package radav

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func (session *Session) GetFile(ctx context.Context, name string, revision svn.Revnum, output io.Writer, wantProps bool) (svn.Revnum, svn.Props, error) {
	resource, resolved, err := session.revisionURL(ctx, revision, name)
	if err != nil {
		return svn.InvalidRevnum, nil, err
	}
	var props svn.Props
	if wantProps {
		responses, propErr := session.propfind(ctx, resource, "0")
		if propErr != nil {
			return svn.InvalidRevnum, nil, propErr
		}
		if len(responses) != 1 {
			return svn.InvalidRevnum, nil, malformed("file PROPFIND returned %d responses", len(responses))
		}
		props, err = versionedProps(successfulProperties(responses[0]))
		if err != nil {
			return svn.InvalidRevnum, nil, err
		}
	}
	response, err := session.transport.request(ctx, "GET", resource, nil, nil)
	if err != nil {
		return svn.InvalidRevnum, nil, err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return svn.InvalidRevnum, nil, responseError(response)
	}
	if output == nil {
		output = io.Discard
	}
	digest := md5.New()
	if _, err := io.Copy(io.MultiWriter(output, digest), response.Body); err != nil {
		return svn.InvalidRevnum, nil, err
	}
	if expected := response.Header.Get("Content-MD5"); expected != "" {
		decoded, decodeErr := base64.StdEncoding.DecodeString(expected)
		if decodeErr != nil {
			decoded, decodeErr = hex.DecodeString(expected)
		}
		if decodeErr != nil || !strings.EqualFold(hex.EncodeToString(decoded), hex.EncodeToString(digest.Sum(nil))) {
			return svn.InvalidRevnum, nil, fmt.Errorf("%w: GET %s", svn.ErrChecksumMismatch, name)
		}
	}
	return resolved, props, nil
}
