package path

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

func DirentCanonicalize(value string) string {
	if runtime.GOOS == "windows" {
		value = strings.ReplaceAll(value, `\`, "/")
		if isDrive(value) {
			value = strings.ToUpper(value[:1]) + value[1:]
		}
		if isUNC(value) {
			parts := strings.FieldsFunc(value[2:], func(r rune) bool { return r == '/' })
			if len(parts) >= 2 {
				parts[0] = strings.ToLower(parts[0])
				return "//" + strings.Join(parts, "/")
			}
		}
	}
	absolute := strings.HasPrefix(value, "/")
	prefix := ""
	if runtime.GOOS == "windows" && isDrive(value) {
		prefix = strings.ToUpper(value[:2])
		value = value[2:]
		absolute = strings.HasPrefix(value, "/")
	}
	result := canonicalComponents(value, absolute)
	if prefix != "" {
		if result == "/" {
			return prefix + "/"
		}
		return prefix + result
	}
	return result
}

func DirentIsCanonical(value string) bool { return value == DirentCanonicalize(value) }

func DirentIsAbsolute(value string) bool {
	if runtime.GOOS == "windows" {
		return strings.HasPrefix(value, "/") || (isDrive(value) && len(value) >= 3 && value[0] >= 'A' && value[0] <= 'Z' && value[2] == '/')
	}
	return strings.HasPrefix(value, "/")
}

func DirentIsRoot(value string) bool {
	value = DirentCanonicalize(value)
	if value == "/" {
		return true
	}
	if runtime.GOOS == "windows" {
		if isDrive(value) && (len(value) == 2 || value[2:] == "/") {
			return true
		}
		if isUNC(value) {
			return strings.Count(value[2:], "/") == 1
		}
	}
	return false
}

func DirentJoin(base string, components ...string) string {
	result := DirentCanonicalize(base)
	for _, component := range components {
		component = DirentCanonicalize(component)
		if component == "" {
			continue
		}
		if DirentIsAbsolute(component) || (runtime.GOOS == "windows" && isDrive(component)) {
			result = component
			continue
		}
		if strings.HasPrefix(component, "/") {
			if runtime.GOOS == "windows" && isDrive(result) {
				result = result[:2] + component
			} else {
				result = component
			}
			continue
		}
		result = joinCanonical(result, component)
	}
	return DirentCanonicalize(result)
}

func DirentDirname(value string) string {
	value = DirentCanonicalize(value)
	root := direntRoot(value)
	directory, _ := splitPath(value, root)
	return directory
}

func DirentBasename(value string) string {
	value = DirentCanonicalize(value)
	_, basename := splitPath(value, direntRoot(value))
	return basename
}

func DirentSplit(value string) (string, string) {
	value = DirentCanonicalize(value)
	return splitPath(value, direntRoot(value))
}

func DirentSkipAncestor(parent, child string) (string, bool) {
	parent, child = DirentCanonicalize(parent), DirentCanonicalize(child)
	if !sameDirentRoot(parent, child) {
		return "", false
	}
	return skipAncestor(parent, child)
}

func DirentIsAncestor(parent, child string) bool {
	_, ok := DirentSkipAncestor(parent, child)
	return ok
}

func DirentIsChild(parent, child string) (string, bool) {
	remainder, ok := DirentSkipAncestor(parent, child)
	return remainder, ok && remainder != ""
}

func DirentLongestAncestor(first, second string) string {
	first, second = DirentCanonicalize(first), DirentCanonicalize(second)
	return longestAncestor(first, second, DirentDirname, sameDirentRoot)
}

func DirentCondense(targets []string, removeRedundant bool) (string, []string, error) {
	common, condensed := condense(targets, DirentCanonicalize, DirentLongestAncestor, DirentSkipAncestor, removeRedundant)
	return common, condensed, nil
}

func DirentLocalStyle(value string) string {
	value = DirentCanonicalize(value)
	if value == "" {
		return "."
	}
	if runtime.GOOS == "windows" {
		return strings.ReplaceAll(value, "/", `\`)
	}
	return value
}

func DirentInternalStyle(value string) string {
	if runtime.GOOS == "windows" {
		value = strings.ReplaceAll(value, `\`, "/")
	}
	return DirentCanonicalize(value)
}

func DirentIsUnderRoot(base, relative string) (string, bool) {
	if relative == "" || relative == "." {
		return DirentCanonicalize(base), true
	}
	if DirentIsAbsolute(relative) {
		return "", false
	}
	if runtime.GOOS == "windows" {
		relative = strings.ReplaceAll(relative, `\`, "/")
	}
	parts := strings.Split(relative, "/")
	result := DirentCanonicalize(base)
	depth := 0
	for _, part := range parts {
		switch part {
		case ".", "":
		case "..":
			if depth == 0 {
				return "", false
			}
			result = DirentDirname(result)
			depth--
		default:
			result = DirentJoin(result, part)
			depth++
		}
	}
	return result, true
}

func DirentAbsolute(value string) (string, error) {
	if DirentIsAbsolute(value) {
		return DirentCanonicalize(value), nil
	}
	if runtime.GOOS == "windows" && isDrive(value) {
		return "", fmt.Errorf("drive-relative paths require operating-system drive state")
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}
	return DirentJoin(DirentInternalStyle(workingDirectory), value), nil
}

func direntRoot(value string) string {
	if runtime.GOOS == "windows" {
		if isUNC(value) {
			parts := strings.Split(value[2:], "/")
			if len(parts) >= 2 {
				return "//" + parts[0] + "/" + parts[1]
			}
		}
		if isDrive(value) {
			if len(value) >= 3 && value[2] == '/' {
				return value[:3]
			}
			return value[:2]
		}
	}
	if strings.HasPrefix(value, "/") {
		return "/"
	}
	return ""
}

func sameDirentRoot(first, second string) bool {
	return direntRoot(first) == direntRoot(second)
}

func isDrive(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':'
}

func isUNC(value string) bool {
	if !strings.HasPrefix(value, "//") {
		return false
	}
	parts := strings.Split(value[2:], "/")
	return len(parts) >= 2 && parts[0] != "" && parts[0] != "." && parts[0] != ".." && parts[1] != "" && parts[1] != "." && parts[1] != ".."
}
