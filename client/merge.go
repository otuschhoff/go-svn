package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	internaldiff3 "github.com/otuschhoff/go-svn/internal/diff3"
	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
	"github.com/otuschhoff/go-svn/wc"
)

type MergeOptions struct {
	Ranges              []svn.RevisionRange
	Depth               svn.Depth
	RecordOnly          bool
	IgnoreAncestry      bool
	Force               bool
	DryRun              bool
	AllowMixedRevisions bool
	Accept              wc.ConflictChoice
	Notify              notify.Func
}

func (client *Client) MergeTwoSources(ctx context.Context, left, right DiffTarget, targetPath string, options MergeOptions) error {
	options.Notify = client.combineNotify(options.Notify)
	database, err := wc.Open(ctx, targetPath, wc.Options{Writable: true})
	if err != nil {
		return err
	}
	defer database.Close()
	if !options.AllowMixedRevisions {
		if err := requireSingleRevision(ctx, database, targetPath, options.Depth); err != nil {
			return err
		}
	}
	leftNodes, leftRevision, err := client.diffSnapshot(ctx, left, options.Depth)
	if err != nil {
		return err
	}
	rightNodes, rightRevision, err := client.diffSnapshot(ctx, right, options.Depth)
	if err != nil {
		return err
	}
	if !options.IgnoreAncestry {
		leftTarget, err := client.resolveTarget(ctx, left.Target, InfoOptions{Revision: left.Revision})
		if err != nil {
			return err
		}
		rightTarget, err := client.resolveTarget(ctx, right.Target, InfoOptions{Revision: right.Revision})
		if err != nil {
			leftTarget.close()
			return err
		}
		common, ancestryErr := youngestCommonLocation(ctx, leftTarget, rightTarget)
		leftTarget.close()
		rightTarget.close()
		if ancestryErr != nil {
			return ancestryErr
		}
		if !common.IsValid() {
			return fmt.Errorf("%w: merge sources have no common ancestry", svn.ErrClientUnrelatedResources)
		}
	}
	mergeNotify(options, notify.Notify{Action: notify.ActionMergeBegin, Path: targetPath, Revision: rightRevision})
	if !options.RecordOnly && !options.DryRun {
		if err := applyMergeSnapshot(ctx, database, targetPath, leftNodes, rightNodes, leftRevision, rightRevision, options); err != nil {
			return err
		}
	}
	if options.DryRun {
		return nil
	}
	return client.recordMergeinfo(ctx, database, targetPath, right.Target, leftRevision, rightRevision)
}

func (client *Client) Merge(ctx context.Context, source, targetPath string, options MergeOptions) error {
	options.Notify = client.combineNotify(options.Notify)
	if len(options.Ranges) == 0 {
		var err error
		source, options.Ranges, err = client.automaticMergeRanges(ctx, source, targetPath)
		if err != nil {
			return err
		}
		if len(options.Ranges) == 0 {
			return nil
		}
	}
	database, err := wc.Open(ctx, targetPath, wc.Options{Writable: true})
	if err != nil {
		return err
	}
	defer database.Close()
	if !options.AllowMixedRevisions {
		if err := requireSingleRevision(ctx, database, targetPath, options.Depth); err != nil {
			return err
		}
	}
	for _, revisionRange := range options.Ranges {
		left, leftRevision, err := client.diffSnapshot(ctx, DiffTarget{Target: source, Revision: revisionRange.Start}, options.Depth)
		if err != nil {
			return err
		}
		right, rightRevision, err := client.diffSnapshot(ctx, DiffTarget{Target: source, Revision: revisionRange.End}, options.Depth)
		if err != nil {
			return err
		}
		mergeNotify(options, notify.Notify{Action: notify.ActionMergeBegin, Path: targetPath, Revision: rightRevision})
		if !options.RecordOnly && !options.DryRun {
			if err := applyMergeSnapshot(ctx, database, targetPath, left, right, leftRevision, rightRevision, options); err != nil {
				return err
			}
		}
		if !options.DryRun {
			if err := client.recordMergeinfo(ctx, database, targetPath, source, leftRevision, rightRevision); err != nil {
				return err
			}
		}
	}
	return nil
}

func (client *Client) automaticMergeRanges(ctx context.Context, source, targetPath string) (string, []svn.RevisionRange, error) {
	sourceTarget, err := client.resolveTarget(ctx, source, InfoOptions{Revision: svn.Revision{Kind: svn.RevisionHead}})
	if err != nil {
		return source, nil, err
	}
	defer sourceTarget.close()
	target, err := client.resolveTarget(ctx, targetPath, InfoOptions{Revision: svn.Revision{Kind: svn.RevisionHead}})
	if err != nil {
		return source, nil, err
	}
	defer target.close()
	sourceUUID, err := sourceTarget.session.UUID(ctx)
	if err != nil {
		return source, nil, err
	}
	targetUUID, err := target.session.UUID(ctx)
	if err != nil {
		return source, nil, err
	}
	if sourceUUID != targetUUID {
		return source, nil, fmt.Errorf("%w: merge sources belong to different repositories", svn.ErrClientUnrelatedResources)
	}
	common, err := youngestCommonLocation(ctx, sourceTarget, target)
	if err != nil {
		return source, nil, err
	}
	if !common.IsValid() {
		return source, nil, fmt.Errorf("%w: no common ancestor", svn.ErrClientUnrelatedResources)
	}
	revisions, err := client.MergeinfoRevisions(ctx, source, targetPath, svn.Revision{Kind: svn.RevisionHead}, false, common+1, sourceTarget.revision)
	if err != nil {
		return source, nil, err
	}
	ranges := make([]svn.RevisionRange, 0, len(revisions))
	for _, revision := range revisions {
		ranges = append(ranges, svn.RevisionRange{Start: numericRevision(revision - 1), End: numericRevision(revision)})
	}
	return source + "@" + revisionString(sourceTarget.revision), ranges, nil
}

func youngestCommonLocation(ctx context.Context, source, target *resolvedTarget) (svn.Revnum, error) {
	segments := func(value *resolvedTarget) ([]ra.LocationSegment, error) {
		var result []ra.LocationSegment
		err := value.session.GetLocationSegments(ctx, value.path, value.revision, value.revision, 0, func(segment ra.LocationSegment) error {
			result = append(result, segment)
			return nil
		})
		return result, err
	}
	sourceSegments, err := segments(source)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	targetSegments, err := segments(target)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	common := svn.InvalidRevnum
	for _, left := range sourceSegments {
		for _, right := range targetSegments {
			if strings.Trim(left.Path, "/") != strings.Trim(right.Path, "/") {
				continue
			}
			start := left.RangeStart
			if right.RangeStart > start {
				start = right.RangeStart
			}
			end := left.RangeEnd
			if right.RangeEnd < end {
				end = right.RangeEnd
			}
			if start <= end && end > common {
				common = end
			}
		}
	}
	return common, nil
}

func requireSingleRevision(ctx context.Context, database *wc.Database, targetPath string, depth svn.Depth) error {
	var revision svn.Revnum = svn.InvalidRevnum
	return database.Status(ctx, targetPath, wc.StatusOptions{Depth: depth}, func(status *wc.Status) error {
		if !status.Revision.IsValid() {
			return nil
		}
		if !revision.IsValid() {
			revision = status.Revision
		} else if revision != status.Revision {
			return fmt.Errorf("%w: mixed-revision working copy", svn.ErrWCMixedRevisions)
		}
		return nil
	})
}

func applyMergeSnapshot(ctx context.Context, database *wc.Database, targetPath string, left, right map[string]diffNode, leftRevision, rightRevision svn.Revnum, options MergeOptions) error {
	var deletions, directories, files, common []string
	for name, before := range left {
		after, exists := right[name]
		if !exists {
			if name != "." {
				deletions = append(deletions, name)
			}
			continue
		}
		if before.kind == after.kind {
			common = append(common, name)
		} else if name != "." {
			deletions = append(deletions, name)
		}
	}
	for name, node := range right {
		before, exists := left[name]
		if (exists && before.kind == node.kind) || name == "." {
			continue
		}
		if node.kind == svn.NodeDir {
			directories = append(directories, name)
		} else {
			files = append(files, name)
		}
	}
	sort.Slice(deletions, func(i, j int) bool { return pathDepth(deletions[i]) > pathDepth(deletions[j]) })
	sort.Slice(directories, func(i, j int) bool { return pathDepth(directories[i]) < pathDepth(directories[j]) })
	sort.Strings(files)
	sort.Strings(common)
	for _, name := range deletions {
		local := mergeLocalPath(targetPath, name)
		if err := database.Delete(ctx, local, wc.DeleteOptions{Force: options.Force}); err != nil {
			if errors.Is(err, svn.ErrClientModified) {
				if err := database.RecordTreeConflict(ctx, local, left[name].kind, "edited", "deleted", rightRevision); err != nil {
					return err
				}
				continue
			}
			return err
		}
		mergeNotify(options, notify.Notify{Action: notify.ActionUpdateDelete, Path: local})
	}
	for _, name := range directories {
		local := mergeLocalPath(targetPath, name)
		if reason, obstructed := mergeAddObstruction(ctx, database, local); obstructed {
			if err := database.RecordTreeConflict(ctx, local, svn.NodeDir, reason, "added", rightRevision); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(local, 0o755); err != nil {
			return err
		}
		if err := database.Add(ctx, local, wc.AddOptions{Depth: svn.DepthEmpty, Parents: true, Force: options.Force}); err != nil {
			return err
		}
		mergeNotify(options, notify.Notify{Action: notify.ActionUpdateAdd, Path: local, Kind: svn.NodeDir})
	}
	for _, name := range files {
		local := mergeLocalPath(targetPath, name)
		if reason, obstructed := mergeAddObstruction(ctx, database, local); obstructed {
			if err := database.RecordTreeConflict(ctx, local, svn.NodeFile, reason, "added", rightRevision); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(local, right[name].content, 0o644); err != nil {
			return err
		}
		if err := database.Add(ctx, local, wc.AddOptions{Parents: true, Force: options.Force}); err != nil {
			return err
		}
		if err := applyMergedProperties(ctx, database, local, nil, right[name].properties, options); err != nil {
			return err
		}
		mergeNotify(options, notify.Notify{Action: notify.ActionUpdateAdd, Path: local, Kind: svn.NodeFile})
	}
	for _, name := range common {
		before, after := left[name], right[name]
		local := mergeLocalPath(targetPath, name)
		if before.kind == svn.NodeFile && !bytes.Equal(before.content, after.content) {
			mine, err := os.ReadFile(local)
			if err != nil {
				return err
			}
			merged := internaldiff3.Merge(before.content, mine, after.content, ".r"+revisionString(leftRevision), ".r"+revisionString(rightRevision))
			if merged.Conflicted {
				switch options.Accept {
				case wc.ConflictWorking:
					merged.Contents = mine
				case wc.ConflictBase:
					merged.Contents = before.content
				case wc.ConflictMine:
					merged.Contents = mine
				case wc.ConflictTheirs:
					merged.Contents = after.content
				case wc.ConflictMineConflict:
					merged.Contents = wc.ResolveConflictHunks(merged.Contents, true)
				case wc.ConflictTheirsConflict:
					merged.Contents = wc.ResolveConflictHunks(merged.Contents, false)
				case wc.ConflictPostpone:
					if err := database.RecordTextConflict(ctx, local, before.content, mine, after.content, leftRevision, rightRevision); err != nil {
						return err
					}
				}
			}
			if err := os.WriteFile(local, merged.Contents, 0o644); err != nil {
				return err
			}
			mergeNotify(options, notify.Notify{Action: notify.ActionUpdateUpdate, Path: local, Kind: svn.NodeFile})
		}
		propertiesChanged := !propsEqualForMerge(before.properties, after.properties)
		if err := applyMergedProperties(ctx, database, local, before.properties, after.properties, options); err != nil {
			return err
		}
		if propertiesChanged {
			mergeNotify(options, notify.Notify{Action: notify.ActionPropertyModified, Path: local, Kind: before.kind})
		}
	}
	return nil
}

func mergeAddObstruction(ctx context.Context, database *wc.Database, targetPath string) (string, bool) {
	if _, err := os.Lstat(targetPath); os.IsNotExist(err) {
		return "", false
	}
	if _, err := database.Info(ctx, targetPath); err == nil {
		return "added", true
	}
	return "unversioned", true
}

func propsEqualForMerge(left, right svn.Props) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if !bytes.Equal(value, right[name]) {
			return false
		}
	}
	return true
}

func mergeNotify(options MergeOptions, event notify.Notify) {
	if options.Notify != nil {
		options.Notify(event)
	}
}

func applyMergedProperties(ctx context.Context, database *wc.Database, targetPath string, before, after svn.Props, options MergeOptions) error {
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return err
	}
	names := make(map[string]bool)
	for name := range before {
		names[name] = true
	}
	for name := range after {
		names[name] = true
	}
	var conflicted []string
	for name := range names {
		if bytes.Equal(before[name], after[name]) {
			continue
		}
		mine := info.WorkingProperties[name]
		value := after[name]
		if !bytes.Equal(mine, before[name]) && !bytes.Equal(mine, after[name]) {
			switch options.Accept {
			case wc.ConflictBase:
				value = before[name]
			case wc.ConflictMine, wc.ConflictMineConflict, wc.ConflictWorking:
				value = mine
			case wc.ConflictTheirs, wc.ConflictTheirsConflict:
				value = after[name]
			default:
				conflicted = append(conflicted, name)
				continue
			}
		}
		if err := database.SetProperty(ctx, targetPath, name, value, options.Force); err != nil {
			return err
		}
	}
	if len(conflicted) != 0 {
		return database.RecordPropertyConflict(ctx, targetPath, conflicted, info.WorkingProperties, after)
	}
	return nil
}

func (client *Client) recordMergeinfo(ctx context.Context, database *wc.Database, targetPath, source string, start, end svn.Revnum) error {
	working, err := client.WorkingCopyMergeinfo(ctx, targetPath, mergeinfo.InheritanceInherited)
	if err != nil {
		return err
	}
	current := working.Mergeinfo
	resolved, err := client.resolveTarget(ctx, source, InfoOptions{Revision: numericRevision(end)})
	if err != nil {
		return err
	}
	defer resolved.close()
	sourcePath := strings.TrimPrefix(strings.TrimSuffix(resolved.url, "/"), strings.TrimSuffix(resolved.repositoryRoot, "/"))
	if sourcePath == "" {
		sourcePath = "/"
	} else if sourcePath[0] != '/' {
		sourcePath = "/" + sourcePath
	}
	ranges := mergeinfo.Rangelist{{Start: start, End: end, Inheritable: true}}
	if start < end {
		current[sourcePath] = mergeinfo.MergeRangelists(current[sourcePath], ranges)
	} else {
		current[sourcePath] = mergeinfo.RemoveRangelist(mergeinfo.Rangelist{{Start: end, End: start, Inheritable: true}}, current[sourcePath], false)
	}
	text := mergeinfo.String(current)
	if text == "" {
		return database.SetProperty(ctx, targetPath, props.Mergeinfo, nil, false)
	}
	return database.SetProperty(ctx, targetPath, props.Mergeinfo, []byte(text), false)
}

func mergeLocalPath(root, name string) string {
	if name == "." {
		return root
	}
	return filepath.Join(root, filepath.FromSlash(name))
}

func pathDepth(name string) int {
	return strings.Count(name, "/") + 1
}
