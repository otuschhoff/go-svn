package radav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oliver-tuschhoff/go-svn/mergeinfo"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

func (session *Session) report(ctx context.Context, rawURL string, body []byte, decode func(*xml.Decoder) error) error {
	response, err := session.transport.request(ctx, "REPORT", rawURL, body, http.Header{"Content-Type": []string{contentXML}})
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	return decode(xml.NewDecoder(response.Body))
}

func xmlText(builder *strings.Builder, name, value string) {
	builder.WriteByte('<')
	builder.WriteString(name)
	builder.WriteByte('>')
	_ = xml.EscapeText(builder, []byte(value))
	builder.WriteString("</")
	builder.WriteString(name)
	builder.WriteByte('>')
}

func (session *Session) reportURL(ctx context.Context, revision svn.Revnum) (string, error) {
	rawURL, _, err := session.revisionURL(ctx, revision, "")
	return rawURL, err
}

func (session *Session) DatedRevision(ctx context.Context, date time.Time) (svn.Revnum, error) {
	rawURL, err := session.reportResource(ctx)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	body := `<S:dated-rev-report xmlns:S="svn:" xmlns:D="DAV:"><D:creationdate>` + date.UTC().Format(time.RFC3339Nano) + `</D:creationdate></S:dated-rev-report>`
	var response struct {
		Revision svn.Revnum `xml:"version-name"`
	}
	response.Revision = svn.InvalidRevnum
	err = session.report(ctx, rawURL, []byte(body), func(decoder *xml.Decoder) error { return decoder.Decode(&response) })
	if err == nil && !response.Revision.IsValid() {
		err = malformed("dated revision response omitted version-name")
	}
	return response.Revision, err
}

func (session *Session) GetLocations(ctx context.Context, name string, peg svn.Revnum, revisions []svn.Revnum) (map[svn.Revnum]string, error) {
	if err := validateRelpath(name); err != nil {
		return nil, err
	}
	rawURL, err := session.reportURL(ctx, peg)
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(`<S:get-locations xmlns:S="svn:">`)
	xmlText(&body, "S:path", name)
	xmlText(&body, "S:peg-revision", strconv.FormatInt(int64(peg), 10))
	for _, revision := range revisions {
		xmlText(&body, "S:location-revision", strconv.FormatInt(int64(revision), 10))
	}
	body.WriteString(`</S:get-locations>`)
	result := make(map[svn.Revnum]string)
	err = session.report(ctx, rawURL, []byte(body.String()), func(decoder *xml.Decoder) error {
		for {
			token, err := decoder.Token()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Space != nsSVN || start.Name.Local != "location" {
				continue
			}
			var revision svn.Revnum = svn.InvalidRevnum
			var location string
			for _, attribute := range start.Attr {
				switch attribute.Name.Local {
				case "rev":
					value, parseErr := strconv.ParseInt(attribute.Value, 10, 64)
					if parseErr != nil {
						return parseErr
					}
					revision = svn.Revnum(value)
				case "path":
					location = attribute.Value
				}
			}
			if !revision.IsValid() {
				return malformed("location has invalid revision")
			}
			result[revision] = location
		}
	})
	return result, err
}

func (session *Session) GetLocationSegments(ctx context.Context, name string, peg, start, end svn.Revnum, handler func(ra.LocationSegment) error) error {
	if err := validateRelpath(name); err != nil {
		return err
	}
	rawURL, err := session.reportURL(ctx, peg)
	if err != nil {
		return err
	}
	var body strings.Builder
	body.WriteString(`<S:get-location-segments xmlns:S="svn:">`)
	xmlText(&body, "S:path", name)
	xmlText(&body, "S:peg-revision", strconv.FormatInt(int64(peg), 10))
	xmlText(&body, "S:start-revision", strconv.FormatInt(int64(start), 10))
	xmlText(&body, "S:end-revision", strconv.FormatInt(int64(end), 10))
	body.WriteString(`</S:get-location-segments>`)
	return session.report(ctx, rawURL, []byte(body.String()), func(decoder *xml.Decoder) error {
		for {
			token, err := decoder.Token()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			startElement, ok := token.(xml.StartElement)
			if !ok || startElement.Name.Space != nsSVN || startElement.Name.Local != "location-segment" {
				continue
			}
			segment := ra.LocationSegment{RangeStart: svn.InvalidRevnum, RangeEnd: svn.InvalidRevnum}
			for _, attribute := range startElement.Attr {
				switch attribute.Name.Local {
				case "path":
					segment.Path = attribute.Value
				case "range-start":
					value, parseErr := strconv.ParseInt(attribute.Value, 10, 64)
					if parseErr != nil {
						return parseErr
					}
					segment.RangeStart = svn.Revnum(value)
				case "range-end":
					value, parseErr := strconv.ParseInt(attribute.Value, 10, 64)
					if parseErr != nil {
						return parseErr
					}
					segment.RangeEnd = svn.Revnum(value)
				}
			}
			if !segment.RangeStart.IsValid() || !segment.RangeEnd.IsValid() {
				return malformed("location segment has invalid range")
			}
			if handler != nil {
				if err := handler(segment); err != nil {
					return err
				}
			}
		}
	})
}

func (session *Session) GetDeletedRev(ctx context.Context, name string, peg, end svn.Revnum) (svn.Revnum, error) {
	if err := validateRelpath(name); err != nil {
		return svn.InvalidRevnum, err
	}
	rawURL, err := session.reportURL(ctx, peg)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	var body strings.Builder
	body.WriteString(`<S:get-deleted-rev-report xmlns:S="svn:">`)
	xmlText(&body, "S:path", name)
	xmlText(&body, "S:peg-revision", strconv.FormatInt(int64(peg), 10))
	xmlText(&body, "S:end-revision", strconv.FormatInt(int64(end), 10))
	body.WriteString(`</S:get-deleted-rev-report>`)
	var response struct {
		Revision svn.Revnum `xml:"version-name"`
	}
	response.Revision = svn.InvalidRevnum
	err = session.report(ctx, rawURL, []byte(body.String()), func(decoder *xml.Decoder) error { return decoder.Decode(&response) })
	if err == nil && !response.Revision.IsValid() {
		err = malformed("deleted revision response omitted version-name")
	}
	return response.Revision, err
}

func (session *Session) GetMergeinfo(ctx context.Context, paths []string, revision svn.Revnum, inheritance mergeinfo.Inheritance, descendants bool) (map[string]mergeinfo.Mergeinfo, error) {
	for _, name := range paths {
		if err := validateRelpath(name); err != nil {
			return nil, err
		}
	}
	rawURL, err := session.reportURL(ctx, revision)
	if err != nil {
		return nil, err
	}
	inheritanceName := []string{"explicit", "inherited", "nearest-ancestor"}
	if int(inheritance) >= len(inheritanceName) {
		return nil, fmt.Errorf("%w: invalid mergeinfo inheritance", svn.ErrIncorrectParams)
	}
	var body strings.Builder
	body.WriteString(`<S:mergeinfo-report xmlns:S="svn:">`)
	if revision.IsValid() {
		xmlText(&body, "S:revision", strconv.FormatInt(int64(revision), 10))
	}
	xmlText(&body, "S:inherit", inheritanceName[inheritance])
	if descendants {
		xmlText(&body, "S:include-descendants", "yes")
	}
	for _, name := range paths {
		xmlText(&body, "S:path", name)
	}
	body.WriteString(`</S:mergeinfo-report>`)
	var response struct {
		Items []struct {
			Path string `xml:"mergeinfo-path"`
			Info string `xml:"mergeinfo-info"`
		} `xml:"mergeinfo-item"`
	}
	err = session.report(ctx, rawURL, []byte(body.String()), func(decoder *xml.Decoder) error { return decoder.Decode(&response) })
	if err != nil {
		return nil, err
	}
	result := make(map[string]mergeinfo.Mergeinfo, len(response.Items))
	for _, item := range response.Items {
		parsed, err := mergeinfo.Parse(item.Info)
		if err != nil {
			return nil, err
		}
		result[strings.TrimPrefix(item.Path, "/")] = parsed
	}
	return result, nil
}

func (session *Session) GetInheritedProps(ctx context.Context, name string, revision svn.Revnum) ([]ra.InheritedProps, error) {
	if err := validateRelpath(name); err != nil {
		return nil, err
	}
	rawURL, err := session.reportURL(ctx, revision)
	if err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(`<S:inherited-props-report xmlns:S="svn:">`)
	xmlText(&body, "S:path", name)
	if revision.IsValid() {
		xmlText(&body, "S:revision", strconv.FormatInt(int64(revision), 10))
	}
	body.WriteString(`</S:inherited-props-report>`)
	var response struct {
		Items []struct {
			Path  string `xml:"iprop-path"`
			Name  string `xml:"iprop-propname"`
			Value struct {
				Encoding string `xml:"encoding,attr"`
				Text     string `xml:",chardata"`
			} `xml:"iprop-propval"`
		} `xml:"iprop-item"`
	}
	err = session.report(ctx, rawURL, []byte(body.String()), func(decoder *xml.Decoder) error { return decoder.Decode(&response) })
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]svn.Props)
	var order []string
	for _, item := range response.Items {
		if byPath[item.Path] == nil {
			byPath[item.Path] = make(svn.Props)
			order = append(order, item.Path)
		}
		if item.Value.Encoding != "" && item.Value.Encoding != "base64" {
			return nil, malformed("unsupported inherited property encoding %q", item.Value.Encoding)
		}
		value := []byte(item.Value.Text)
		if item.Value.Encoding == "base64" {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(item.Value.Text))
			if err != nil {
				return nil, malformed("invalid inherited property encoding")
			}
			value = decoded
		}
		byPath[item.Path][item.Name] = value
	}
	result := make([]ra.InheritedProps, 0, len(order))
	for _, itemPath := range order {
		result = append(result, ra.InheritedProps{Path: itemPath, Props: byPath[itemPath]})
	}
	return result, nil
}

func (session *Session) GetLocks(ctx context.Context, name string, depth svn.Depth) (map[string]*svn.Lock, error) {
	if err := validateRelpath(name); err != nil {
		return nil, err
	}
	var body strings.Builder
	body.WriteString(`<S:get-locks-report xmlns:S="svn:" depth="`)
	body.WriteString(depth.String())
	body.WriteString(`"></S:get-locks-report>`)
	rawURL := appendURLPath(session.url, name)
	var response struct {
		Locks []struct {
			Path    string `xml:"path"`
			Token   string `xml:"token"`
			Owner   string `xml:"owner"`
			Comment string `xml:"comment"`
			IsDAV   bool   `xml:"is-dav-comment"`
			Created string `xml:"creationdate"`
			Expires string `xml:"expirationdate"`
		} `xml:"lock"`
	}
	err := session.report(ctx, rawURL, []byte(body.String()), func(decoder *xml.Decoder) error { return decoder.Decode(&response) })
	if err != nil {
		return nil, err
	}
	result := make(map[string]*svn.Lock, len(response.Locks))
	requestedPath := "/" + strings.Trim(name, "/")
	if requestedPath == "/" && name != "" {
		requestedPath = "/" + name
	}
	for _, value := range response.Locks {
		lock := &svn.Lock{Path: value.Path, Token: value.Token, Owner: value.Owner, Comment: value.Comment, IsDAVComment: value.IsDAV}
		if value.Created != "" {
			lock.CreationDate, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(value.Created))
			if err != nil {
				return nil, malformed("invalid lock creation date")
			}
		}
		if value.Expires != "" {
			lock.ExpirationDate, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(value.Expires))
			if err != nil {
				return nil, malformed("invalid lock expiration date")
			}
		}
		if lockMatchesDepth(requestedPath, lock.Path, depth) {
			result[lock.Path] = lock
		}
	}
	return result, nil
}

func lockMatchesDepth(requested, locked string, depth svn.Depth) bool {
	requested = strings.TrimSuffix(requested, "/")
	locked = strings.TrimSuffix(locked, "/")
	if requested == locked {
		return true
	}
	if !strings.HasPrefix(locked, requested+"/") {
		return false
	}
	if depth == svn.DepthInfinity {
		return true
	}
	if depth == svn.DepthFiles || depth == svn.DepthImmediates {
		return !strings.Contains(strings.TrimPrefix(locked, requested+"/"), "/")
	}
	return false
}

func (session *Session) GetLock(ctx context.Context, name string) (*svn.Lock, error) {
	locks, err := session.GetLocks(ctx, name, svn.DepthEmpty)
	if err != nil {
		return nil, err
	}
	for _, lock := range locks {
		return lock, nil
	}
	return nil, nil
}
