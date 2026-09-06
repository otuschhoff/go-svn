package radav

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/skel"
)

type commitEditor struct {
	session          *Session
	revprops         svn.Props
	lockTokens       map[string]string
	keepLocks        bool
	callback         func(*ra.CommitInfo) error
	txnURL           string
	txnRootURL       string
	openBatons       int
	transaction      bool
	done             bool
	temporaries      map[string]*commitFile
	deleted          map[string]bool
	svndiffVersion   int
	compressionLevel int
}

type commitDir struct {
	editor       *commitEditor
	name         string
	baseRevision svn.Revnum
	properties   svn.Props
	added        bool
	closed       bool
}

type commitFile struct {
	editor       *commitEditor
	name         string
	baseRevision svn.Revnum
	added        bool
	copied       bool
	properties   svn.Props
	deltaFile    *os.File
	deltaName    string
	baseChecksum *svn.Checksum
	deltaClosed  bool
	closed       bool
}

type commitDelta struct {
	file    *commitFile
	handler delta.WindowHandler
	closed  bool
}

type mergeResponse struct {
	Responses []struct {
		Propstats []struct {
			Status string `xml:"status"`
			Props  struct {
				Version string `xml:"version-name"`
				Date    string `xml:"creationdate"`
				Author  string `xml:"creator-displayname"`
				Error   string `xml:"post-commit-err"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"updated-set>response"`
}

func (session *Session) startCommit(revprops svn.Props, lockTokens map[string]string, keepLocks bool, callback func(*ra.CommitInfo) error) (delta.Editor, error) {
	if session.info.MeResource == "" || session.info.TxnRootStub == "" || session.info.TxnStub == "" {
		return nil, fmt.Errorf("%w: server does not advertise Subversion HTTPv2 transaction resources", svn.ErrUnsupportedFeature)
	}
	for name := range lockTokens {
		if err := validateRelpath(name); err != nil {
			return nil, err
		}
	}
	properties := cloneDAVProps(revprops)
	if properties == nil {
		properties = make(svn.Props)
	}
	if session.info.Capabilities[ra.CapabilityEphemeralTxnprops] {
		properties["svn:txn-client-compat-version"] = []byte("1.14.5")
		properties["svn:txn-user-agent"] = []byte(userAgent)
	}
	version, compression := 0, zlib.NoCompression
	if session.info.Capabilities[ra.CapabilitySvndiff2] {
		version, compression = 2, zlib.BestSpeed
	} else if session.info.Capabilities[ra.CapabilitySvndiff1] {
		version, compression = 1, zlib.DefaultCompression
	}
	return &commitEditor{
		session: session, revprops: properties, lockTokens: cloneStrings(lockTokens), keepLocks: keepLocks, callback: callback,
		temporaries: make(map[string]*commitFile), deleted: make(map[string]bool), svndiffVersion: version, compressionLevel: compression,
	}, nil
}

func (editor *commitEditor) SetTargetRevision(context.Context, svn.Revnum) error {
	return editor.ensureActive()
}

func (editor *commitEditor) OpenRoot(ctx context.Context, revision svn.Revnum) (delta.DirEditor, error) {
	if err := editor.ensureActive(); err != nil {
		return nil, err
	}
	if editor.transaction {
		return nil, fmt.Errorf("%w: commit root is already open", svn.ErrIncorrectParams)
	}
	if err := editor.createTransaction(ctx); err != nil {
		return nil, err
	}
	editor.openBatons = 1
	return &commitDir{editor: editor, baseRevision: revision, properties: make(svn.Props)}, nil
}

func (editor *commitEditor) createTransaction(ctx context.Context) error {
	withProperties := editor.session.info.SupportedPosts["create-txn-with-props"]
	command := "create-txn"
	request := skel.NewList(skel.NewString(command))
	if withProperties {
		command = "create-txn-with-props"
		request = skel.NewList(skel.NewString(command), skel.PropsToProplist(editor.revprops))
	}
	body, err := request.MarshalBinary()
	if err != nil {
		return err
	}
	resource, err := resolveURL(editor.session.url, editor.session.info.MeResource)
	if err != nil {
		return err
	}
	response, err := editor.session.transport.request(ctx, http.MethodPost, resource, body, http.Header{"Content-Type": []string{"application/vnd.svn-skel"}})
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode != http.StatusCreated {
		return responseError(response)
	}
	transactionName := response.Header.Get("SVN-Txn-Name")
	rootStub, txnStub := editor.session.info.TxnRootStub, editor.session.info.TxnStub
	if transactionName == "" {
		transactionName = response.Header.Get("SVN-VTxn-Name")
		rootStub, txnStub = editor.session.info.VTxnRootStub, editor.session.info.VTxnStub
	}
	if transactionName == "" || rootStub == "" || txnStub == "" {
		return malformed("POST did not return usable transaction information")
	}
	txnRoot, err := resolveURL(editor.session.url, rootStub)
	if err != nil {
		return err
	}
	txn, err := resolveURL(editor.session.url, txnStub)
	if err != nil {
		return err
	}
	editor.txnRootURL = appendURLPath(txnRoot, transactionName, editor.sessionAnchor())
	editor.txnURL = appendURLPath(txn, transactionName)
	editor.transaction = true
	if !withProperties && len(editor.revprops) > 0 {
		if err := editor.proppatch(ctx, editor.txnURL, svn.InvalidRevnum, "", editor.revprops); err != nil {
			_ = editor.deleteTransaction(ctx)
			return err
		}
	}
	return nil
}

func (editor *commitEditor) sessionAnchor() string {
	return editor.session.sessionAnchor()
}

func (session *Session) sessionAnchor() string {
	repository, repositoryErr := url.Parse(session.info.RepositoryRoot)
	current, currentErr := url.Parse(session.url)
	if repositoryErr != nil || currentErr != nil || repository.Scheme != current.Scheme || repository.Host != current.Host {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(current.Path, strings.TrimSuffix(repository.Path, "/")), "/")
}

func (editor *commitEditor) CloseEdit(ctx context.Context) error {
	if err := editor.ensureActive(); err != nil {
		return err
	}
	if !editor.transaction || editor.openBatons != 0 {
		return fmt.Errorf("%w: closing commit with %d open editor batons", svn.ErrFSIncorrectEditorCompletion, editor.openBatons)
	}
	body := editor.mergeBody()
	headers := http.Header{"Content-Type": []string{contentXML}}
	if !editor.keepLocks && len(editor.lockTokens) > 0 {
		headers.Set("X-SVN-Options", "release-locks")
	}
	response, err := editor.session.transport.request(ctx, "MERGE", editor.session.url, body, headers)
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	info, err := parseMergeResponse(response, editor.session.info.RepositoryRoot)
	if err != nil {
		return err
	}
	editor.done = true
	editor.txnURL = ""
	if info.Revision.IsValid() {
		editor.session.info.Youngest = info.Revision
	}
	if editor.callback != nil {
		return editor.callback(info)
	}
	return nil
}

func (editor *commitEditor) AbortEdit(ctx context.Context) error {
	if editor.done {
		return nil
	}
	for _, file := range editor.temporaries {
		file.cleanup()
	}
	if !editor.transaction || editor.txnURL == "" {
		editor.done = true
		return nil
	}
	if err := editor.deleteTransaction(ctx); err != nil {
		return err
	}
	editor.done = true
	return nil
}

func (editor *commitEditor) deleteTransaction(ctx context.Context) error {
	response, err := editor.session.transport.request(ctx, http.MethodDelete, editor.txnURL, nil, nil)
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusForbidden && response.StatusCode != http.StatusNotFound {
		return responseError(response)
	}
	editor.transaction = false
	editor.txnURL = ""
	return nil
}

func (editor *commitEditor) ensureActive() error {
	if editor.done {
		return fmt.Errorf("%w: commit editor is already complete", svn.ErrIncorrectParams)
	}
	return nil
}

func (directory *commitDir) DeleteEntry(ctx context.Context, name string, revision svn.Revnum) error {
	if err := directory.validateChild(name); err != nil {
		return err
	}
	headers := directory.editor.mutationHeaders(name, revision)
	if err := directory.editor.expectEmpty(ctx, http.MethodDelete, directory.editor.nodeURL(name), nil, headers, http.StatusNoContent); err != nil {
		return err
	}
	directory.editor.deleted[name] = true
	return nil
}

func (directory *commitDir) AddDirectory(ctx context.Context, name string, source *delta.CopySource) (delta.DirEditor, error) {
	if err := directory.validateChild(name); err != nil {
		return nil, err
	}
	resource := directory.editor.nodeURL(name)
	if source == nil {
		if err := directory.editor.expectEmpty(ctx, "MKCOL", resource, nil, directory.editor.recursiveLockHeaders(name), http.StatusCreated); err != nil {
			return nil, err
		}
	} else if err := directory.editor.copy(ctx, name, source, true); err != nil {
		return nil, err
	}
	directory.editor.openBatons++
	return &commitDir{editor: directory.editor, name: name, baseRevision: svn.InvalidRevnum, properties: make(svn.Props), added: true}, nil
}

func (directory *commitDir) OpenDirectory(_ context.Context, name string, revision svn.Revnum) (delta.DirEditor, error) {
	if err := directory.validateChild(name); err != nil {
		return nil, err
	}
	directory.editor.openBatons++
	return &commitDir{editor: directory.editor, name: name, baseRevision: revision, properties: make(svn.Props)}, nil
}

func (directory *commitDir) ChangeProp(_ context.Context, name string, value []byte) error {
	if directory.closed {
		return fmt.Errorf("%w: directory is closed", svn.ErrIncorrectParams)
	}
	directory.properties[name] = append([]byte(nil), value...)
	if value == nil {
		directory.properties[name] = nil
	}
	return nil
}

func (*commitDir) AbsentDirectory(context.Context, string) error { return nil }

func (directory *commitDir) AddFile(ctx context.Context, name string, source *delta.CopySource) (delta.FileEditor, error) {
	if err := directory.validateChild(name); err != nil {
		return nil, err
	}
	file := &commitFile{editor: directory.editor, name: name, baseRevision: svn.InvalidRevnum, added: true, properties: make(svn.Props)}
	if source != nil {
		if err := directory.editor.copy(ctx, name, source, false); err != nil {
			return nil, err
		}
		file.copied = true
	} else if !directory.added && !directory.editor.wasDeleted(name) {
		response, err := directory.editor.session.transport.request(ctx, http.MethodHead, appendURLPath(directory.editor.session.url, name), nil, nil)
		if err != nil {
			return nil, err
		}
		defer drainAndClose(response.Body)
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return nil, fmt.Errorf("%w: file %s already exists", svn.ErrFSAlreadyExists, name)
		}
		if response.StatusCode != http.StatusNotFound {
			return nil, responseError(response)
		}
	}
	directory.editor.openBatons++
	return file, nil
}

func (directory *commitDir) OpenFile(_ context.Context, name string, revision svn.Revnum) (delta.FileEditor, error) {
	if err := directory.validateChild(name); err != nil {
		return nil, err
	}
	directory.editor.openBatons++
	return &commitFile{editor: directory.editor, name: name, baseRevision: revision, properties: make(svn.Props)}, nil
}

func (*commitDir) AbsentFile(context.Context, string) error { return nil }

func (directory *commitDir) Close(ctx context.Context) error {
	if directory.closed {
		return fmt.Errorf("%w: directory is already closed", svn.ErrIncorrectParams)
	}
	directory.closed = true
	directory.editor.openBatons--
	if len(directory.properties) > 0 {
		if err := directory.editor.proppatch(ctx, directory.editor.nodeURL(directory.name), directory.baseRevision, directory.name, directory.properties); err != nil {
			return err
		}
	}
	return nil
}

func (directory *commitDir) validateChild(name string) error {
	if directory.closed {
		return fmt.Errorf("%w: directory is closed", svn.ErrIncorrectParams)
	}
	if err := validateRelpath(name); err != nil {
		return err
	}
	if directory.name != "" && name != directory.name && !strings.HasPrefix(name, directory.name+"/") {
		return fmt.Errorf("%w: %q is not below %q", svn.ErrBadFilename, name, directory.name)
	}
	return directory.editor.ensureActive()
}

func (file *commitFile) ApplyTextDelta(_ context.Context, base *svn.Checksum) (delta.WindowHandler, error) {
	if file.closed || file.deltaFile != nil {
		return nil, fmt.Errorf("%w: invalid text delta state", svn.ErrIncorrectParams)
	}
	temporary, err := os.CreateTemp("", "go-svn-radav-delta-*")
	if err != nil {
		return nil, err
	}
	file.deltaFile = temporary
	file.deltaName = temporary.Name()
	file.baseChecksum = base
	file.editor.temporaries[file.deltaName] = file
	handler := delta.NewSvndiffWriter(temporary, file.editor.svndiffVersion, file.editor.compressionLevel)
	return &commitDelta{file: file, handler: handler}, nil
}

func (stream *commitDelta) Window(window *delta.Window) error {
	if stream.closed {
		return fmt.Errorf("text delta is closed")
	}
	return stream.handler.Window(window)
}

func (stream *commitDelta) Close() error {
	if stream.closed {
		return fmt.Errorf("text delta is already closed")
	}
	stream.closed = true
	if err := stream.handler.Close(); err != nil {
		return err
	}
	stream.file.deltaClosed = true
	return stream.file.deltaFile.Close()
}

func (file *commitFile) ChangeProp(_ context.Context, name string, value []byte) error {
	if file.closed {
		return fmt.Errorf("%w: file is closed", svn.ErrIncorrectParams)
	}
	file.properties[name] = append([]byte(nil), value...)
	if value == nil {
		file.properties[name] = nil
	}
	return nil
}

func (file *commitFile) Close(ctx context.Context, checksum *svn.Checksum) error {
	if file.closed {
		return fmt.Errorf("%w: file is already closed", svn.ErrIncorrectParams)
	}
	file.closed = true
	file.editor.openBatons--
	defer file.cleanup()
	if file.deltaFile != nil && !file.deltaClosed {
		return fmt.Errorf("%w: text delta is not closed", svn.ErrFSIncorrectEditorCompletion)
	}
	if file.deltaName != "" || file.added && !file.copied {
		if err := file.put(ctx, checksum); err != nil {
			return err
		}
	}
	if len(file.properties) > 0 {
		if err := file.editor.proppatch(ctx, file.editor.nodeURL(file.name), file.baseRevision, file.name, file.properties); err != nil {
			return err
		}
	}
	return nil
}

func (file *commitFile) put(ctx context.Context, checksum *svn.Checksum) error {
	headers := file.editor.mutationHeaders(file.name, file.baseRevision)
	if file.deltaName == "" {
		headers.Set("Content-Type", "text/plain")
	} else {
		headers.Set("Content-Type", "application/vnd.svn-svndiff")
	}
	if file.baseChecksum != nil {
		headers.Set("X-SVN-Base-Fulltext-MD5", file.baseChecksum.Hex())
	}
	if checksum != nil {
		headers.Set("X-SVN-Result-Fulltext-MD5", checksum.Hex())
	}
	var response *http.Response
	var err error
	if file.deltaName != "" {
		response, err = file.editor.session.transport.requestFile(ctx, http.MethodPut, file.editor.nodeURL(file.name), file.deltaName, headers)
	} else {
		response, err = file.editor.session.transport.request(ctx, http.MethodPut, file.editor.nodeURL(file.name), nil, headers)
	}
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	expected := http.StatusNoContent
	if file.added && !file.copied {
		expected = http.StatusCreated
	}
	if response.StatusCode != expected {
		return responseError(response)
	}
	if remote := response.Header.Get("X-SVN-Result-Fulltext-MD5"); remote != "" && checksum != nil && remote != checksum.Hex() {
		return fmt.Errorf("%w: result checksum mismatch for %s", svn.ErrChecksumMismatch, file.name)
	}
	return nil
}

func (editor *commitEditor) wasDeleted(name string) bool {
	for name != "" {
		if editor.deleted[name] {
			return true
		}
		index := strings.LastIndexByte(name, '/')
		if index < 0 {
			break
		}
		name = name[:index]
	}
	return false
}

func (file *commitFile) cleanup() {
	if file.deltaFile != nil {
		_ = file.deltaFile.Close()
	}
	if file.deltaName != "" {
		delete(file.editor.temporaries, file.deltaName)
		_ = os.Remove(file.deltaName)
	}
}

func (editor *commitEditor) nodeURL(name string) string {
	return appendURLPath(editor.txnRootURL, name)
}

func (editor *commitEditor) copy(ctx context.Context, name string, source *delta.CopySource, directory bool) error {
	sourceURL, err := editor.copySourceURL(ctx, source)
	if err != nil {
		return err
	}
	headers := editor.recursiveLockHeaders(name)
	headers.Set("Destination", editor.nodeURL(name))
	headers.Set("Overwrite", "F")
	if directory {
		headers.Set("Depth", "infinity")
	}
	return editor.expectEmpty(ctx, "COPY", sourceURL, nil, headers, http.StatusCreated)
}

func (editor *commitEditor) copySourceURL(ctx context.Context, source *delta.CopySource) (string, error) {
	if source == nil || !source.Rev.IsValid() {
		return "", fmt.Errorf("%w: invalid copy source", svn.ErrIncorrectParams)
	}
	root, err := url.Parse(editor.session.info.RepositoryRoot)
	if err != nil {
		return "", err
	}
	candidate, err := url.Parse(source.Path)
	if err != nil {
		return "", err
	}
	if candidate.Scheme != root.Scheme || candidate.Host != root.Host {
		return "", fmt.Errorf("%w: copy source is outside repository", svn.ErrRAIllegalURL)
	}
	rootPath := strings.TrimSuffix(root.Path, "/")
	if candidate.Path != rootPath && !strings.HasPrefix(candidate.Path, rootPath+"/") {
		return "", fmt.Errorf("%w: copy source is outside repository", svn.ErrRAIllegalURL)
	}
	name := strings.TrimPrefix(strings.TrimPrefix(candidate.Path, rootPath), "/")
	resource, _, err := editor.session.repositoryRevisionURL(ctx, source.Rev, name)
	return resource, err
}

func (editor *commitEditor) mutationHeaders(name string, revision svn.Revnum) http.Header {
	headers := make(http.Header)
	if revision.IsValid() {
		headers.Set("X-SVN-Version-Name", strconv.FormatInt(int64(revision), 10))
	}
	if token := editor.lockTokens[name]; token != "" && !editor.deleted[name] {
		headers.Set("If", "<"+appendURLPath(editor.session.url, name)+"> (<"+token+">)")
		if editor.keepLocks {
			headers.Set("X-SVN-Options", "keep-locks")
		}
	}
	return headers
}

func (editor *commitEditor) recursiveLockHeaders(name string) http.Header {
	headers := make(http.Header)
	var conditions []string
	for lockedPath, token := range editor.lockTokens {
		if editor.deleted[lockedPath] {
			continue
		}
		if lockedPath == name || strings.HasPrefix(lockedPath, strings.TrimSuffix(name, "/")+"/") {
			conditions = append(conditions, "<"+appendURLPath(editor.session.url, lockedPath)+"> (<"+token+">)")
		}
	}
	sort.Strings(conditions)
	if len(conditions) > 0 {
		headers.Set("If", strings.Join(conditions, " "))
	}
	return headers
}

func (editor *commitEditor) expectEmpty(ctx context.Context, method, resource string, body []byte, headers http.Header, statuses ...int) error {
	response, err := editor.session.transport.request(ctx, method, resource, body, headers)
	if err != nil {
		return err
	}
	defer drainAndClose(response.Body)
	for _, status := range statuses {
		if response.StatusCode == status {
			return nil
		}
	}
	return responseError(response)
}

func (editor *commitEditor) proppatch(ctx context.Context, resource string, revision svn.Revnum, name string, properties svn.Props) error {
	body, err := propertyUpdateXML(properties, nil)
	if err != nil {
		return err
	}
	headers := editor.mutationHeaders(name, revision)
	headers.Set("Content-Type", contentXML)
	response, err := editor.session.transport.request(ctx, "PROPPATCH", resource, body, headers)
	if err != nil {
		return err
	}
	return checkProppatchResponse(response)
}

type proppatchMultiStatus struct {
	Responses []struct {
		Status    string `xml:"status"`
		Propstats []struct {
			Status string `xml:"status"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func checkProppatchResponse(response *http.Response) error {
	defer drainAndClose(response.Body)
	if response.StatusCode != http.StatusMultiStatus {
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return nil
		}
		return responseError(response)
	}
	var multistatus proppatchMultiStatus
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if err := decodeDAVXML(body, &multistatus); err != nil {
		if valid, statusErr := malformedProppatchStatuses(body); valid {
			return statusErr
		}
		return malformed("invalid PROPPATCH response: %v: %s", err, body)
	}
	for _, item := range multistatus.Responses {
		statuses := []string{item.Status}
		for _, propstat := range item.Propstats {
			statuses = append(statuses, propstat.Status)
		}
		for _, status := range statuses {
			if status != "" && !davStatusSuccessful(status) {
				return &svn.Error{Code: svn.ErrRADAVProppatchFailed, Message: "PROPPATCH failed: " + strings.TrimSpace(status)}
			}
		}
	}
	return nil
}

func malformedProppatchStatuses(body []byte) (bool, error) {
	remaining := string(body)
	found := false
	for {
		start := strings.Index(remaining, ">HTTP/")
		if start < 0 {
			break
		}
		remaining = remaining[start+1:]
		end := strings.IndexByte(remaining, '<')
		if end < 0 {
			return false, nil
		}
		status := strings.TrimSpace(remaining[:end])
		found = true
		if !davStatusSuccessful(status) {
			return true, &svn.Error{Code: svn.ErrRADAVProppatchFailed, Message: "PROPPATCH failed: " + status}
		}
		remaining = remaining[end+1:]
	}
	return found, nil
}

func davStatusSuccessful(status string) bool {
	fields := strings.Fields(status)
	if len(fields) < 2 {
		return false
	}
	code, err := strconv.Atoi(fields[1])
	return err == nil && code >= 200 && code < 300
}

func (editor *commitEditor) mergeBody() []byte {
	var output bytes.Buffer
	output.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:merge xmlns:D="DAV:" xmlns:S="svn:"><D:source><D:href>`)
	_ = xml.EscapeText(&output, []byte(editor.txnURL))
	output.WriteString(`</D:href></D:source><D:no-auto-merge/><D:no-checkout/><D:prop><D:checked-in/><D:version-name/><D:resourcetype/><D:creationdate/><D:creator-displayname/></D:prop>`)
	if len(editor.lockTokens) > 0 {
		output.WriteString(`<S:lock-token-list>`)
		paths := sortedDAVKeys(editor.lockTokens)
		for _, name := range paths {
			output.WriteString(`<S:lock><S:lock-path>`)
			_ = xml.EscapeText(&output, []byte(name))
			output.WriteString(`</S:lock-path><S:lock-token>`)
			_ = xml.EscapeText(&output, []byte(editor.lockTokens[name]))
			output.WriteString(`</S:lock-token></S:lock>`)
		}
		output.WriteString(`</S:lock-token-list>`)
	}
	output.WriteString(`</D:merge>`)
	return output.Bytes()
}

func parseMergeResponse(response *http.Response, repositoryRoot string) (*ra.CommitInfo, error) {
	var parsed mergeResponse
	if err := xml.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return nil, malformed("invalid MERGE response: %v", err)
	}
	for _, item := range parsed.Responses {
		for _, stat := range item.Propstats {
			if !strings.Contains(stat.Status, " 200 ") || strings.TrimSpace(stat.Props.Version) == "" {
				continue
			}
			revision, err := strconv.ParseInt(strings.TrimSpace(stat.Props.Version), 10, 64)
			if err != nil {
				return nil, malformed("invalid committed revision %q", stat.Props.Version)
			}
			info := &ra.CommitInfo{Revision: svn.Revnum(revision), Author: stat.Props.Author, PostCommitError: stat.Props.Error, ReposRoot: repositoryRoot}
			if value := strings.TrimSpace(stat.Props.Date); value != "" {
				info.Date, err = time.Parse(time.RFC3339Nano, value)
				if err != nil {
					return nil, malformed("invalid commit date %q", value)
				}
			}
			return info, nil
		}
	}
	return nil, malformed("MERGE response contains no committed revision")
}

func propertyUpdateXML(properties svn.Props, oldValues map[string]*[]byte) ([]byte, error) {
	var sets, removes []string
	for _, name := range sortedDAVKeys(properties) {
		value := properties[name]
		old, hasOld := oldValues[name]
		element, err := propertyElement(name, value, old, hasOld)
		if err != nil {
			return nil, err
		}
		if value != nil || hasOld {
			sets = append(sets, element)
		} else {
			removes = append(removes, element)
		}
	}
	var output strings.Builder
	output.WriteString(`<?xml version="1.0" encoding="utf-8"?><D:propertyupdate xmlns:D="DAV:" xmlns:V="http://subversion.tigris.org/xmlns/dav/" xmlns:C="http://subversion.tigris.org/xmlns/custom/" xmlns:S="http://subversion.tigris.org/xmlns/svn/">`)
	if len(sets) > 0 {
		output.WriteString(`<D:set><D:prop>`)
		output.WriteString(strings.Join(sets, ""))
		output.WriteString(`</D:prop></D:set>`)
	}
	if len(removes) > 0 {
		output.WriteString(`<D:remove><D:prop>`)
		output.WriteString(strings.Join(removes, ""))
		output.WriteString(`</D:prop></D:remove>`)
	}
	output.WriteString(`</D:propertyupdate>`)
	return []byte(output.String()), nil
}

func propertyElement(name string, value []byte, old *[]byte, hasOld bool) (string, error) {
	prefix, local := "C", name
	if strings.HasPrefix(name, "svn:") {
		prefix, local = "S", strings.TrimPrefix(name, "svn:")
	}
	if local == "" || strings.ContainsAny(local, "<>\"' \t\r\n") {
		return "", fmt.Errorf("%w: invalid property name %q", svn.ErrBadPropertyValue, name)
	}
	var output strings.Builder
	output.WriteByte('<')
	output.WriteString(prefix)
	output.WriteByte(':')
	output.WriteString(local)
	encoded, encoding := encodePropertyValue(value)
	if value == nil {
		output.WriteString(` V:old-value-absent="1"`)
	} else if encoding != "" {
		output.WriteString(` V:encoding="base64"`)
	}
	output.WriteString(">")
	if hasOld {
		oldEncoded, oldEncoding := encodePropertyValue(pointerBytes(old))
		output.WriteString(`<V:old-value`)
		if old == nil {
			output.WriteString(` V:old-value-absent="1"`)
		} else if oldEncoding != "" {
			output.WriteString(` V:encoding="base64"`)
		}
		output.WriteByte('>')
		output.WriteString(oldEncoded)
		output.WriteString(`</V:old-value>`)
	}
	output.WriteString(encoded)
	output.WriteString("</")
	output.WriteString(prefix)
	output.WriteByte(':')
	output.WriteString(local)
	output.WriteByte('>')
	return output.String(), nil
}

func encodePropertyValue(value []byte) (string, string) {
	if value == nil {
		return "", ""
	}
	if !xmlSafe(value) {
		return base64.StdEncoding.EncodeToString(value), "base64"
	}
	var output strings.Builder
	_ = xml.EscapeText(&output, value)
	return output.String(), ""
}

func xmlSafe(value []byte) bool {
	if !utf8.Valid(value) {
		return false
	}
	for len(value) > 0 {
		character, size := utf8.DecodeRune(value)
		if character != '\t' && character != '\n' && character != '\r' && (character < 0x20 || character == 0xfffe || character == 0xffff) {
			return false
		}
		value = value[size:]
	}
	return true
}

func pointerBytes(value *[]byte) []byte {
	if value == nil {
		return nil
	}
	return *value
}

func sortedDAVKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneDAVProps(properties svn.Props) svn.Props {
	if properties == nil {
		return nil
	}
	copy := make(svn.Props, len(properties))
	for name, value := range properties {
		copy[name] = append([]byte(nil), value...)
	}
	return copy
}

func cloneStrings(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for name, value := range values {
		copy[name] = value
	}
	return copy
}
