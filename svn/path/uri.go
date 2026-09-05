package path

import (
	"fmt"
	"runtime"
	"strings"
)

func IsURL(value string) bool {
	separator := strings.Index(value, "://")
	if separator <= 0 {
		return false
	}
	for index, char := range value[:separator] {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (index > 0 && (char >= '0' && char <= '9' || char == '+' || char == '-' || char == '.'))) {
			return false
		}
	}
	return true
}

func URICanonicalize(value string) string {
	separator := strings.Index(value, "://")
	if separator < 1 {
		return ""
	}
	scheme := strings.ToLower(value[:separator])
	remainder := value[separator+3:]
	pathIndex := strings.IndexByte(remainder, '/')
	authority := remainder
	rawPath := ""
	if pathIndex >= 0 {
		authority = remainder[:pathIndex]
		rawPath = remainder[pathIndex:]
	}
	authority = canonicalAuthority(scheme, authority)
	decodedPath := normalizeURIEscapes(rawPath)
	canonicalPath := canonicalComponents(decodedPath, strings.HasPrefix(decodedPath, "/"))
	if canonicalPath == "/" {
		canonicalPath = ""
	}
	return scheme + "://" + authority + canonicalPath
}

func URIIsCanonical(value string) bool { return IsURL(value) && value == URICanonicalize(value) }

func URIIsRoot(value string) bool {
	value = URICanonicalize(value)
	separator := strings.Index(value, "://")
	if separator < 0 || strings.Contains(value[separator+3:], "/") {
		return false
	}
	return value[:separator] != "file" || value[separator+3:] == ""
}

func URIJoin(base string, components ...string) string {
	result := URICanonicalize(base)
	for _, component := range components {
		if IsURL(component) {
			result = URICanonicalize(component)
			continue
		}
		component = URIEncode(RelpathCanonicalize(component))
		result = joinCanonical(result, component)
	}
	return URICanonicalize(result)
}

func URIDirname(value string) string {
	value = URICanonicalize(value)
	root := uriRoot(value)
	directory, _ := splitPath(value, root)
	return directory
}

func URIBasename(value string) string {
	value = URICanonicalize(value)
	_, basename := splitPath(value, uriRoot(value))
	decoded, _ := URIDecode(basename)
	return decoded
}

func URISplit(value string) (string, string) {
	return URIDirname(value), URIBasename(value)
}

func URISkipAncestor(parent, child string) (string, bool) {
	parent, child = URICanonicalize(parent), URICanonicalize(child)
	if uriRoot(parent) != uriRoot(child) {
		return "", false
	}
	remainder, ok := skipAncestor(parent, child)
	if !ok {
		return "", false
	}
	decoded, err := URIDecode(remainder)
	return decoded, err == nil
}

func URIIsAncestor(parent, child string) bool {
	_, ok := URISkipAncestor(parent, child)
	return ok
}

func URIIsChild(parent, child string) (string, bool) {
	remainder, ok := URISkipAncestor(parent, child)
	return remainder, ok && remainder != ""
}

func URILongestAncestor(first, second string) string {
	first, second = URICanonicalize(first), URICanonicalize(second)
	return longestAncestor(first, second, URIDirname, func(a, b string) bool { return uriRoot(a) == uriRoot(b) })
}

func URICondense(targets []string, removeRedundant bool) (string, []string, error) {
	common, condensed := condense(targets, URICanonicalize, URILongestAncestor, URISkipAncestor, removeRedundant)
	return common, condensed, nil
}

func URIEncode(value string) string {
	var result strings.Builder
	const hexadecimal = "0123456789ABCDEF"
	for _, current := range []byte(value) {
		if isURISafe(current) {
			result.WriteByte(current)
		} else {
			result.WriteByte('%')
			result.WriteByte(hexadecimal[current>>4])
			result.WriteByte(hexadecimal[current&0x0f])
		}
	}
	return result.String()
}

func URIDecode(value string) (string, error) {
	decoded := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			decoded = append(decoded, value[index])
			continue
		}
		if index+2 >= len(value) {
			return "", fmt.Errorf("invalid URI escape at byte %d", index)
		}
		high, low := fromHex(value[index+1]), fromHex(value[index+2])
		if high < 0 || low < 0 {
			return "", fmt.Errorf("invalid URI escape at byte %d", index)
		}
		decoded = append(decoded, byte(high<<4|low))
		index += 2
	}
	return string(decoded), nil
}

func URIToDirent(value string) (string, error) {
	canonical := URICanonicalize(value)
	if !strings.HasPrefix(canonical, "file://") {
		return "", fmt.Errorf("%q is not a file URL", value)
	}
	remainder := canonical[len("file://"):]
	slash := strings.IndexByte(remainder, '/')
	host, rawPath := remainder, ""
	if slash >= 0 {
		host, rawPath = remainder[:slash], remainder[slash:]
	}
	decoded, err := URIDecode(rawPath)
	if err != nil {
		return "", err
	}
	if host != "" && host != "localhost" {
		if runtime.GOOS != "windows" {
			return "", fmt.Errorf("file URL host %q is not local", host)
		}
		return DirentCanonicalize("//" + host + decoded), nil
	}
	if runtime.GOOS == "windows" && len(decoded) >= 3 && decoded[0] == '/' && isDrive(decoded[1:]) {
		decoded = decoded[1:]
	}
	if decoded == "" {
		decoded = "/"
	}
	return DirentCanonicalize(decoded), nil
}

func DirentToFileURL(value string) (string, error) {
	value = DirentCanonicalize(value)
	if !DirentIsAbsolute(value) {
		return "", fmt.Errorf("dirent %q is not absolute", value)
	}
	if runtime.GOOS == "windows" {
		if isUNC(value) {
			parts := strings.SplitN(value[2:], "/", 3)
			path := ""
			if len(parts) == 3 {
				path = "/" + parts[2]
			}
			return URICanonicalize("file://" + parts[0] + "/" + parts[1] + path), nil
		}
		return URICanonicalize("file:///" + value), nil
	}
	return URICanonicalize("file://" + URIEncode(value)), nil
}

func uriRoot(value string) string {
	separator := strings.Index(value, "://")
	if separator < 0 {
		return ""
	}
	remainder := value[separator+3:]
	if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
		return value[:separator+3+slash]
	}
	return value
}

func canonicalAuthority(scheme, authority string) string {
	userInfo := ""
	hostPort := authority
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		userInfo, hostPort = authority[:at+1], authority[at+1:]
	}
	hostPort = strings.ToLower(hostPort)
	defaults := map[string]string{"http": "80", "https": "443", "svn": "3690"}
	if port, ok := defaults[scheme]; ok {
		separator := -1
		if strings.HasPrefix(hostPort, "[") {
			if bracket := strings.IndexByte(hostPort, ']'); bracket >= 0 && bracket+1 < len(hostPort) && hostPort[bracket+1] == ':' {
				separator = bracket + 1
			}
		} else if strings.Count(hostPort, ":") == 1 {
			separator = strings.IndexByte(hostPort, ':')
		}
		if separator >= 0 {
			candidate := hostPort[separator+1:]
			if candidate == "" || candidate == port {
				hostPort = hostPort[:separator]
			}
		}
	}
	return userInfo + hostPort
}

func normalizeURIEscapes(value string) string {
	decoded := make([]byte, 0, len(value))
	for index := 0; index < len(value); index++ {
		current := value[index]
		if current == '%' && index+2 < len(value) {
			high, low := fromHex(value[index+1]), fromHex(value[index+2])
			if high >= 0 && low >= 0 {
				decoded = append(decoded, byte(high<<4|low))
				index += 2
				continue
			}
		}
		decoded = append(decoded, current)
	}
	return URIEncode(string(decoded))
}

func isURISafe(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("-._~!$&'()*+,;=:@/", rune(value))
}

func fromHex(value byte) int {
	switch {
	case value >= '0' && value <= '9':
		return int(value - '0')
	case value >= 'a' && value <= 'f':
		return int(value-'a') + 10
	case value >= 'A' && value <= 'F':
		return int(value-'A') + 10
	default:
		return -1
	}
}
