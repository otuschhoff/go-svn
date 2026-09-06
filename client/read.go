package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
	"github.com/otuschhoff/go-svn/wc"
)

type CatOptions struct {
	InfoOptions
	IgnoreKeywords bool
	EOL            string
}

func (client *Client) Cat(ctx context.Context, targetValue string, destination io.Writer, options CatOptions) error {
	if destination == nil {
		return fmt.Errorf("%w: nil cat destination", svn.ErrIncorrectParams)
	}
	target, err := client.resolveTarget(ctx, targetValue, options.InfoOptions)
	if err != nil {
		return err
	}
	defer target.close()
	if target.workingInfo != nil && options.Revision.Kind == svn.RevisionWorking {
		input, err := os.Open(target.workingInfo.Path)
		if err != nil {
			return err
		}
		defer input.Close()
		_, err = io.Copy(destination, input)
		return err
	}
	var contents bytes.Buffer
	_, properties, err := target.session.GetFile(ctx, target.path, target.revision, &contents, true)
	if err != nil {
		return err
	}
	if options.IgnoreKeywords && options.EOL == "" {
		_, err = io.Copy(destination, &contents)
		return err
	}
	keywords := props.KeywordValues(nil)
	if !options.IgnoreKeywords {
		entry, err := target.session.Stat(ctx, target.path, target.revision)
		if err != nil {
			return err
		}
		if entry == nil {
			return fmt.Errorf("%w: %s", svn.ErrFSNotFound, targetValue)
		}
		keywords = props.ParseKeywords(string(properties[props.Keywords]), props.KeywordContext{
			Author: entry.LastAuthor, Basename: path.Base(target.url), Date: entry.Time, Revision: entry.CreatedRev,
			Path:    strings.TrimPrefix(strings.TrimPrefix(target.url, target.repositoryRoot), "/"),
			RootURL: target.repositoryRoot, URL: target.url,
		})
	}
	eol := options.EOL
	if eol == "" {
		eol = string(properties[props.EOLStyle])
	}
	return props.Translate(&contents, destination, eol, keywords, true, true)
}

type ListOptions struct {
	InfoOptions
	Depth    svn.Depth
	Patterns []string
	Fields   svn.DirentFields
}

type ListEntry struct {
	Path  string
	URL   string
	Entry svn.Dirent
}

func (client *Client) List(ctx context.Context, targetValue string, options ListOptions, callback func(ListEntry) error) error {
	if callback == nil {
		return fmt.Errorf("%w: nil list callback", svn.ErrIncorrectParams)
	}
	target, err := client.resolveTarget(ctx, targetValue, options.InfoOptions)
	if err != nil {
		return err
	}
	defer target.close()
	depth := options.Depth
	if depth == svn.DepthUnknown {
		depth = svn.DepthImmediates
	}
	fields := options.Fields
	if fields == 0 {
		fields = svn.DirentAll
	}
	return target.session.List(ctx, target.path, target.revision, options.Patterns, depth, fields, func(name string, entry *svn.Dirent) error {
		if entry == nil || !matchesListPatterns(name, options.Patterns) {
			return nil
		}
		copy := *entry
		return callback(ListEntry{Path: name, URL: joinRepositoryURL(target.url, name), Entry: copy})
	})
}

func matchesListPatterns(name string, patterns []string) bool {
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

type LogOptions struct {
	InfoOptions
	Ranges               []svn.RevisionRange
	Limit                int
	DiscoverChangedPaths bool
	StopOnCopy           bool
	IncludeMerged        bool
	RevisionProperties   []string
	Search               []string
	DiffOutput           io.Writer
	DiffOptions          DiffOptions
}

func (client *Client) Log(ctx context.Context, targetValue string, options LogOptions, callback func(*svn.LogEntry) error) error {
	if callback == nil {
		return fmt.Errorf("%w: nil log callback", svn.ErrIncorrectParams)
	}
	target, err := client.resolveTarget(ctx, targetValue, options.InfoOptions)
	if err != nil {
		return err
	}
	defer target.close()
	ranges := options.Ranges
	if len(ranges) == 0 {
		ranges = []svn.RevisionRange{{Start: numericRevision(target.revision), End: numericRevision(0)}}
	}
	delivered := 0
	for _, revisionRange := range ranges {
		start, err := resolveRARevision(ctx, target.session, revisionRange.Start)
		if err != nil {
			return err
		}
		end, err := resolveRARevision(ctx, target.session, revisionRange.End)
		if err != nil {
			return err
		}
		err = target.session.Log(ctx, ra.LogOptions{
			Paths: []string{target.path}, Start: start, End: end, DiscoverChangedPaths: options.DiscoverChangedPaths,
			StrictNodeHistory: options.StopOnCopy, IncludeMerged: options.IncludeMerged, RevProps: options.RevisionProperties,
		}, func(entry *svn.LogEntry) error {
			if !matchesLogSearch(entry, options.Search) {
				return nil
			}
			if options.Limit > 0 && delivered >= options.Limit {
				return nil
			}
			delivered++
			if err := callback(entry); err != nil {
				return err
			}
			if options.DiffOutput != nil && entry.Revision > 0 {
				left := DiffTarget{Target: targetValue, Revision: numericRevision(entry.Revision - 1)}
				right := DiffTarget{Target: targetValue, Revision: numericRevision(entry.Revision)}
				if err := client.Diff(ctx, left, right, options.DiffOutput, options.DiffOptions); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		if options.Limit > 0 && delivered >= options.Limit {
			break
		}
	}
	return nil
}

func matchesLogSearch(entry *svn.LogEntry, search []string) bool {
	if len(search) == 0 {
		return true
	}
	haystack := entry.Author + "\n" + entry.Message
	for _, change := range entry.ChangedPaths {
		haystack += "\n" + change.Path
	}
	for _, term := range search {
		if strings.Contains(strings.ToLower(haystack), strings.ToLower(term)) {
			return true
		}
	}
	return false
}

type PropertyOptions struct {
	InfoOptions
	Depth svn.Depth
	Base  bool
}

func (client *Client) PropList(ctx context.Context, targetValue string, options PropertyOptions) (svn.Props, error) {
	target, err := client.resolveTarget(ctx, targetValue, options.InfoOptions)
	if err != nil {
		return nil, err
	}
	defer target.close()
	if target.workingCopy != nil && (options.Revision.Kind == svn.RevisionUnspecified || options.Revision.Kind == svn.RevisionWorking || options.Revision.Kind == svn.RevisionBase) {
		layer := wc.PropertiesWorking
		if options.Base || options.Revision.Kind == svn.RevisionBase {
			layer = wc.PropertiesBase
		}
		return target.workingCopy.PropList(ctx, target.workingInfo.Path, layer)
	}
	kind, err := target.session.CheckPath(ctx, target.path, target.revision)
	if err != nil {
		return nil, err
	}
	if kind == svn.NodeFile {
		_, properties, err := target.session.GetFile(ctx, target.path, target.revision, nil, true)
		return properties, err
	}
	if kind == svn.NodeDir {
		_, _, properties, err := target.session.GetDir(ctx, target.path, target.revision, 0)
		return properties, err
	}
	return nil, fmt.Errorf("%w: %s", svn.ErrFSNotFound, targetValue)
}

func (client *Client) PropGet(ctx context.Context, targetValue, name string, options PropertyOptions) ([]byte, bool, error) {
	properties, err := client.PropList(ctx, targetValue, options)
	if err != nil {
		return nil, false, err
	}
	value, ok := properties[name]
	return append([]byte(nil), value...), ok, nil
}

func (client *Client) RevisionProperties(ctx context.Context, targetValue string, revision svn.Revision) (svn.Props, error) {
	target, err := client.resolveTarget(ctx, targetValue, InfoOptions{Revision: revision})
	if err != nil {
		return nil, err
	}
	defer target.close()
	return target.session.RevProps(ctx, target.revision)
}

func (client *Client) SetRevisionProperty(ctx context.Context, targetValue string, revision svn.Revision, name string, value, oldValue []byte, force bool) error {
	target, err := client.resolveTarget(ctx, targetValue, InfoOptions{Revision: revision})
	if err != nil {
		return err
	}
	defer target.close()
	if err := target.session.ChangeRevProp(ctx, target.revision, name, value, oldValue, force); err != nil {
		return err
	}
	action := notify.ActionRevisionPropertySet
	if value == nil {
		action = notify.ActionRevisionPropertyDeleted
	}
	client.notify(notify.Notify{Action: action, Path: targetValue, Revision: target.revision})
	return nil
}

func (client *Client) Mergeinfo(ctx context.Context, targetValue string, revision svn.Revision, inheritance mergeinfo.Inheritance, includeDescendants bool) (map[string]mergeinfo.Mergeinfo, error) {
	target, err := client.resolveTarget(ctx, targetValue, InfoOptions{Revision: revision})
	if err != nil {
		return nil, err
	}
	defer target.close()
	return target.session.GetMergeinfo(ctx, []string{target.path}, target.revision, inheritance, includeDescendants)
}

func (client *Client) MergeinfoRevisions(ctx context.Context, sourceValue, targetValue string, revision svn.Revision, showMerged bool, start, end svn.Revnum) ([]svn.Revnum, error) {
	source, err := client.resolveTarget(ctx, sourceValue, InfoOptions{Revision: revision})
	if err != nil {
		return nil, err
	}
	defer source.close()
	target, err := client.resolveTarget(ctx, targetValue, InfoOptions{Revision: revision})
	if err != nil {
		return nil, err
	}
	defer target.close()
	sourceUUID, err := source.session.UUID(ctx)
	if err != nil {
		return nil, err
	}
	targetUUID, err := target.session.UUID(ctx)
	if err != nil {
		return nil, err
	}
	if sourceUUID != targetUUID {
		return nil, fmt.Errorf("%w: mergeinfo sources belong to different repositories", svn.ErrClientUnrelatedResources)
	}
	if !end.IsValid() {
		end = source.revision
	}
	if start < 1 {
		start = 1
	}
	changed := make(map[svn.Revnum]bool)
	if err := source.session.Log(ctx, ra.LogOptions{Paths: []string{source.path}, Start: end, End: start, StrictNodeHistory: true}, func(entry *svn.LogEntry) error {
		changed[entry.Revision] = true
		return nil
	}); err != nil {
		return nil, err
	}
	var catalog map[string]mergeinfo.Mergeinfo
	if target.workingInfo != nil {
		working, err := client.WorkingCopyMergeinfo(ctx, target.workingInfo.Path, mergeinfo.InheritanceInherited)
		if err != nil {
			return nil, err
		}
		catalog = map[string]mergeinfo.Mergeinfo{target.path: working.Mergeinfo}
	} else {
		catalog, err = target.session.GetMergeinfo(ctx, []string{target.path}, target.revision, mergeinfo.InheritanceInherited, false)
		if err != nil {
			return nil, err
		}
	}
	sourcePath := "/" + strings.Trim(strings.TrimPrefix(source.url, source.repositoryRoot), "/")
	merged := make(map[svn.Revnum]bool)
	for _, info := range catalog {
		for mergeSource, ranges := range info {
			if "/"+strings.Trim(mergeSource, "/") != sourcePath {
				continue
			}
			for _, mergedRevision := range mergeinfo.ToRevs(ranges) {
				merged[mergedRevision] = true
			}
		}
	}
	result := make([]svn.Revnum, 0)
	for candidate := start; candidate <= end; candidate++ {
		if changed[candidate] && merged[candidate] == showMerged {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func (client *Client) MergeinfoLog(ctx context.Context, sourceValue, targetValue string, revision svn.Revision, showMerged bool, start, end svn.Revnum, callback func(*svn.LogEntry) error) error {
	if callback == nil {
		return fmt.Errorf("%w: nil mergeinfo log callback", svn.ErrIncorrectParams)
	}
	revisions, err := client.MergeinfoRevisions(ctx, sourceValue, targetValue, revision, showMerged, start, end)
	if err != nil {
		return err
	}
	for index := len(revisions) - 1; index >= 0; index-- {
		item := numericRevision(revisions[index])
		if err := client.Log(ctx, sourceValue, LogOptions{InfoOptions: InfoOptions{Revision: item}, Ranges: []svn.RevisionRange{{Start: item, End: item}}, DiscoverChangedPaths: true}, callback); err != nil {
			return err
		}
	}
	return nil
}

func localListPath(root, name string) string {
	return filepath.Join(root, filepath.FromSlash(name))
}
