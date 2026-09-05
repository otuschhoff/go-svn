package path

import "strings"

func canonicalComponents(value string, absolute bool) string {
	parts := strings.Split(value, "/")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		kept = append(kept, part)
	}
	result := strings.Join(kept, "/")
	if absolute {
		return "/" + result
	}
	return result
}

func joinCanonical(base string, components ...string) string {
	result := base
	for _, component := range components {
		if component == "" {
			continue
		}
		if result == "" || strings.HasSuffix(result, "/") {
			result += component
		} else {
			result += "/" + component
		}
	}
	return result
}

func splitPath(value, root string) (string, string) {
	if value == root || value == "" {
		return value, ""
	}
	index := strings.LastIndexByte(value, '/')
	if index < 0 {
		return root, value
	}
	if index < len(root) {
		return root, value[len(root):]
	}
	if index == 0 {
		return "/", value[1:]
	}
	return value[:index], value[index+1:]
}

func skipAncestor(parent, child string) (string, bool) {
	if parent == child {
		return "", true
	}
	if parent == "" {
		if strings.HasPrefix(child, "/") {
			return "", false
		}
		return child, true
	}
	prefix := parent
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if !strings.HasPrefix(child, prefix) {
		return "", false
	}
	return child[len(prefix):], true
}

func longestAncestor(first, second string, dirname func(string) string, compatible func(string, string) bool) string {
	if !compatible(first, second) {
		return ""
	}
	candidate := first
	for {
		if _, ok := skipAncestor(candidate, second); ok {
			return candidate
		}
		parent := dirname(candidate)
		if parent == candidate {
			return ""
		}
		candidate = parent
	}
}

func condense(targets []string, canonicalize func(string) string, longest func(string, string) string, skip func(string, string) (string, bool), removeRedundant bool) (string, []string) {
	if len(targets) == 0 {
		return "", nil
	}
	canonical := make([]string, len(targets))
	for index, target := range targets {
		canonical[index] = canonicalize(target)
	}
	common := canonical[0]
	for _, target := range canonical[1:] {
		common = longest(common, target)
	}
	result := make([]string, 0, len(canonical))
	for index, target := range canonical {
		remainder, ok := skip(common, target)
		if common == "" || !ok {
			remainder = target
		}
		if removeRedundant {
			redundant := false
			for otherIndex, other := range canonical {
				if index == otherIndex {
					continue
				}
				if child, related := skip(other, target); related && child != "" {
					redundant = true
					break
				}
			}
			if redundant {
				continue
			}
		}
		result = append(result, remainder)
	}
	return common, result
}
