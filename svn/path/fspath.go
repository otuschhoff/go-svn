package path

import "strings"

func FspathCanonicalize(value string) string {
	return canonicalComponents("/"+strings.TrimLeft(value, "/"), true)
}

func FspathIsCanonical(value string) bool {
	return strings.HasPrefix(value, "/") && value == FspathCanonicalize(value)
}

func FspathIsRoot(value string) bool { return value == "/" }

func FspathJoin(base string, components ...string) string {
	result := FspathCanonicalize(base)
	for _, component := range components {
		result = joinCanonical(result, RelpathCanonicalize(component))
	}
	return FspathCanonicalize(result)
}

func FspathDirname(value string) string {
	directory, _ := splitPath(FspathCanonicalize(value), "/")
	return directory
}

func FspathBasename(value string) string {
	_, basename := splitPath(FspathCanonicalize(value), "/")
	return basename
}

func FspathSplit(value string) (string, string) {
	return splitPath(FspathCanonicalize(value), "/")
}

func FspathSkipAncestor(parent, child string) (string, bool) {
	return skipAncestor(FspathCanonicalize(parent), FspathCanonicalize(child))
}

func FspathIsAncestor(parent, child string) bool {
	_, ok := FspathSkipAncestor(parent, child)
	return ok
}

func FspathIsChild(parent, child string) (string, bool) {
	remainder, ok := FspathSkipAncestor(parent, child)
	return remainder, ok && remainder != ""
}

func FspathLongestAncestor(first, second string) string {
	return longestAncestor(FspathCanonicalize(first), FspathCanonicalize(second), FspathDirname, func(string, string) bool { return true })
}

func FspathCondense(targets []string, removeRedundant bool) (string, []string, error) {
	common, condensed := condense(targets, FspathCanonicalize, FspathLongestAncestor, FspathSkipAncestor, removeRedundant)
	return common, condensed, nil
}
