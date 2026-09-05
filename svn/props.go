package svn

import "strings"

type Props map[string][]byte

func (props Props) Clone() Props {
	if props == nil {
		return nil
	}
	clone := make(Props, len(props))
	for name, value := range props {
		clone[name] = append([]byte(nil), value...)
	}
	return clone
}

func IsSVNProp(name string) bool { return strings.HasPrefix(name, "svn:") }

func IsRegularProp(name string) bool {
	return !IsEntryProp(name) && !IsWCProp(name)
}

func IsEntryProp(name string) bool { return strings.HasPrefix(name, "svn:entry:") }

func IsWCProp(name string) bool { return strings.HasPrefix(name, "svn:wc:") }
