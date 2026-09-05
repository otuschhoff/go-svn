package rasvn

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/oliver-tuschhoff/go-svn/delta"
	"github.com/oliver-tuschhoff/go-svn/mergeinfo"
	"github.com/oliver-tuschhoff/go-svn/ra"
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type Session struct {
	conn          *connection
	url           string
	repositoryURL string
	uuid          string
	capabilities  map[string]bool
}

func init() {
	ra.Register("svn", openSession)
	ra.Register("svn+*", openSession)
}

func openSession(ctx context.Context, parsed *url.URL, callbacks *ra.Callbacks) (ra.Session, string, error) {
	var conn *connection
	var err error
	if parsed.Scheme == "svn" {
		conn, err = dialConnection(ctx, parsed, callbacks)
	} else {
		conn, err = tunnelConnection(ctx, parsed, callbacks)
	}
	if err != nil {
		return nil, "", err
	}
	wireURL := *parsed
	if wireURL.User != nil {
		wireURL.User = url.User(wireURL.User.Username())
	}
	info, err := conn.handshake(ctx, wireURL.String())
	if err != nil {
		_ = conn.Close()
		return nil, "", err
	}
	session := &Session{
		conn: conn, url: wireURL.String(), repositoryURL: info.repositoryURL,
		uuid: info.uuid, capabilities: info.capabilities,
	}
	return session, session.url, nil
}

func (session *Session) URL() string { return session.url }

func (session *Session) Reparent(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	current, currentErr := url.Parse(session.url)
	if err != nil || currentErr != nil || parsed.Scheme != current.Scheme || parsed.Host == "" || parsed.Host != current.Host {
		return fmt.Errorf("%w: %q", svn.ErrRAIllegalURL, rawURL)
	}
	if _, err := session.conn.command(ctx, "reparent", String([]byte(rawURL))); err != nil {
		return err
	}
	session.url = rawURL
	return nil
}

func (session *Session) RepositoryRoot(context.Context) (string, error) {
	return session.repositoryURL, nil
}

func (session *Session) UUID(context.Context) (string, error) { return session.uuid, nil }

func (session *Session) HasCapability(_ context.Context, capability ra.Capability) (bool, error) {
	wireName := string(capability)
	if capability == ra.CapabilitySvndiff2 {
		wireName = "accepts-svndiff2"
	}
	return session.capabilities[wireName], nil
}

func (session *Session) LatestRevision(ctx context.Context) (svn.Revnum, error) {
	items, err := session.conn.command(ctx, "get-latest-rev")
	if err != nil {
		return svn.InvalidRevnum, err
	}
	if len(items) != 1 || items[0].Kind != NumberKind || items[0].Number > uint64(^uint64(0)>>1) {
		return svn.InvalidRevnum, malformed("invalid latest revision response")
	}
	return svn.Revnum(items[0].Number), nil
}

func (session *Session) DatedRevision(ctx context.Context, date time.Time) (svn.Revnum, error) {
	items, err := session.conn.command(ctx, "get-dated-rev", String([]byte(svn.FormatDate(date))))
	if err != nil {
		return svn.InvalidRevnum, err
	}
	if len(items) != 1 || items[0].Kind != NumberKind {
		return svn.InvalidRevnum, malformed("invalid dated revision response")
	}
	return svn.Revnum(items[0].Number), nil
}

func (session *Session) RevProps(ctx context.Context, revision svn.Revnum) (svn.Props, error) {
	items, err := session.conn.command(ctx, "rev-proplist", Number(uint64(revision)))
	if err != nil {
		return nil, err
	}
	if len(items) != 1 || items[0].Kind != ListKind {
		return nil, malformed("invalid revision properties response")
	}
	return parseProps(items[0].List)
}

func (session *Session) RevProp(ctx context.Context, revision svn.Revnum, name string) ([]byte, bool, error) {
	items, err := session.conn.command(ctx, "rev-prop", Number(uint64(revision)), String([]byte(name)))
	if err != nil {
		return nil, false, err
	}
	optional, err := parseOptional(items)
	if err != nil {
		return nil, false, err
	}
	if len(optional) == 0 {
		return nil, false, nil
	}
	if len(optional) != 1 || optional[0].Kind != StringKind {
		return nil, false, malformed("invalid revision property response")
	}
	return optional[0].String, true, nil
}

func (session *Session) CheckPath(ctx context.Context, path string, revision svn.Revnum) (svn.NodeKind, error) {
	items, err := session.conn.command(ctx, "check-path", String([]byte(path)), optionalRevision(revision))
	if err != nil {
		return svn.NodeUnknown, err
	}
	if len(items) != 1 || items[0].Kind != WordKind {
		return svn.NodeUnknown, malformed("invalid check-path response")
	}
	return svn.ParseNodeKind(items[0].Word)
}

func (session *Session) Stat(ctx context.Context, path string, revision svn.Revnum) (*svn.Dirent, error) {
	items, err := session.conn.command(ctx, "stat", String([]byte(path)), optionalRevision(revision))
	if err != nil {
		return nil, err
	}
	optional, err := parseOptional(items)
	if err != nil {
		return nil, err
	}
	if len(optional) == 0 {
		return nil, nil
	}
	if len(optional) != 1 || optional[0].Kind != ListKind {
		return nil, malformed("invalid stat response")
	}
	return parseDirent(optional[0].List, "")
}

func (session *Session) GetLock(ctx context.Context, path string) (*svn.Lock, error) {
	items, err := session.conn.command(ctx, "get-lock", String([]byte(path)))
	if err != nil {
		return nil, err
	}
	optional, err := parseOptional(items)
	if err != nil {
		return nil, err
	}
	if len(optional) == 0 {
		return nil, nil
	}
	if len(optional) != 1 || optional[0].Kind != ListKind {
		return nil, malformed("invalid get-lock response")
	}
	return parseLock(optional[0].List)
}

func (session *Session) GetFile(ctx context.Context, path string, revision svn.Revnum, destination io.Writer, wantProps bool) (resultRevision svn.Revnum, resultProps svn.Props, resultErr error) {
	resultRevision = svn.InvalidRevnum
	if destination == nil {
		destination = io.Discard
	}
	checksum := md5.New()
	destination = io.MultiWriter(destination, checksum)
	err := session.conn.streamCommand(ctx, "get-file", func() error {
		items, err := session.conn.readResponse()
		if err != nil {
			return err
		}
		if len(items) < 3 || items[0].Kind != ListKind || items[1].Kind != NumberKind || items[2].Kind != ListKind {
			return malformed("invalid get-file response")
		}
		var expectedChecksum string
		if len(items[0].List) == 1 && items[0].List[0].Kind == StringKind {
			expectedChecksum = string(items[0].List[0].String)
		} else if len(items[0].List) != 0 {
			return malformed("invalid file checksum")
		}
		resultRevision = svn.Revnum(items[1].Number)
		resultProps, err = parseProps(items[2].List)
		if err != nil {
			return err
		}
		for {
			chunk, err := session.conn.reader.Decode()
			if err != nil {
				return err
			}
			if chunk.Kind != StringKind {
				return malformed("file chunk is not a string")
			}
			if len(chunk.String) == 0 {
				break
			}
			if _, err := destination.Write(chunk.String); err != nil {
				return err
			}
		}
		if _, err := session.conn.readResponse(); err != nil {
			return err
		}
		if expectedChecksum != "" && !equalHex(expectedChecksum, checksum.Sum(nil)) {
			return fmt.Errorf("%w: expected MD5 %s, got %x", svn.ErrChecksumMismatch, expectedChecksum, checksum.Sum(nil))
		}
		return nil
	}, String([]byte(path)), optionalRevision(revision), Word(boolWord(wantProps)), Word("true"))
	return resultRevision, resultProps, err
}

func (session *Session) GetDir(ctx context.Context, path string, revision svn.Revnum, fields svn.DirentFields) ([]svn.Dirent, svn.Revnum, svn.Props, error) {
	fieldItems := direntFieldItems(fields)
	items, err := session.conn.command(ctx, "get-dir", String([]byte(path)), optionalRevision(revision), Word("true"), Word("true"), List(fieldItems...))
	if err != nil {
		return nil, svn.InvalidRevnum, nil, err
	}
	if len(items) < 3 || items[0].Kind != NumberKind || items[1].Kind != ListKind || items[2].Kind != ListKind {
		return nil, svn.InvalidRevnum, nil, malformed("invalid get-dir response")
	}
	props, err := parseProps(items[1].List)
	if err != nil {
		return nil, svn.InvalidRevnum, nil, err
	}
	entries := make([]svn.Dirent, 0, len(items[2].List))
	for _, item := range items[2].List {
		if item.Kind != ListKind || len(item.List) == 0 || item.List[0].Kind != StringKind {
			return nil, svn.InvalidRevnum, nil, malformed("invalid get-dir entry")
		}
		entry, err := parseDirent(item.List[1:], string(item.List[0].String))
		if err != nil {
			return nil, svn.InvalidRevnum, nil, err
		}
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
		entries = append(entries, *entry)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Path < entries[right].Path })
	return entries, svn.Revnum(items[0].Number), props, nil
}

func (session *Session) GetLocks(ctx context.Context, path string, depth svn.Depth) (map[string]*svn.Lock, error) {
	items, err := session.conn.command(ctx, "get-locks", String([]byte(path)), List(Word(depth.String())))
	if err != nil {
		return nil, err
	}
	if len(items) != 1 || items[0].Kind != ListKind {
		return nil, malformed("invalid get-locks response")
	}
	locks := make(map[string]*svn.Lock, len(items[0].List))
	for _, item := range items[0].List {
		if item.Kind != ListKind {
			return nil, malformed("invalid lock entry")
		}
		lock, err := parseLock(item.List)
		if err != nil {
			return nil, err
		}
		locks[lock.Path] = lock
	}
	return locks, nil
}

func (session *Session) GetDeletedRev(ctx context.Context, path string, pegRevision, endRevision svn.Revnum) (svn.Revnum, error) {
	items, err := session.conn.command(ctx, "get-deleted-rev", String([]byte(path)), Number(uint64(pegRevision)), Number(uint64(endRevision)))
	if err != nil {
		return svn.InvalidRevnum, err
	}
	if len(items) != 1 || items[0].Kind != NumberKind {
		return svn.InvalidRevnum, malformed("invalid deleted revision response")
	}
	if items[0].Number == ^uint64(0) {
		return svn.InvalidRevnum, nil
	}
	return svn.Revnum(items[0].Number), nil
}

func (session *Session) GetMergeinfo(ctx context.Context, paths []string, revision svn.Revnum, inheritance mergeinfo.Inheritance, descendants bool) (map[string]mergeinfo.Mergeinfo, error) {
	pathItems := make([]Item, 0, len(paths))
	for _, path := range paths {
		pathItems = append(pathItems, String([]byte(path)))
	}
	items, err := session.conn.command(ctx, "get-mergeinfo", List(pathItems...), optionalRevision(revision), Word(inheritanceWord(inheritance)), Word(boolWord(descendants)))
	if err != nil {
		return nil, err
	}
	if len(items) != 1 || items[0].Kind != ListKind {
		return nil, malformed("invalid mergeinfo response")
	}
	result := make(map[string]mergeinfo.Mergeinfo, len(items[0].List))
	for _, item := range items[0].List {
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != StringKind || item.List[1].Kind != StringKind {
			return nil, malformed("invalid mergeinfo entry")
		}
		parsed, err := mergeinfo.Parse(string(item.List[1].String))
		if err != nil {
			return nil, err
		}
		result[string(item.List[0].String)] = parsed
	}
	return result, nil
}

func (session *Session) GetInheritedProps(ctx context.Context, path string, revision svn.Revnum) ([]ra.InheritedProps, error) {
	items, err := session.conn.command(ctx, "get-iprops", String([]byte(path)), optionalRevision(revision))
	if err != nil {
		return nil, err
	}
	if len(items) != 1 || items[0].Kind != ListKind {
		return nil, malformed("invalid inherited properties response")
	}
	result := make([]ra.InheritedProps, 0, len(items[0].List))
	for _, item := range items[0].List {
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != StringKind || item.List[1].Kind != ListKind {
			return nil, malformed("invalid inherited properties entry")
		}
		props, err := parseProps(item.List[1].List)
		if err != nil {
			return nil, err
		}
		result = append(result, ra.InheritedProps{Path: string(item.List[0].String), Props: props})
	}
	return result, nil
}

func (session *Session) List(ctx context.Context, path string, revision svn.Revnum, patterns []string, depth svn.Depth, fields svn.DirentFields, handler func(string, *svn.Dirent) error) error {
	if !session.capabilities[string(ra.CapabilityList)] {
		return fmt.Errorf("%w: server does not support list", svn.ErrUnsupportedFeature)
	}
	patternItems := make([]Item, 0, len(patterns))
	for _, pattern := range patterns {
		patternItems = append(patternItems, String([]byte(pattern)))
	}
	parameters := []Item{String([]byte(path)), optionalRevision(revision), Word(depth.String()), List(direntFieldItems(fields)...)}
	if len(patternItems) > 0 {
		parameters = append(parameters, List(patternItems...))
	}
	return session.conn.streamCommand(ctx, "list", func() error {
		for {
			item, err := session.conn.reader.Decode()
			if err != nil {
				return err
			}
			if item.Kind == WordKind && item.Word == "done" {
				_, err = session.conn.readResponse()
				return err
			}
			entryPath, entry, err := parseListDirent(item, fields)
			if err != nil {
				return err
			}
			if handler != nil {
				if err := handler(entryPath, entry); err != nil {
					return err
				}
			}
		}
	}, parameters...)
}

func (session *Session) Log(ctx context.Context, options ra.LogOptions, handler func(*svn.LogEntry) error) error {
	paths := make([]Item, 0, len(options.Paths))
	for _, path := range options.Paths {
		paths = append(paths, String([]byte(path)))
	}
	parameters := []Item{
		List(paths...), optionalRevision(options.Start), optionalRevision(options.End), Word(boolWord(options.DiscoverChangedPaths)),
		Word(boolWord(options.StrictNodeHistory)), Number(uint64(options.Limit)), Word(boolWord(options.IncludeMerged)),
	}
	if options.RevProps == nil {
		parameters = append(parameters, Word("all-revprops"), List())
	} else {
		revprops := make([]Item, 0, len(options.RevProps))
		for _, name := range options.RevProps {
			revprops = append(revprops, String([]byte(name)))
		}
		parameters = append(parameters, Word("revprops"), List(revprops...))
	}
	return session.conn.streamCommand(ctx, "log", func() error {
		for {
			item, err := session.conn.reader.Decode()
			if err != nil {
				return err
			}
			if item.Kind == WordKind && item.Word == "done" {
				_, err = session.conn.readResponse()
				return err
			}
			entry, err := parseLogEntry(item)
			if err != nil {
				return err
			}
			if options.RevProps == nil || containsString(options.RevProps, "svn:author") {
				entry.RevProps["svn:author"] = []byte(entry.Author)
			}
			if options.RevProps == nil || containsString(options.RevProps, "svn:date") {
				if !entry.Date.IsZero() {
					entry.RevProps["svn:date"] = []byte(svn.FormatDate(entry.Date))
				}
			}
			if options.RevProps == nil || containsString(options.RevProps, "svn:log") {
				entry.RevProps["svn:log"] = []byte(entry.Message)
			}
			if handler != nil {
				if err := handler(entry); err != nil {
					return err
				}
			}
		}
	}, parameters...)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (session *Session) GetLocations(ctx context.Context, path string, pegRevision svn.Revnum, revisions []svn.Revnum) (map[svn.Revnum]string, error) {
	revisionItems := make([]Item, 0, len(revisions))
	for _, revision := range revisions {
		revisionItems = append(revisionItems, Number(uint64(revision)))
	}
	result := make(map[svn.Revnum]string)
	err := session.conn.streamCommand(ctx, "get-locations", func() error {
		for {
			item, err := session.conn.reader.Decode()
			if err != nil {
				return err
			}
			if item.Kind == WordKind && item.Word == "done" {
				_, err = session.conn.readResponse()
				return err
			}
			if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != NumberKind || item.List[1].Kind != StringKind {
				return malformed("invalid location entry")
			}
			result[svn.Revnum(item.List[0].Number)] = string(item.List[1].String)
		}
	}, String([]byte(path)), Number(uint64(pegRevision)), List(revisionItems...))
	return result, err
}

func (session *Session) GetLocationSegments(ctx context.Context, path string, pegRevision, startRevision, endRevision svn.Revnum, handler func(ra.LocationSegment) error) error {
	return session.conn.streamCommand(ctx, "get-location-segments", func() error {
		for {
			item, err := session.conn.reader.Decode()
			if err != nil {
				return err
			}
			if item.Kind == WordKind && item.Word == "done" {
				_, err = session.conn.readResponse()
				return err
			}
			if item.Kind != ListKind || len(item.List) != 3 || item.List[0].Kind != NumberKind || item.List[1].Kind != NumberKind || item.List[2].Kind != ListKind {
				return malformed("invalid location segment")
			}
			segment := ra.LocationSegment{RangeStart: svn.Revnum(item.List[0].Number), RangeEnd: svn.Revnum(item.List[1].Number)}
			if len(item.List[2].List) == 1 && item.List[2].List[0].Kind == StringKind {
				segment.Path = string(item.List[2].List[0].String)
			} else if len(item.List[2].List) != 0 {
				return malformed("invalid location segment path")
			}
			if handler != nil {
				if err := handler(segment); err != nil {
					return err
				}
			}
		}
	}, String([]byte(path)), optionalRevision(pegRevision), optionalRevision(startRevision), optionalRevision(endRevision))
}

func (session *Session) GetFileRevs(ctx context.Context, path string, startRevision, endRevision svn.Revnum, includeMerged bool, handler ra.FileRevHandler) error {
	return session.conn.streamCommand(ctx, "get-file-revs", func() error {
		for {
			item, err := session.conn.reader.Decode()
			if err != nil {
				return err
			}
			if item.Kind == WordKind && item.Word == "done" {
				_, err = session.conn.readResponse()
				return err
			}
			revision, err := parseFileRevision(item)
			if err != nil {
				return err
			}
			firstChunk, err := session.conn.reader.Decode()
			if err != nil {
				return err
			}
			if firstChunk.Kind != StringKind {
				return malformed("file revision delta chunk is not a string")
			}
			if len(firstChunk.String) == 0 {
				if handler != nil {
					if err := handler(ctx, revision); err != nil {
						return err
					}
				}
				continue
			}
			if err := session.streamFileRevisionDelta(ctx, revision, firstChunk.String, handler); err != nil {
				return err
			}
		}
	}, String([]byte(path)), optionalRevision(startRevision), optionalRevision(endRevision), Word(boolWord(includeMerged)))
}

func (session *Session) streamFileRevisionDelta(ctx context.Context, revision ra.FileRevision, first []byte, handler ra.FileRevHandler) error {
	if handler == nil {
		for {
			chunk, err := session.conn.reader.Decode()
			if err != nil {
				return err
			}
			if chunk.Kind != StringKind {
				return malformed("file revision delta chunk is not a string")
			}
			if len(chunk.String) == 0 {
				return nil
			}
		}
	}
	pipeReader, pipeWriter := io.Pipe()
	result := make(chan error, 1)
	go func() {
		windows, err := delta.NewSvndiffReader(pipeReader)
		if err == nil {
			revision.Delta = windows
			err = handler(ctx, revision)
		}
		_ = pipeReader.CloseWithError(err)
		result <- err
	}()
	writerOpen := true
	if _, err := pipeWriter.Write(first); err != nil {
		writerOpen = false
		_ = pipeWriter.Close()
	}
	for {
		chunk, err := session.conn.reader.Decode()
		if err != nil {
			if writerOpen {
				_ = pipeWriter.CloseWithError(err)
			}
			return errors.Join(err, <-result)
		}
		if chunk.Kind != StringKind {
			err = malformed("file revision delta chunk is not a string")
			if writerOpen {
				_ = pipeWriter.CloseWithError(err)
			}
			return errors.Join(err, <-result)
		}
		if len(chunk.String) == 0 {
			if writerOpen {
				_ = pipeWriter.Close()
			}
			return <-result
		}
		if writerOpen {
			if _, err := pipeWriter.Write(chunk.String); err != nil {
				writerOpen = false
				_ = pipeWriter.Close()
			}
		}
	}
}

func parseListDirent(item Item, fields svn.DirentFields) (string, *svn.Dirent, error) {
	if item.Kind != ListKind || len(item.List) < 2 || item.List[0].Kind != StringKind || item.List[1].Kind != WordKind {
		return "", nil, malformed("invalid list entry")
	}
	path := string(item.List[0].String)
	kind, err := svn.ParseNodeKind(item.List[1].Word)
	if err != nil {
		return "", nil, err
	}
	entry := &svn.Dirent{Path: path, Kind: kind, Size: -1, CreatedRev: svn.InvalidRevnum}
	index := 2
	for _, field := range []svn.DirentFields{svn.DirentSize, svn.DirentHasProps, svn.DirentCreatedRev, svn.DirentTime, svn.DirentLastAuthor} {
		if fields&field == 0 {
			continue
		}
		if index >= len(item.List) || item.List[index].Kind != ListKind || len(item.List[index].List) > 1 {
			return "", nil, malformed("invalid optional list field")
		}
		optional := item.List[index].List
		index++
		if len(optional) == 0 {
			continue
		}
		switch field {
		case svn.DirentSize:
			if optional[0].Kind != NumberKind {
				return "", nil, malformed("invalid list entry size")
			}
			entry.Size = int64(optional[0].Number)
		case svn.DirentHasProps:
			if optional[0].Kind != WordKind || (optional[0].Word != "true" && optional[0].Word != "false") {
				return "", nil, malformed("invalid list entry property flag")
			}
			entry.HasProps = optional[0].Word == "true"
		case svn.DirentCreatedRev:
			if optional[0].Kind != NumberKind {
				return "", nil, malformed("invalid list entry revision")
			}
			entry.CreatedRev = svn.Revnum(optional[0].Number)
		case svn.DirentTime:
			if optional[0].Kind != StringKind {
				return "", nil, malformed("invalid list entry date")
			}
			entry.Time, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(string(optional[0].String)))
			if err != nil {
				return "", nil, malformedCause(err, "invalid list entry date")
			}
		case svn.DirentLastAuthor:
			if optional[0].Kind != StringKind {
				return "", nil, malformed("invalid list entry author")
			}
			entry.LastAuthor = string(optional[0].String)
		}
	}
	return path, entry, nil
}

func parseLogEntry(item Item) (*svn.LogEntry, error) {
	if item.Kind != ListKind || len(item.List) < 5 || item.List[0].Kind != ListKind || item.List[1].Kind != NumberKind {
		return nil, malformed("invalid log entry")
	}
	entry := &svn.LogEntry{Revision: svn.Revnum(item.List[1].Number), RevProps: make(svn.Props)}
	for index, target := range []*string{&entry.Author, nil, &entry.Message} {
		optional := item.List[index+2]
		if optional.Kind != ListKind || len(optional.List) > 1 {
			return nil, malformed("invalid log revision property")
		}
		if len(optional.List) == 0 {
			continue
		}
		if optional.List[0].Kind != StringKind {
			return nil, malformed("invalid log revision property")
		}
		value := string(optional.List[0].String)
		if index == 1 {
			parsedDate, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
			if err != nil {
				return nil, malformedCause(err, "invalid log date")
			}
			entry.Date = parsedDate
		} else {
			*target = value
		}
	}
	for _, changed := range item.List[0].List {
		parsed, err := parseChangedPath(changed)
		if err != nil {
			return nil, err
		}
		entry.ChangedPaths = append(entry.ChangedPaths, parsed)
	}
	if len(item.List) > 5 && item.List[5].Kind == WordKind {
		entry.HasChildren = item.List[5].Word == "true"
	}
	if len(item.List) > 6 && item.List[6].Kind == WordKind && item.List[6].Word == "true" {
		entry.Revision = svn.InvalidRevnum
	}
	if len(item.List) > 8 && item.List[8].Kind == ListKind {
		props, err := parseProps(item.List[8].List)
		if err != nil {
			return nil, err
		}
		entry.RevProps = props
	}
	if len(item.List) > 9 && item.List[9].Kind == WordKind {
		entry.SubtractiveMerge = item.List[9].Word == "true"
	}
	return entry, nil
}

func parseChangedPath(item Item) (svn.ChangedPath, error) {
	if item.Kind != ListKind || len(item.List) < 2 || item.List[0].Kind != StringKind || item.List[1].Kind != WordKind || len(item.List[1].Word) != 1 {
		return svn.ChangedPath{}, malformed("invalid changed path")
	}
	changed := svn.ChangedPath{Path: string(item.List[0].String), Action: svn.LogChangeAction(item.List[1].Word[0]), CopyfromRev: svn.InvalidRevnum}
	if len(item.List) > 2 && item.List[2].Kind == ListKind && len(item.List[2].List) == 2 {
		changed.CopyfromPath = string(item.List[2].List[0].String)
		changed.CopyfromRev = svn.Revnum(item.List[2].List[1].Number)
	}
	if len(item.List) > 3 && item.List[3].Kind == ListKind {
		flags := item.List[3].List
		if len(flags) > 0 {
			var nodeKind string
			switch flags[0].Kind {
			case StringKind:
				nodeKind = string(flags[0].String)
			case WordKind:
				nodeKind = flags[0].Word
			default:
				return svn.ChangedPath{}, malformed("invalid changed path node kind")
			}
			changed.NodeKind, _ = svn.ParseNodeKind(nodeKind)
		}
		if len(flags) > 1 {
			changed.TextModified = parseTristate(flags[1])
		}
		if len(flags) > 2 {
			changed.PropsModified = parseTristate(flags[2])
		}
	}
	return changed, nil
}

func parseTristate(item Item) svn.Tristate {
	if item.Kind != WordKind {
		return svn.TristateUnknown
	}
	if item.Word == "true" {
		return svn.TristateTrue
	}
	if item.Word == "false" {
		return svn.TristateFalse
	}
	return svn.TristateUnknown
}

func parseFileRevision(item Item) (ra.FileRevision, error) {
	if item.Kind != ListKind || len(item.List) < 4 || item.List[0].Kind != StringKind || item.List[1].Kind != NumberKind || item.List[2].Kind != ListKind || item.List[3].Kind != ListKind {
		return ra.FileRevision{}, malformed("invalid file revision")
	}
	revision := ra.FileRevision{Path: string(item.List[0].String), Revision: svn.Revnum(item.List[1].Number)}
	var err error
	revision.RevProps, err = parseProps(item.List[2].List)
	if err != nil {
		return ra.FileRevision{}, err
	}
	revision.PropDiffs, err = parsePropDiffs(item.List[3].List)
	if err != nil {
		return ra.FileRevision{}, err
	}
	if len(item.List) > 4 && item.List[4].Kind == WordKind {
		revision.Merged = item.List[4].Word == "true"
	}
	return revision, nil
}

func parsePropDiffs(items []Item) (svn.Props, error) {
	props := make(svn.Props, len(items))
	for _, item := range items {
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != StringKind || item.List[1].Kind != ListKind || len(item.List[1].List) > 1 {
			return nil, malformed("invalid property delta")
		}
		var value []byte
		if len(item.List[1].List) == 1 {
			value = append([]byte{}, item.List[1].List[0].String...)
		}
		props[string(item.List[0].String)] = value
	}
	return props, nil
}

func boolWord(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func inheritanceWord(inheritance mergeinfo.Inheritance) string {
	switch inheritance {
	case mergeinfo.InheritanceExplicit:
		return "explicit"
	case mergeinfo.InheritanceInherited:
		return "inherited"
	default:
		return "nearest-ancestor"
	}
}

func direntFieldItems(fields svn.DirentFields) []Item {
	values := []struct {
		field svn.DirentFields
		word  string
	}{{svn.DirentKind, "kind"}, {svn.DirentSize, "size"}, {svn.DirentHasProps, "has-props"}, {svn.DirentCreatedRev, "created-rev"}, {svn.DirentTime, "time"}, {svn.DirentLastAuthor, "last-author"}}
	items := make([]Item, 0, len(values))
	for _, value := range values {
		if fields&value.field != 0 {
			items = append(items, Word(value.word))
		}
	}
	return items
}

func equalHex(value string, digest []byte) bool {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(digest) {
		return false
	}
	var difference byte
	for index := range decoded {
		difference |= decoded[index] ^ digest[index]
	}
	return difference == 0
}

func parseOptional(items []Item) ([]Item, error) {
	if len(items) != 1 || items[0].Kind != ListKind {
		return nil, malformed("invalid optional response")
	}
	return items[0].List, nil
}

func optionalRevision(revision svn.Revnum) Item {
	if revision == svn.InvalidRevnum {
		return List()
	}
	return List(Number(uint64(revision)))
}

func parseProps(items []Item) (svn.Props, error) {
	props := make(svn.Props, len(items))
	for _, item := range items {
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != StringKind || item.List[1].Kind != StringKind {
			return nil, malformed("invalid property list")
		}
		props[string(item.List[0].String)] = append([]byte(nil), item.List[1].String...)
	}
	return props, nil
}

func parseDirent(items []Item, path string) (*svn.Dirent, error) {
	if len(items) < 4 || items[0].Kind != WordKind || items[1].Kind != NumberKind || items[2].Kind != WordKind || items[3].Kind != NumberKind {
		return nil, malformed("invalid directory entry %#v", items)
	}
	kind, err := svn.ParseNodeKind(items[0].Word)
	if err != nil {
		return nil, malformedCause(err, "invalid directory entry kind")
	}
	dirent := &svn.Dirent{Path: path, Kind: kind, Size: int64(items[1].Number), HasProps: items[2].Word == "true", CreatedRev: svn.Revnum(items[3].Number)}
	if items[2].Word != "true" && items[2].Word != "false" {
		return nil, malformed("invalid directory entry property flag")
	}
	if len(items) > 4 {
		if items[4].Kind != ListKind || len(items[4].List) > 1 {
			return nil, malformed("invalid directory entry date")
		}
		if len(items[4].List) == 1 {
			dirent.Time, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(string(items[4].List[0].String)))
			if err != nil {
				return nil, malformedCause(err, "invalid directory entry date")
			}
		}
	}
	if len(items) > 5 && len(items[5].List) == 1 {
		dirent.LastAuthor = string(items[5].List[0].String)
	}
	return dirent, nil
}

func parseLock(items []Item) (*svn.Lock, error) {
	if len(items) < 6 || items[0].Kind != StringKind || items[1].Kind != StringKind || items[2].Kind != StringKind {
		return nil, malformed("invalid lock description")
	}
	lock := &svn.Lock{Path: string(items[0].String), Token: string(items[1].String), Owner: string(items[2].String)}
	var err error
	if len(items[3].List) == 1 {
		lock.Comment = string(items[3].List[0].String)
	}
	if len(items[4].List) != 1 {
		return nil, malformed("lock has no creation date")
	}
	lock.CreationDate, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(string(items[4].List[0].String)))
	if err != nil {
		return nil, malformedCause(err, "invalid lock creation date")
	}
	if len(items[5].List) == 1 {
		lock.ExpirationDate, err = time.Parse(time.RFC3339Nano, strings.TrimSpace(string(items[5].List[0].String)))
		if err != nil {
			return nil, malformedCause(err, "invalid lock expiration date")
		}
	}
	return lock, nil
}

func notImplemented(operation string) error {
	return fmt.Errorf("%w: ra_svn %s", svn.ErrRANotImplemented, operation)
}

func (session *Session) ChangeRevProp(context.Context, svn.Revnum, string, []byte, []byte, bool) error {
	return notImplemented("change revision property")
}
func (session *Session) DoUpdate(ctx context.Context, revision svn.Revnum, target string, depth svn.Depth, sendCopyfrom, ignoreAncestry bool, editor delta.Editor) (ra.Reporter, error) {
	return session.startReport(ctx, "update", editor, optionalRevision(revision), String([]byte(target)), Word(boolWord(reportRecurse(depth))), Word(depth.String()), Word(boolWord(sendCopyfrom)), Word(boolWord(ignoreAncestry)))
}
func (session *Session) DoSwitch(ctx context.Context, revision svn.Revnum, target string, depth svn.Depth, switchURL string, sendCopyfrom, ignoreAncestry bool, editor delta.Editor) (ra.Reporter, error) {
	return session.startReport(ctx, "switch", editor, optionalRevision(revision), String([]byte(target)), Word(boolWord(reportRecurse(depth))), String([]byte(switchURL)), Word(depth.String()), Word(boolWord(sendCopyfrom)), Word(boolWord(ignoreAncestry)))
}
func (session *Session) DoStatus(ctx context.Context, target string, revision svn.Revnum, depth svn.Depth, editor delta.Editor) (ra.Reporter, error) {
	return session.startReport(ctx, "status", editor, String([]byte(target)), Word(boolWord(reportRecurse(depth))), optionalRevision(revision), Word(depth.String()))
}
func (session *Session) DoDiff(ctx context.Context, revision svn.Revnum, target string, depth svn.Depth, ignoreAncestry, textDeltas bool, versusURL string, editor delta.Editor) (ra.Reporter, error) {
	return session.startReport(ctx, "diff", editor, optionalRevision(revision), String([]byte(target)), Word(boolWord(reportRecurse(depth))), Word(boolWord(ignoreAncestry)), String([]byte(versusURL)), Word(boolWord(textDeltas)), Word(depth.String()))
}
func (session *Session) GetCommitEditor(context.Context, svn.Props, map[string]string, bool, func(*ra.CommitInfo) error) (delta.Editor, error) {
	return nil, notImplemented("commit")
}
func (session *Session) Lock(context.Context, map[string]svn.Revnum, string, bool, ra.LockCallback) error {
	return notImplemented("lock")
}
func (session *Session) Unlock(context.Context, map[string]string, bool, ra.LockCallback) error {
	return notImplemented("unlock")
}
func (session *Session) Replay(ctx context.Context, revision, lowWaterMark svn.Revnum, sendDeltas bool, editor delta.Editor) error {
	return session.conn.streamCommand(ctx, "replay", func() error {
		if err := session.conn.driveEditor(ctx, editor, true); err != nil {
			return err
		}
		_, err := session.conn.readResponse()
		return err
	}, Number(uint64(revision)), Number(uint64(lowWaterMark)), Word(boolWord(sendDeltas)))
}
func (session *Session) ReplayRange(ctx context.Context, start, end, lowWaterMark svn.Revnum, sendDeltas bool, startRevision func(svn.Revnum, svn.Props) (delta.Editor, error), finishRevision func(svn.Revnum, svn.Props, delta.Editor) error) error {
	if !start.IsValid() || end < start || !lowWaterMark.IsValid() || startRevision == nil || finishRevision == nil {
		return fmt.Errorf("%w: invalid replay range", svn.ErrIncorrectParams)
	}
	return session.conn.streamCommand(ctx, "replay-range", func() error {
		for revision := start; ; revision++ {
			item, err := session.conn.reader.Decode()
			if err != nil {
				return err
			}
			if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != WordKind || item.List[0].Word != "revprops" || item.List[1].Kind != ListKind {
				return malformed("invalid replay-range revprops")
			}
			props, err := parseProps(item.List[1].List)
			if err != nil {
				return err
			}
			editor, err := startRevision(revision, props)
			if err != nil {
				return err
			}
			if err := session.conn.driveEditor(ctx, editor, true); err != nil {
				return err
			}
			if err := finishRevision(revision, props, editor); err != nil {
				return err
			}
			if revision == end {
				break
			}
		}
		_, err := session.conn.readResponse()
		return err
	}, Number(uint64(start)), Number(uint64(end)), Number(uint64(lowWaterMark)), Word(boolWord(sendDeltas)))
}
func (session *Session) Close() error { return session.conn.Close() }

func reportRecurse(depth svn.Depth) bool {
	return depth == svn.DepthUnknown || depth == svn.DepthInfinity
}
