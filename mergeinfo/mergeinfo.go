package mergeinfo

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
	svnpath "github.com/otuschhoff/go-svn/svn/path"
)

type Inheritance uint8

const (
	InheritanceExplicit Inheritance = iota
	InheritanceInherited
	InheritanceNearestAncestor
)

type Range struct {
	Start       svn.Revnum
	End         svn.Revnum
	Inheritable bool
}

type Rangelist []Range
type Mergeinfo map[string]Rangelist
type Catalog map[string]Mergeinfo

func Parse(value string) (Mergeinfo, error) {
	result := make(Mergeinfo)
	for lineNumber, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		separator := strings.LastIndexByte(line, ':')
		if separator < 0 {
			return nil, fmt.Errorf("%w: invalid mergeinfo line %d", svn.ErrMergeinfoParseError, lineNumber+1)
		}
		path, ranges := line[:separator], line[separator+1:]
		if !svnpath.FspathIsCanonical(path) || hasDotSegment(path) || path == "/" && strings.TrimSpace(ranges) == "" {
			return nil, fmt.Errorf("%w: invalid mergeinfo line %d", svn.ErrMergeinfoParseError, lineNumber+1)
		}
		parsed, err := ParseRangelist(ranges)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber+1, err)
		}
		result[path] = MergeRangelists(result[path], parsed)
	}
	return result, nil
}

func ParseRangelist(value string) (Rangelist, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%w: empty rangelist", svn.ErrMergeinfoParseError)
	}
	ranges := make(Rangelist, 0)
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		inheritable := !strings.HasSuffix(field, "*")
		field = strings.TrimSuffix(field, "*")
		first, last, paired := strings.Cut(field, "-")
		start, err := parseRevision(first)
		if err != nil {
			return nil, err
		}
		end := start
		if paired {
			end, err = parseRevision(last)
			if err != nil {
				return nil, err
			}
			if end <= start {
				return nil, fmt.Errorf("%w: reversed or empty textual range %q", svn.ErrMergeinfoParseError, field)
			}
		}
		ranges = append(ranges, Range{Start: start - 1, End: end, Inheritable: inheritable})
	}
	for index, first := range ranges {
		for _, second := range ranges[index+1:] {
			if first.Start < second.End && second.Start < first.End && first.Inheritable != second.Inheritable {
				return nil, fmt.Errorf("%w: overlapping ranges have different inheritance", svn.ErrMergeinfoParseError)
			}
		}
	}
	return Normalize(ranges), nil
}

func String(info Mergeinfo) string {
	paths := make([]string, 0, len(info))
	for path := range info {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var result strings.Builder
	for _, path := range paths {
		ranges := Normalize(info[path])
		if len(ranges) == 0 {
			continue
		}
		if result.Len() > 0 {
			result.WriteByte('\n')
		}
		result.WriteString(path)
		result.WriteByte(':')
		result.WriteString(ranges.String())
	}
	return result.String()
}

func (ranges Rangelist) String() string {
	parts := make([]string, 0, len(ranges))
	for _, item := range ranges {
		var text string
		switch {
		case item.Start == item.End-1:
			text = strconv.FormatInt(int64(item.End), 10)
		case item.Start-1 == item.End:
			text = "-" + strconv.FormatInt(int64(item.Start), 10)
		case item.Start < item.End:
			text = strconv.FormatInt(int64(item.Start+1), 10) + "-" + strconv.FormatInt(int64(item.End), 10)
		case item.Start > item.End:
			text = strconv.FormatInt(int64(item.Start), 10) + "-" + strconv.FormatInt(int64(item.End+1), 10)
		default:
			continue
		}
		if !item.Inheritable {
			text += "*"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ",")
}

func Normalize(ranges Rangelist) Rangelist {
	boundaries := make([]svn.Revnum, 0, len(ranges)*2)
	for _, item := range ranges {
		if item.Start < 0 || item.End <= item.Start {
			continue
		}
		boundaries = append(boundaries, item.Start, item.End)
	}
	return combine(boundaries, func(start, end svn.Revnum) (bool, bool) {
		present, inheritable := false, false
		for _, item := range ranges {
			if item.Start <= start && item.End >= end {
				present = true
				inheritable = inheritable || item.Inheritable
			}
		}
		return present, inheritable
	})
}

func MergeRangelists(first, second Rangelist) Rangelist {
	boundaries := collectBoundaries(first, second)
	return combine(boundaries, func(start, end svn.Revnum) (bool, bool) {
		pa, ia := covers(first, start, end)
		pb, ib := covers(second, start, end)
		return pa || pb, ia || ib
	})
}

func IntersectRangelists(first, second Rangelist, considerInheritance bool) Rangelist {
	boundaries := collectBoundaries(first, second)
	return combine(boundaries, func(start, end svn.Revnum) (bool, bool) {
		pa, ia := covers(first, start, end)
		pb, ib := covers(second, start, end)
		if considerInheritance && ia != ib {
			return false, false
		}
		return pa && pb, ia || ib
	})
}

func DiffRangelists(from, to Rangelist, considerInheritance bool) (deleted, added Rangelist) {
	return RemoveRangelist(to, from, considerInheritance), RemoveRangelist(from, to, considerInheritance)
}

func RemoveRangelist(eraser, whiteboard Rangelist, considerInheritance bool) Rangelist {
	boundaries := collectBoundaries(eraser, whiteboard)
	return combine(boundaries, func(start, end svn.Revnum) (bool, bool) {
		white, whiteInherited := covers(whiteboard, start, end)
		erase, eraseInherited := covers(eraser, start, end)
		if considerInheritance && whiteInherited != eraseInherited {
			erase = false
		}
		return white && !erase, whiteInherited
	})
}

func Reverse(ranges Rangelist) Rangelist {
	result := make(Rangelist, len(ranges))
	for index := range ranges {
		item := ranges[len(ranges)-1-index]
		result[index] = Range{Start: item.End, End: item.Start, Inheritable: item.Inheritable}
	}
	return result
}

func Inheritable(ranges Rangelist) Rangelist {
	result := make(Rangelist, 0, len(ranges))
	for _, item := range Normalize(ranges) {
		if item.Inheritable {
			result = append(result, item)
		}
	}
	return result
}

func ToRevs(ranges Rangelist) []svn.Revnum {
	var revisions []svn.Revnum
	for _, item := range Normalize(ranges) {
		for revision := item.Start + 1; revision <= item.End; revision++ {
			revisions = append(revisions, revision)
		}
	}
	return revisions
}

func Merge(first, second Mergeinfo) Mergeinfo {
	return combineMergeinfo(first, second, func(a, b Rangelist) Rangelist { return MergeRangelists(a, b) })
}
func Intersect(first, second Mergeinfo, considerInheritance bool) Mergeinfo {
	return combineMergeinfo(first, second, func(a, b Rangelist) Rangelist { return IntersectRangelists(a, b, considerInheritance) })
}
func Remove(eraser, whiteboard Mergeinfo, considerInheritance bool) Mergeinfo {
	return combineMergeinfo(whiteboard, eraser, func(a, b Rangelist) Rangelist { return RemoveRangelist(b, a, considerInheritance) })
}
func Diff(from, to Mergeinfo, considerInheritance bool) (deleted, added Mergeinfo) {
	return Remove(to, from, considerInheritance), Remove(from, to, considerInheritance)
}

func ReverseMergeinfo(info Mergeinfo) Mergeinfo {
	result := make(Mergeinfo, len(info))
	for path, ranges := range info {
		result[path] = Reverse(ranges)
	}
	return result
}
func InheritableMergeinfo(info Mergeinfo) Mergeinfo {
	result := make(Mergeinfo)
	for path, ranges := range info {
		if filtered := Inheritable(ranges); len(filtered) > 0 {
			result[path] = filtered
		}
	}
	return result
}
func MergeCatalogs(first, second Catalog) Catalog {
	result := cloneCatalog(first)
	for path, info := range second {
		result[path] = Merge(result[path], info)
	}
	return result
}

func combineMergeinfo(first, second Mergeinfo, operation func(Rangelist, Rangelist) Rangelist) Mergeinfo {
	result := make(Mergeinfo)
	keys := make(map[string]struct{}, len(first)+len(second))
	for key := range first {
		keys[key] = struct{}{}
	}
	for key := range second {
		keys[key] = struct{}{}
	}
	for key := range keys {
		if ranges := operation(first[key], second[key]); len(ranges) > 0 {
			result[key] = ranges
		}
	}
	return result
}

func collectBoundaries(lists ...Rangelist) []svn.Revnum {
	var values []svn.Revnum
	for _, list := range lists {
		for _, item := range Normalize(list) {
			values = append(values, item.Start, item.End)
		}
	}
	return values
}

func combine(boundaries []svn.Revnum, predicate func(svn.Revnum, svn.Revnum) (bool, bool)) Rangelist {
	if len(boundaries) == 0 {
		return nil
	}
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i] < boundaries[j] })
	unique := boundaries[:0]
	for _, value := range boundaries {
		if len(unique) == 0 || unique[len(unique)-1] != value {
			unique = append(unique, value)
		}
	}
	var result Rangelist
	for index := 0; index+1 < len(unique); index++ {
		start, end := unique[index], unique[index+1]
		present, inherited := predicate(start, end)
		if !present {
			continue
		}
		if len(result) > 0 && result[len(result)-1].End == start && result[len(result)-1].Inheritable == inherited {
			result[len(result)-1].End = end
		} else {
			result = append(result, Range{Start: start, End: end, Inheritable: inherited})
		}
	}
	return result
}

func covers(ranges Rangelist, start, end svn.Revnum) (bool, bool) {
	for _, item := range Normalize(ranges) {
		if item.Start <= start && item.End >= end {
			return true, item.Inheritable
		}
	}
	return false, false
}

func parseRevision(value string) (svn.Revnum, error) {
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 {
		return 0, fmt.Errorf("%w: invalid revision %q", svn.ErrMergeinfoParseError, value)
	}
	return svn.Revnum(number), nil
}

func hasDotSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func cloneCatalog(catalog Catalog) Catalog {
	result := make(Catalog, len(catalog))
	for path, info := range catalog {
		copied := make(Mergeinfo, len(info))
		for source, ranges := range info {
			copied[source] = append(Rangelist(nil), ranges...)
		}
		result[path] = copied
	}
	return result
}
