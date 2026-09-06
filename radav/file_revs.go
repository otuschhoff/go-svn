package radav

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

func (session *Session) GetFileRevs(ctx context.Context, name string, startRevision, endRevision svn.Revnum, includeMerged bool, handler ra.FileRevHandler) error {
	if err := validateRelpath(name); err != nil {
		return err
	}
	peg := endRevision
	if startRevision > peg {
		peg = startRevision
	}
	rawURL, err := session.reportURL(ctx, peg)
	if err != nil {
		return err
	}
	var body strings.Builder
	body.WriteString(`<S:file-revs-report xmlns:S="svn:">`)
	xmlText(&body, "S:start-revision", strconv.FormatInt(int64(startRevision), 10))
	xmlText(&body, "S:end-revision", strconv.FormatInt(int64(endRevision), 10))
	if includeMerged {
		body.WriteString(`<S:include-merged-revisions/>`)
	}
	xmlText(&body, "S:path", name)
	body.WriteString(`</S:file-revs-report>`)
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
			if !ok || start.Name.Space != nsSVN || start.Name.Local != "file-rev" {
				continue
			}
			if err := decodeFileRevision(ctx, decoder, start, handler); err != nil {
				return err
			}
		}
	})
}

func decodeFileRevision(ctx context.Context, decoder *xml.Decoder, start xml.StartElement, handler ra.FileRevHandler) error {
	revisionValue, err := strconv.ParseInt(attribute(start, "rev"), 10, 64)
	if err != nil || revisionValue < 0 {
		return malformed("invalid file revision")
	}
	revision := ra.FileRevision{Path: attribute(start, "path"), Revision: svn.Revnum(revisionValue), RevProps: make(svn.Props), PropDiffs: make(svn.Props)}
	delivered := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Space != nsSVN {
				if err := decoder.Skip(); err != nil {
					return err
				}
				continue
			}
			switch value.Name.Local {
			case "rev-prop", "set-prop":
				data, err := decodePropertyElement(decoder, value)
				if err != nil {
					return err
				}
				if value.Name.Local == "rev-prop" {
					revision.RevProps[attribute(value, "name")] = data
				} else {
					revision.PropDiffs[attribute(value, "name")] = data
				}
			case "remove-prop":
				revision.PropDiffs[attribute(value, "name")] = nil
			case "merged-revision":
				revision.Merged = true
			case "txdelta":
				if handler == nil {
					if err := decoder.Skip(); err != nil {
						return err
					}
					delivered = true
					continue
				}
				if err := deliverFileRevisionDelta(ctx, decoder, value, revision, handler); err != nil {
					return err
				}
				delivered = true
			}
		case xml.EndElement:
			if value.Name == start.Name {
				if !delivered && handler != nil {
					return handler(ctx, revision)
				}
				return nil
			}
		}
	}
}

func decodePropertyElement(decoder *xml.Decoder, start xml.StartElement) ([]byte, error) {
	var text string
	if err := decoder.DecodeElement(&text, &start); err != nil {
		return nil, err
	}
	if encoding := attribute(start, "encoding"); encoding != "" {
		if encoding != "base64" {
			return nil, malformed("unsupported property encoding %q", encoding)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
		if err != nil {
			return nil, malformed("invalid base64 property")
		}
		return decoded, nil
	}
	return []byte(text), nil
}

func deliverFileRevisionDelta(ctx context.Context, decoder *xml.Decoder, start xml.StartElement, revision ra.FileRevision, handler ra.FileRevHandler) error {
	file, err := os.CreateTemp("", "go-svn-file-rev-*.b64")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.CharData:
			if _, err := file.Write(value); err != nil {
				return err
			}
		case xml.EndElement:
			if value.Name == start.Name {
				if _, err := file.Seek(0, io.SeekStart); err != nil {
					return err
				}
				windowReader, err := delta.NewSvndiffReader(base64.NewDecoder(base64.StdEncoding, file))
				if err != nil {
					return fmt.Errorf("%w: %v", svn.ErrRADAVMalformedData, err)
				}
				revision.Delta = windowReader
				return handler(ctx, revision)
			}
		}
	}
}
