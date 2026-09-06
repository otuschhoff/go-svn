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
	"strconv"
	"strings"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type reportEntry struct {
	path       string
	url        string
	revision   svn.Revnum
	depth      svn.Depth
	startEmpty bool
	lockToken  string
	deleted    bool
}

type updateReporter struct {
	session      *Session
	ctx          context.Context
	editor       delta.Editor
	operation    string
	revision     svn.Revnum
	target       string
	depth        svn.Depth
	switchURL    string
	sendCopyfrom bool
	ignore       bool
	textDeltas   bool
	entries      []reportEntry
	done         bool
}

type editorNode struct {
	name         string
	dir          delta.DirEditor
	file         delta.FileEditor
	checkedIn    string
	fetchFile    bool
	baseChecksum string
	deltaBase    string
}

func (session *Session) DoUpdate(ctx context.Context, revision svn.Revnum, target string, depth svn.Depth, sendCopyfrom, ignoreAncestry bool, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(ctx, "update", revision, target, depth, "", sendCopyfrom, ignoreAncestry, true, editor)
}

func (session *Session) DoSwitch(ctx context.Context, revision svn.Revnum, target string, depth svn.Depth, switchURL string, sendCopyfrom, ignoreAncestry bool, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(ctx, "switch", revision, target, depth, switchURL, sendCopyfrom, ignoreAncestry, true, editor)
}

func (session *Session) DoStatus(ctx context.Context, target string, revision svn.Revnum, depth svn.Depth, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(ctx, "status", revision, target, depth, "", false, false, false, editor)
}

func (session *Session) DoDiff(ctx context.Context, revision svn.Revnum, target string, depth svn.Depth, ignoreAncestry, textDeltas bool, versusURL string, editor delta.Editor) (ra.Reporter, error) {
	return session.newReporter(ctx, "diff", revision, target, depth, versusURL, false, ignoreAncestry, textDeltas, editor)
}

func (session *Session) newReporter(ctx context.Context, operation string, revision svn.Revnum, target string, depth svn.Depth, switchURL string, sendCopyfrom, ignore, textDeltas bool, editor delta.Editor) (ra.Reporter, error) {
	if editor == nil {
		return nil, fmt.Errorf("%w: nil editor", svn.ErrIncorrectParams)
	}
	if err := validateRelpath(target); err != nil {
		return nil, err
	}
	return &updateReporter{session: session, ctx: ctx, operation: operation, revision: revision, target: target, depth: depth, switchURL: switchURL, sendCopyfrom: sendCopyfrom, ignore: ignore, textDeltas: textDeltas, editor: editor}, nil
}

func (reporter *updateReporter) SetPath(_ context.Context, name string, revision svn.Revnum, depth svn.Depth, startEmpty bool, lockToken string) error {
	if reporter.done {
		return fmt.Errorf("%w: report complete", svn.ErrIncorrectParams)
	}
	if err := validateRelpath(name); err != nil {
		return err
	}
	reporter.entries = append(reporter.entries, reportEntry{path: name, revision: revision, depth: depth, startEmpty: startEmpty, lockToken: lockToken})
	return nil
}

func (reporter *updateReporter) LinkPath(_ context.Context, name, rawURL string, revision svn.Revnum, depth svn.Depth, startEmpty bool, lockToken string) error {
	if reporter.done {
		return fmt.Errorf("%w: report complete", svn.ErrIncorrectParams)
	}
	if err := validateRelpath(name); err != nil {
		return err
	}
	reporter.entries = append(reporter.entries, reportEntry{path: name, url: rawURL, revision: revision, depth: depth, startEmpty: startEmpty, lockToken: lockToken})
	return nil
}

func (reporter *updateReporter) DeletePath(_ context.Context, name string) error {
	if reporter.done {
		return fmt.Errorf("%w: report complete", svn.ErrIncorrectParams)
	}
	if err := validateRelpath(name); err != nil {
		return err
	}
	reporter.entries = append(reporter.entries, reportEntry{path: name, deleted: true})
	return nil
}

func (reporter *updateReporter) AbortReport(context.Context) error { reporter.done = true; return nil }

func (reporter *updateReporter) FinishReport(ctx context.Context) error {
	if reporter.done {
		return fmt.Errorf("%w: report complete", svn.ErrIncorrectParams)
	}
	reporter.done = true
	body := reporter.requestBody()
	reportURL, err := reporter.session.reportResource(ctx)
	if err != nil {
		return err
	}
	err = reporter.session.report(ctx, reportURL, body, func(decoder *xml.Decoder) error {
		return reporter.session.driveEditor(ctx, decoder, reporter.editor)
	})
	if err != nil {
		if abortErr := reporter.editor.AbortEdit(ctx); abortErr != nil {
			return fmt.Errorf("%w; abort edit: %v", err, abortErr)
		}
	}
	return err
}

func (reporter *updateReporter) requestBody() []byte {
	var body strings.Builder
	bulk := strings.EqualFold(reporter.session.info.BulkUpdates, "prefer")
	body.WriteString(`<S:update-report xmlns:S="svn:"`)
	if bulk {
		body.WriteString(` send-all="true"`)
	}
	body.WriteByte('>')
	if !bulk {
		xmlText(&body, "S:include-props", "yes")
	}
	source := reporter.session.url
	if parsed, err := url.Parse(source); err == nil {
		source = parsed.EscapedPath()
	}
	xmlText(&body, "S:src-path", source)
	if reporter.switchURL != "" {
		xmlText(&body, "S:dst-path", reporter.switchURL)
	}
	if reporter.revision.IsValid() {
		xmlText(&body, "S:target-revision", strconv.FormatInt(int64(reporter.revision), 10))
	}
	if reporter.target != "" {
		xmlText(&body, "S:update-target", reporter.target)
	}
	xmlText(&body, "S:depth", reporter.depth.String())
	if reporter.depth == svn.DepthFiles || reporter.depth == svn.DepthEmpty {
		xmlText(&body, "S:recursive", "no")
	}
	if reporter.ignore {
		xmlText(&body, "S:ignore-ancestry", "yes")
	}
	if reporter.sendCopyfrom {
		xmlText(&body, "S:send-copyfrom-args", "yes")
	}
	if !reporter.textDeltas {
		xmlText(&body, "S:text-deltas", "no")
	}
	xmlText(&body, "S:depth", reporter.depth.String())
	for _, entry := range reporter.entries {
		if entry.deleted {
			body.WriteString(`<S:missing>`)
			_ = xml.EscapeText(&body, []byte(entry.path))
			body.WriteString(`</S:missing>`)
			continue
		}
		body.WriteString(`<S:entry rev="`)
		body.WriteString(strconv.FormatInt(int64(entry.revision), 10))
		body.WriteByte('"')
		if entry.depth != svn.DepthUnknown {
			body.WriteString(` depth="`)
			body.WriteString(entry.depth.String())
			body.WriteByte('"')
		}
		if entry.startEmpty {
			body.WriteString(` start-empty="true"`)
		}
		if entry.lockToken != "" {
			body.WriteString(` lock-token="`)
			_ = xml.EscapeText(&body, []byte(entry.lockToken))
			body.WriteByte('"')
		}
		if entry.url != "" {
			body.WriteString(` linkpath="`)
			_ = xml.EscapeText(&body, []byte(entry.url))
			body.WriteByte('"')
		}
		body.WriteByte('>')
		_ = xml.EscapeText(&body, []byte(entry.path))
		body.WriteString(`</S:entry>`)
	}
	body.WriteString(`</S:update-report>`)
	return []byte(body.String())
}

func (session *Session) driveEditor(ctx context.Context, decoder *xml.Decoder, editor delta.Editor) error {
	var stack []editorNode
	explicitClose := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if value.Name.Space == nsDAV && value.Name.Local == "href" {
				if len(stack) > 0 && stack[len(stack)-1].file != nil {
					var href string
					if err := decoder.DecodeElement(&href, &value); err != nil {
						return err
					}
					stack[len(stack)-1].checkedIn = strings.TrimSpace(href)
				}
				continue
			}
			if value.Name.Space != nsSVN {
				continue
			}
			name := attribute(value, "name")
			callbackName := name
			if !explicitClose && len(stack) > 0 && stack[len(stack)-1].name != "" {
				callbackName = path.Join(stack[len(stack)-1].name, name)
			}
			revision := revisionAttribute(value, "rev")
			switch value.Name.Local {
			case "editor-report":
				explicitClose = true
			case "target-revision":
				if err := editor.SetTargetRevision(ctx, revision); err != nil {
					return err
				}
			case "open-root":
				directory, err := editor.OpenRoot(ctx, revision)
				if err != nil {
					return err
				}
				stack = append(stack, editorNode{dir: directory})
			case "open-directory":
				if len(stack) == 0 && !explicitClose {
					directory, err := editor.OpenRoot(ctx, revision)
					if err != nil {
						return err
					}
					stack = append(stack, editorNode{dir: directory})
					continue
				}
				if len(stack) == 0 || stack[len(stack)-1].dir == nil {
					return malformed("open-directory without parent")
				}
				directory, err := stack[len(stack)-1].dir.OpenDirectory(ctx, callbackName, revision)
				if err != nil {
					return err
				}
				stack = append(stack, editorNode{name: callbackName, dir: directory})
			case "add-directory":
				if len(stack) == 0 || stack[len(stack)-1].dir == nil {
					return malformed("add-directory without parent")
				}
				directory, err := stack[len(stack)-1].dir.AddDirectory(ctx, callbackName, copySource(value))
				if err != nil {
					return err
				}
				stack = append(stack, editorNode{name: callbackName, dir: directory})
			case "open-file":
				if len(stack) == 0 || stack[len(stack)-1].dir == nil {
					return malformed("open-file without parent")
				}
				file, err := stack[len(stack)-1].dir.OpenFile(ctx, callbackName, revision)
				if err != nil {
					return err
				}
				stack = append(stack, editorNode{name: callbackName, file: file, deltaBase: session.httpV2RevisionURL(revision, callbackName)})
			case "add-file":
				if len(stack) == 0 || stack[len(stack)-1].dir == nil {
					return malformed("add-file without parent")
				}
				copyFrom := copySource(value)
				file, err := stack[len(stack)-1].dir.AddFile(ctx, callbackName, copyFrom)
				if err != nil {
					return err
				}
				deltaBase := ""
				if copyFrom != nil {
					deltaBase = session.httpV2RevisionURL(copyFrom.Rev, strings.TrimPrefix(copyFrom.Path, "/"))
				}
				stack = append(stack, editorNode{name: callbackName, file: file, deltaBase: deltaBase})
			case "delete-entry":
				if len(stack) == 0 || stack[len(stack)-1].dir == nil {
					return malformed("delete-entry without parent")
				}
				if err := stack[len(stack)-1].dir.DeleteEntry(ctx, callbackName, revision); err != nil {
					return err
				}
			case "absent-directory":
				if err := stack[len(stack)-1].dir.AbsentDirectory(ctx, callbackName); err != nil {
					return err
				}
			case "absent-file":
				if err := stack[len(stack)-1].dir.AbsentFile(ctx, callbackName); err != nil {
					return err
				}
			case "set-prop", "remove-prop", "change-dir-prop", "change-file-prop":
				var text string
				deleted := value.Name.Local == "remove-prop" || attribute(value, "del") != ""
				if !deleted {
					if err := decoder.DecodeElement(&text, &value); err != nil {
						return err
					}
				}
				var data []byte
				if !deleted {
					data = []byte(text)
					if explicitClose || attribute(value, "encoding") == "base64" {
						decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(text))
						if err != nil {
							return malformed("invalid property encoding")
						}
						data = decoded
					}
				}
				if len(stack) == 0 {
					return malformed("property without node")
				}
				if stack[len(stack)-1].file != nil {
					if err := stack[len(stack)-1].file.ChangeProp(ctx, name, data); err != nil {
						return err
					}
				} else if err := stack[len(stack)-1].dir.ChangeProp(ctx, name, data); err != nil {
					return err
				}
			case "txdelta", "apply-textdelta":
				if len(stack) == 0 || stack[len(stack)-1].file == nil {
					return malformed("txdelta without file")
				}
				var text string
				if err := decoder.DecodeElement(&text, &value); err != nil {
					return err
				}
				checksum := attribute(value, "base-checksum")
				if checksum == "" {
					checksum = attribute(value, "checksum")
				}
				if err := applyEncodedDelta(ctx, stack[len(stack)-1].file, checksum, text); err != nil {
					return err
				}
			case "fetch-file":
				if len(stack) == 0 || stack[len(stack)-1].file == nil {
					return malformed("fetch-file without file")
				}
				stack[len(stack)-1].fetchFile = true
				stack[len(stack)-1].baseChecksum = attribute(value, "base-checksum")
			case "close-file":
				if explicitClose {
					if len(stack) == 0 || stack[len(stack)-1].file == nil {
						return malformed("close-file without file")
					}
					node := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					var checksum *svn.Checksum
					if raw := attribute(value, "checksum"); raw != "" {
						parsed, err := svn.ParseChecksum(raw)
						if err != nil {
							return err
						}
						checksum = &parsed
					}
					if err := node.file.Close(ctx, checksum); err != nil {
						return err
					}
				}
			case "close-directory":
				if explicitClose {
					if len(stack) == 0 || stack[len(stack)-1].dir == nil {
						return malformed("close-directory without directory")
					}
					node := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					if err := node.dir.Close(ctx); err != nil {
						return err
					}
				}
			}
		case xml.EndElement:
			if value.Name.Space != nsSVN {
				continue
			}
			if explicitClose {
				continue
			}
			switch value.Name.Local {
			case "open-file", "add-file":
				node := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if node.fetchFile {
					if err := session.fetchDelta(ctx, node.file, node.checkedIn, node.deltaBase, node.baseChecksum); err != nil {
						return err
					}
				}
				if err := node.file.Close(ctx, nil); err != nil {
					return err
				}
			case "open-directory", "add-directory", "open-root":
				node := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if err := node.dir.Close(ctx); err != nil {
					return err
				}
			}
		}
	}
	if len(stack) != 0 {
		return malformed("unclosed editor nodes")
	}
	return editor.CloseEdit(ctx)
}

func applyEncodedDelta(ctx context.Context, file delta.FileEditor, baseChecksum, encoded string) error {
	var base *svn.Checksum
	if baseChecksum != "" {
		parsed, err := svn.ParseChecksum(baseChecksum)
		if err != nil {
			return err
		}
		base = &parsed
	}
	handler, err := file.ApplyTextDelta(ctx, base)
	if err != nil {
		return err
	}
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(strings.TrimSpace(encoded)))
	windows, err := delta.NewSvndiffReader(reader)
	if err != nil {
		return err
	}
	for {
		window, err := windows.NextWindow()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := handler.Window(window); err != nil {
			return err
		}
	}
	return handler.Close()
}

func (session *Session) httpV2RevisionURL(revision svn.Revnum, name string) string {
	if !revision.IsValid() || session.info.RevRootStub == "" {
		return ""
	}
	stub, err := resolveURL(session.url, session.info.RevRootStub)
	if err != nil {
		return ""
	}
	return appendURLPath(stub, strconv.FormatInt(int64(revision), 10), name)
}

func (session *Session) fetchDelta(ctx context.Context, file delta.FileEditor, href, deltaBase, baseChecksum string) error {
	if href == "" {
		return malformed("fetch-file without href")
	}
	resource, err := resolveURL(session.url, href)
	if err != nil {
		return err
	}
	headers := make(http.Header)
	if deltaBase != "" {
		headers.Set("SVN-Delta-Base", deltaBase)
		headers.Set("Accept-Encoding", "svndiff")
	}
	response, err := session.transport.request(ctx, "GET", resource, nil, headers)
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	if returnedBase := response.Header.Get("SVN-Delta-Base"); returnedBase != "" && returnedBase != deltaBase {
		return fmt.Errorf("%w: unexpected delta base %q", svn.ErrRADAVRequestFailed, returnedBase)
	}
	var base *svn.Checksum
	if baseChecksum != "" {
		parsed, err := svn.ParseChecksum(baseChecksum)
		if err != nil {
			return err
		}
		base = &parsed
	}
	handler, err := file.ApplyTextDelta(ctx, base)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]), "application/vnd.svn-svndiff") {
		buffer := make([]byte, 32<<10)
		for {
			count, readErr := response.Body.Read(buffer)
			if count > 0 {
				data := append([]byte(nil), buffer[:count]...)
				window := &delta.Window{TargetLength: count, Ops: []delta.Op{{Kind: delta.OpNew, Length: count}}, NewData: data}
				if err := handler.Window(window); err != nil {
					return err
				}
			}
			if readErr == io.EOF {
				return handler.Close()
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	windows, err := delta.NewSvndiffReader(response.Body)
	if err != nil {
		return err
	}
	for {
		window, err := windows.NextWindow()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := handler.Window(window); err != nil {
			return err
		}
	}
	return handler.Close()
}

func attribute(element xml.StartElement, name string) string {
	for _, value := range element.Attr {
		if value.Name.Local == name {
			return value.Value
		}
	}
	return ""
}

func revisionAttribute(element xml.StartElement, name string) svn.Revnum {
	value, err := strconv.ParseInt(attribute(element, name), 10, 64)
	if err != nil {
		return svn.InvalidRevnum
	}
	return svn.Revnum(value)
}

func copySource(element xml.StartElement) *delta.CopySource {
	name := attribute(element, "copyfrom-path")
	if name == "" {
		return nil
	}
	return &delta.CopySource{Path: name, Rev: revisionAttribute(element, "copyfrom-rev")}
}
