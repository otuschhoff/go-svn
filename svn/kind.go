package svn

import "fmt"

type NodeKind uint8

const (
	NodeNone NodeKind = iota
	NodeFile
	NodeDir
	NodeUnknown
	NodeSymlink
)

func (kind NodeKind) String() string {
	switch kind {
	case NodeNone:
		return "none"
	case NodeFile:
		return "file"
	case NodeDir:
		return "dir"
	case NodeUnknown:
		return "unknown"
	case NodeSymlink:
		return "symlink"
	default:
		return fmt.Sprintf("NodeKind(%d)", kind)
	}
}

func ParseNodeKind(value string) (NodeKind, error) {
	for _, kind := range []NodeKind{NodeNone, NodeFile, NodeDir, NodeUnknown, NodeSymlink} {
		if value == kind.String() {
			return kind, nil
		}
	}
	return NodeUnknown, fmt.Errorf("unknown node kind %q", value)
}

type Depth int8

const (
	DepthUnknown    Depth = -2
	DepthExclude    Depth = -1
	DepthEmpty      Depth = 0
	DepthFiles      Depth = 1
	DepthImmediates Depth = 2
	DepthInfinity   Depth = 3
)

func (depth Depth) String() string {
	switch depth {
	case DepthUnknown:
		return "unknown"
	case DepthExclude:
		return "exclude"
	case DepthEmpty:
		return "empty"
	case DepthFiles:
		return "files"
	case DepthImmediates:
		return "immediates"
	case DepthInfinity:
		return "infinity"
	default:
		return fmt.Sprintf("Depth(%d)", depth)
	}
}

func ParseDepth(value string) (Depth, error) {
	for _, depth := range []Depth{DepthUnknown, DepthExclude, DepthEmpty, DepthFiles, DepthImmediates, DepthInfinity} {
		if value == depth.String() {
			return depth, nil
		}
	}
	return DepthUnknown, fmt.Errorf("unknown depth %q", value)
}

type Tristate uint8

const (
	TristateUnknown Tristate = iota
	TristateFalse
	TristateTrue
)
