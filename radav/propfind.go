package radav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/svn"
	svnpath "github.com/otuschhoff/go-svn/svn/path"
)

type property struct {
	Name  xml.Name
	Attr  []xml.Attr
	Text  string `xml:",chardata"`
	Inner string `xml:",innerxml"`
	Href  string `xml:"href"`
}

type propertySet struct{ Values []property }

func (set *propertySet) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.StartElement:
			item := property{Name: value.Name, Attr: append([]xml.Attr(nil), value.Attr...)}
			if err := decoder.DecodeElement(&item, &value); err != nil {
				return err
			}
			set.Values = append(set.Values, item)
		case xml.EndElement:
			if value.Name == start.Name {
				return nil
			}
		}
	}
}

type propstat struct {
	Properties propertySet `xml:"prop"`
	Status     string      `xml:"status"`
}

type propResponse struct {
	Href      string     `xml:"href"`
	Propstats []propstat `xml:"propstat"`
}

type multistatus struct {
	Responses []propResponse `xml:"response"`
}

const propertyColonEscape = "__svn_colon__"

var invalidQualifiedProperty = regexp.MustCompile(`(<\/?[A-Za-z_][A-Za-z0-9_.-]*:[A-Za-z_][A-Za-z0-9_.-]*):([A-Za-z_][A-Za-z0-9_.-]*)`)

func decodeDAVXML(body []byte, target any) error {
	body = invalidQualifiedProperty.ReplaceAll(body, []byte("${1}"+propertyColonEscape+"${2}"))
	return xml.Unmarshal(body, target)
}

func (session *Session) propfind(ctx context.Context, rawURL, depth string) ([]propResponse, error) {
	return session.propfindHeaders(ctx, rawURL, depth, nil)
}

func (session *Session) propfindHeaders(ctx context.Context, rawURL, depth string, extra http.Header) ([]propResponse, error) {
	body := []byte(`<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`)
	headers := http.Header{"Content-Type": []string{contentXML}, "Depth": []string{depth}}
	for name, values := range extra {
		headers[name] = append([]string(nil), values...)
	}
	response, err := session.transport.request(ctx, "PROPFIND", rawURL, body, headers)
	if err != nil {
		return nil, err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode != http.StatusMultiStatus {
		return nil, responseError(response)
	}
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	var result multistatus
	if err := decodeDAVXML(responseBody, &result); err != nil {
		return nil, malformed("invalid multistatus: %v", err)
	}
	return result.Responses, nil
}

func (session *Session) revisionURL(ctx context.Context, revision svn.Revnum, name string) (string, svn.Revnum, error) {
	if err := validateRelpath(name); err != nil {
		return "", svn.InvalidRevnum, err
	}
	return session.repositoryRevisionURL(ctx, revision, path.Join(session.sessionAnchor(), name))
}

func (session *Session) repositoryRevisionURL(ctx context.Context, revision svn.Revnum, name string) (string, svn.Revnum, error) {
	if !revision.IsValid() {
		var err error
		revision, err = session.LatestRevision(ctx)
		if err != nil {
			return "", svn.InvalidRevnum, err
		}
	}
	if !revision.IsValid() {
		return "", svn.InvalidRevnum, malformed("repository did not advertise a youngest revision")
	}
	if session.info.RevRootStub == "" {
		collection, _, discovered, err := session.legacyRevisionResources(ctx, revision)
		if err != nil {
			return "", svn.InvalidRevnum, err
		}
		return appendURLPath(collection, name), discovered, nil
	}
	stub, err := resolveURL(session.url, session.info.RevRootStub)
	if err != nil {
		return "", svn.InvalidRevnum, err
	}
	return appendURLPath(stub, strconv.FormatInt(int64(revision), 10), name), revision, nil
}

func validateRelpath(name string) error {
	if !svnpath.RelpathIsCanonical(name) {
		return fmt.Errorf("%w: repository-relative path %q", svn.ErrBadFilename, name)
	}
	return nil
}

func (session *Session) revisionPropURL(ctx context.Context, revision svn.Revnum) (string, error) {
	if !revision.IsValid() {
		var err error
		revision, err = session.LatestRevision(ctx)
		if err != nil {
			return "", err
		}
	}
	if !revision.IsValid() {
		return "", malformed("repository did not advertise a youngest revision")
	}
	if session.info.RevStub == "" {
		_, baseline, _, err := session.legacyRevisionResources(ctx, revision)
		return baseline, err
	}
	stub, err := resolveURL(session.url, session.info.RevStub)
	if err != nil {
		return "", err
	}
	return appendURLPath(stub, strconv.FormatInt(int64(revision), 10)), nil
}

func (session *Session) legacyLatestRevision(ctx context.Context) (svn.Revnum, error) {
	vcc, err := session.legacyVCC(ctx)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	responses, err := session.propfind(ctx, vcc, "0")
	if err != nil {
		return svn.InvalidRevnum, err
	}
	baseline, ok := responsePropertyHref(responses, nsDAV, "checked-in")
	if !ok {
		return svn.InvalidRevnum, malformed("checked-in baseline is missing")
	}
	baseline, err = resolveURL(vcc, baseline)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	responses, err = session.propfind(ctx, baseline, "0")
	if err != nil {
		return svn.InvalidRevnum, err
	}
	if len(responses) == 0 {
		return svn.InvalidRevnum, malformed("baseline PROPFIND returned no responses")
	}
	property, ok := findProperty(successfulProperties(responses[0]), nsDAV, "version-name")
	if !ok {
		return svn.InvalidRevnum, malformed("baseline version-name is missing")
	}
	value, err := strconv.ParseInt(strings.TrimSpace(property.Text), 10, 64)
	if err != nil || value < 0 {
		return svn.InvalidRevnum, malformed("invalid baseline version-name %q", property.Text)
	}
	return svn.Revnum(value), nil
}

func (session *Session) reportResource(ctx context.Context) (string, error) {
	if session.info.MeResource != "" {
		return resolveURL(session.url, session.info.MeResource)
	}
	return session.legacyVCC(ctx)
}

func (session *Session) legacyVCC(ctx context.Context) (string, error) {
	responses, err := session.propfind(ctx, session.url, "0")
	if err != nil {
		return "", err
	}
	vcc, ok := responsePropertyHref(responses, nsDAV, "version-controlled-configuration")
	if !ok {
		return "", malformed("version-controlled configuration is missing")
	}
	return resolveURL(session.url, vcc)
}

func (session *Session) legacyRevisionResources(ctx context.Context, revision svn.Revnum) (string, string, svn.Revnum, error) {
	vcc, err := session.legacyVCC(ctx)
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	responses, err := session.propfindHeaders(ctx, vcc, "0", http.Header{"Label": []string{strconv.FormatInt(int64(revision), 10)}})
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	baseline, ok := responsePropertyHref(responses, nsDAV, "checked-in")
	if !ok {
		return "", "", svn.InvalidRevnum, malformed("checked-in baseline is missing")
	}
	baseline, err = resolveURL(vcc, baseline)
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	responses, err = session.propfind(ctx, baseline, "0")
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	collection, ok := responsePropertyHref(responses, nsDAV, "baseline-collection")
	if !ok {
		return "", "", svn.InvalidRevnum, malformed("baseline collection is missing")
	}
	collection, err = resolveURL(baseline, collection)
	if err != nil {
		return "", "", svn.InvalidRevnum, err
	}
	return collection, baseline, revision, nil
}

func responsePropertyHref(responses []propResponse, space, local string) (string, bool) {
	if len(responses) == 0 {
		return "", false
	}
	property, ok := findProperty(successfulProperties(responses[0]), space, local)
	return strings.TrimSpace(property.Href), ok && strings.TrimSpace(property.Href) != ""
}

func appendURLPath(rawURL string, elements ...string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parts := []string{parsed.Path}
	parts = append(parts, elements...)
	parsed.Path = path.Join(parts...)
	return parsed.String()
}

func successfulProperties(response propResponse) []property {
	var result []property
	for _, stat := range response.Propstats {
		if strings.Contains(stat.Status, " 200 ") {
			result = append(result, stat.Properties.Values...)
		}
	}
	return result
}

func findProperty(properties []property, space, local string) (property, bool) {
	for _, value := range properties {
		if value.Name.Space == space && value.Name.Local == local {
			return value, true
		}
	}
	return property{}, false
}

func versionedProps(properties []property) (svn.Props, error) {
	result := make(svn.Props)
	for _, value := range properties {
		name := ""
		switch value.Name.Space {
		case nsSVNProp:
			name = "svn:" + value.Name.Local
		case nsCustom:
			name = strings.ReplaceAll(value.Name.Local, propertyColonEscape, ":")
		}
		if name == "" {
			continue
		}
		data := []byte(value.Text)
		for _, attribute := range value.Attr {
			if attribute.Name.Space == nsDAVSVN && attribute.Name.Local == "encoding" && attribute.Value == "base64" {
				decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value.Text))
				if err != nil {
					return nil, malformed("invalid base64 property %s", name)
				}
				data = decoded
			}
		}
		result[name] = data
	}
	return result, nil
}

func direntFromProperties(name string, properties []property) (*svn.Dirent, error) {
	entry := &svn.Dirent{Path: name, CreatedRev: svn.InvalidRevnum}
	resource, ok := findProperty(properties, nsDAV, "resourcetype")
	if !ok {
		return nil, malformed("resource %s has no type", name)
	}
	if strings.Contains(resource.Inner, "collection") {
		entry.Kind = svn.NodeDir
	} else {
		entry.Kind = svn.NodeFile
	}
	if value, ok := findProperty(properties, nsDAV, "getcontentlength"); ok && strings.TrimSpace(value.Text) != "" {
		size, err := strconv.ParseInt(strings.TrimSpace(value.Text), 10, 64)
		if err != nil {
			return nil, malformed("invalid content length for %s", name)
		}
		entry.Size = size
	}
	if value, ok := findProperty(properties, nsDAV, "version-name"); ok {
		revision, err := strconv.ParseInt(strings.TrimSpace(value.Text), 10, 64)
		if err != nil {
			return nil, malformed("invalid version for %s", name)
		}
		entry.CreatedRev = svn.Revnum(revision)
	}
	if value, ok := findProperty(properties, nsDAV, "creationdate"); ok && strings.TrimSpace(value.Text) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value.Text))
		if err != nil {
			return nil, malformed("invalid creation date for %s", name)
		}
		entry.Time = parsed
	}
	if value, ok := findProperty(properties, nsDAV, "creator-displayname"); ok {
		entry.LastAuthor = value.Text
	}
	if value, ok := findProperty(properties, nsDAVSVN, "deadprop-count"); ok {
		entry.HasProps = strings.TrimSpace(value.Text) != "0"
	} else {
		props, err := versionedProps(properties)
		if err != nil {
			return nil, err
		}
		entry.HasProps = len(props) > 0
	}
	return entry, nil
}

func maskDirent(entry svn.Dirent, fields svn.DirentFields) svn.Dirent {
	if fields&svn.DirentKind == 0 {
		entry.Kind = svn.NodeUnknown
	}
	if fields&svn.DirentSize == 0 {
		entry.Size = 0
	}
	if fields&svn.DirentHasProps == 0 {
		entry.HasProps = false
	}
	if fields&svn.DirentCreatedRev == 0 {
		entry.CreatedRev = svn.InvalidRevnum
	}
	if fields&svn.DirentTime == 0 {
		entry.Time = time.Time{}
	}
	if fields&svn.DirentLastAuthor == 0 {
		entry.LastAuthor = ""
	}
	return entry
}

func sortDirents(entries []svn.Dirent) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
}

func responsePath(href string) string {
	parsed, err := url.Parse(href)
	if err != nil {
		return href
	}
	return strings.TrimSuffix(parsed.Path, "/")
}

func matches(patterns []string, name string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if matched, _ := path.Match(pattern, name); matched {
			return true
		}
		if matched, _ := path.Match(pattern, path.Base(name)); matched {
			return true
		}
	}
	return false
}
