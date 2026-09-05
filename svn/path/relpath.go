package path

import "strings"

func RelpathCanonicalize(value string) string {
	return canonicalComponents(strings.TrimLeft(value, "/"), false)
}

func RelpathIsCanonical(value string) bool {
	return !strings.HasPrefix(value, "/") && value == RelpathCanonicalize(value)
}

func RelpathIsRoot(value string) bool { return value == "" }

func RelpathJoin(base string, components ...string) string {
	result := RelpathCanonicalize(base)
	for _, component := range components {
		result = joinCanonical(result, RelpathCanonicalize(component))
	}
	return result
}

func RelpathDirname(value string) string {
	directory, _ := splitPath(RelpathCanonicalize(value), "")
	return directory
}

func RelpathBasename(value string) string {
	_, basename := splitPath(RelpathCanonicalize(value), "")
	return basename
}

func RelpathSplit(value string) (string, string) {
	return splitPath(RelpathCanonicalize(value), "")
}

func RelpathSkipAncestor(parent, child string) (string, bool) {
	return skipAncestor(RelpathCanonicalize(parent), RelpathCanonicalize(child))
}

func RelpathIsAncestor(parent, child string) bool {
	_, ok := RelpathSkipAncestor(parent, child)
	return ok
}

func RelpathIsChild(parent, child string) (string, bool) {
	remainder, ok := RelpathSkipAncestor(parent, child)
	return remainder, ok && remainder != ""
}

func RelpathLongestAncestor(first, second string) string {
	return longestAncestor(RelpathCanonicalize(first), RelpathCanonicalize(second), RelpathDirname, func(string, string) bool { return true })
}

func RelpathCondense(targets []string, removeRedundant bool) (string, []string, error) {
	common, condensed := condense(targets, RelpathCanonicalize, RelpathLongestAncestor, RelpathSkipAncestor, removeRedundant)
	return common, condensed, nil
}
