package radav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type logPathXML struct {
	XMLName      xml.Name
	Path         string `xml:",chardata"`
	NodeKind     string `xml:"node-kind,attr"`
	TextModified string `xml:"text-mods,attr"`
	PropModified string `xml:"prop-mods,attr"`
	CopyfromPath string `xml:"copyfrom-path,attr"`
	CopyfromRev  string `xml:"copyfrom-rev,attr"`
}

type logRevPropXML struct {
	Name     string `xml:"name,attr"`
	Encoding string `xml:"encoding,attr"`
	Value    string `xml:",chardata"`
}

type logItemXML struct {
	Revision         int64           `xml:"version-name"`
	Author           string          `xml:"creator-displayname"`
	Date             string          `xml:"date"`
	CreationDate     string          `xml:"creationdate"`
	Comment          string          `xml:"comment"`
	Paths            []logPathXML    `xml:",any"`
	RevProps         []logRevPropXML `xml:"revprop"`
	HasChildren      *struct{}       `xml:"has-children"`
	SubtractiveMerge *struct{}       `xml:"subtractive-merge"`
}

func (session *Session) Log(ctx context.Context, options ra.LogOptions, handler func(*svn.LogEntry) error) error {
	for _, name := range options.Paths {
		if err := validateRelpath(name); err != nil {
			return err
		}
	}
	peg := options.End
	if options.Start > peg {
		peg = options.Start
	}
	rawURL, err := session.reportURL(ctx, peg)
	if err != nil {
		return err
	}
	var body strings.Builder
	body.WriteString(`<S:log-report xmlns:S="svn:" xmlns:D="DAV:">`)
	if options.Start.IsValid() {
		xmlText(&body, "S:start-revision", strconv.FormatInt(int64(options.Start), 10))
	}
	if options.End.IsValid() {
		xmlText(&body, "S:end-revision", strconv.FormatInt(int64(options.End), 10))
	}
	if options.Limit > 0 {
		xmlText(&body, "S:limit", strconv.Itoa(options.Limit))
	}
	if options.DiscoverChangedPaths {
		body.WriteString(`<S:discover-changed-paths/>`)
	}
	if options.StrictNodeHistory {
		body.WriteString(`<S:strict-node-history/>`)
	}
	if options.IncludeMerged {
		body.WriteString(`<S:include-merged-revisions/>`)
	}
	body.WriteString(`<S:encode-binary-props/>`)
	if options.RevProps == nil {
		body.WriteString(`<S:all-revprops/>`)
	} else {
		if len(options.RevProps) == 0 {
			body.WriteString(`<S:no-revprops/>`)
		}
		for _, name := range options.RevProps {
			xmlText(&body, "S:revprop", name)
		}
	}
	for _, name := range options.Paths {
		xmlText(&body, "S:path", name)
	}
	body.WriteString(`</S:log-report>`)
	return session.report(ctx, rawURL, []byte(body.String()), func(decoder *xml.Decoder) error {
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Space != nsSVN || start.Name.Local != "log-item" {
				continue
			}
			var item logItemXML
			item.Revision = int64(svn.InvalidRevnum)
			if err := decoder.DecodeElement(&item, &start); err != nil {
				return err
			}
			entry, err := decodeLogItem(item)
			if err != nil {
				return err
			}
			if handler != nil {
				if err := handler(entry); err != nil {
					return err
				}
			}
		}
	})
}

func decodeLogItem(item logItemXML) (*svn.LogEntry, error) {
	if !svn.Revnum(item.Revision).IsValid() {
		return nil, malformed("log item omitted a valid revision")
	}
	entry := &svn.LogEntry{Revision: svn.Revnum(item.Revision), Author: item.Author, Message: item.Comment, RevProps: make(svn.Props), HasChildren: item.HasChildren != nil, SubtractiveMerge: item.SubtractiveMerge != nil}
	date := item.Date
	if date == "" {
		date = item.CreationDate
	}
	if strings.TrimSpace(date) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(date))
		if err != nil {
			return nil, malformed("invalid log date")
		}
		entry.Date = parsed
	}
	if item.Author != "" {
		entry.RevProps["svn:author"] = []byte(item.Author)
	}
	if date != "" {
		entry.RevProps["svn:date"] = []byte(date)
	}
	if item.Comment != "" {
		entry.RevProps["svn:log"] = []byte(item.Comment)
	}
	for _, value := range item.RevProps {
		data := []byte(value.Value)
		if value.Encoding != "" && value.Encoding != "base64" {
			return nil, malformed("unsupported log revision property encoding %q", value.Encoding)
		}
		if value.Encoding == "base64" {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value.Value))
			if err != nil {
				return nil, malformed("invalid log revision property %s", value.Name)
			}
			data = decoded
		}
		entry.RevProps[value.Name] = data
	}
	for _, value := range item.Paths {
		var action svn.LogChangeAction
		switch value.XMLName.Local {
		case "added-path":
			action = svn.LogAdded
		case "deleted-path":
			action = svn.LogDeleted
		case "modified-path":
			action = svn.LogModified
		case "replaced-path":
			action = svn.LogReplaced
		default:
			continue
		}
		kind := svn.NodeUnknown
		if value.NodeKind != "" {
			parsed, err := svn.ParseNodeKind(value.NodeKind)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", svn.ErrRADAVMalformedData, err)
			}
			kind = parsed
		}
		changed := svn.ChangedPath{Path: strings.TrimSpace(value.Path), Action: action, NodeKind: kind, TextModified: parseTristate(value.TextModified), PropsModified: parseTristate(value.PropModified), CopyfromPath: value.CopyfromPath, CopyfromRev: svn.InvalidRevnum}
		if value.CopyfromRev != "" {
			revision, err := strconv.ParseInt(value.CopyfromRev, 10, 64)
			if err != nil {
				return nil, malformed("invalid copyfrom revision")
			}
			changed.CopyfromRev = svn.Revnum(revision)
		}
		entry.ChangedPaths = append(entry.ChangedPaths, changed)
	}
	return entry, nil
}

func parseTristate(value string) svn.Tristate {
	switch value {
	case "true":
		return svn.TristateTrue
	case "false":
		return svn.TristateFalse
	default:
		return svn.TristateUnknown
	}
}
