package radav

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
)

type davError struct {
	Human struct {
		Code string `xml:"errcode,attr"`
		Text string `xml:",chardata"`
	} `xml:"human-readable"`
}

func responseError(response *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	code := statusCode(response.StatusCode)
	message := strings.TrimSpace(string(data))
	var parsed davError
	if xml.Unmarshal(data, &parsed) == nil && strings.TrimSpace(parsed.Human.Text) != "" {
		message = strings.TrimSpace(parsed.Human.Text)
		if number, err := strconv.ParseUint(parsed.Human.Code, 10, 32); err == nil {
			code = svn.Code(number)
		}
	}
	if message == "" {
		message = response.Status
	}
	return &svn.Error{Code: code, Message: message}
}

func statusCode(status int) svn.Code {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return svn.ErrRANotAuthorized
	case http.StatusNotFound:
		return svn.ErrRADAVPathNotFound
	case http.StatusPreconditionFailed:
		return svn.ErrRADAVPreconditionFailed
	case http.StatusMethodNotAllowed:
		return svn.ErrRADAVMethodNotAllowed
	default:
		return svn.ErrRADAVRequestFailed
	}
}

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", svn.ErrRADAVMalformedData, fmt.Sprintf(format, args...))
}
