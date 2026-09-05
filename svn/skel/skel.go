package skel

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

const (
	maxSize  = 64 << 20
	maxDepth = 64
)

type Node struct {
	Atom     []byte
	Children []*Node
	list     bool
}

func NewAtom(value []byte) *Node {
	return &Node{Atom: append([]byte(nil), value...)}
}

func NewString(value string) *Node { return NewAtom([]byte(value)) }

func NewList(children ...*Node) *Node {
	return &Node{Children: append([]*Node(nil), children...), list: true}
}

func (node *Node) IsAtom() bool { return node != nil && !node.list }

func (node *Node) IsList() bool { return node != nil && node.list }

func IsValidProplist(node *Node) bool {
	if !node.IsList() || len(node.Children)%2 != 0 {
		return false
	}
	for _, child := range node.Children {
		if !child.IsAtom() {
			return false
		}
	}
	return true
}

func PropsToProplist(properties svn.Props) *Node {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	children := make([]*Node, 0, len(keys)*2)
	for _, key := range keys {
		children = append(children, NewString(key), NewAtom(properties[key]))
	}
	return NewList(children...)
}

func ProplistToProps(node *Node) (svn.Props, error) {
	if !IsValidProplist(node) {
		return nil, malformed("invalid property-list skeleton")
	}
	properties := make(svn.Props, len(node.Children)/2)
	for index := 0; index < len(node.Children); index += 2 {
		properties[string(node.Children[index].Atom)] = append([]byte(nil), node.Children[index+1].Atom...)
	}
	return properties, nil
}

func Parse(data []byte) (*Node, error) {
	if len(data) == 0 || len(data) > maxSize {
		return nil, malformed("invalid skeleton size %d", len(data))
	}
	parser := parser{data: data}
	node, err := parser.parse(0)
	if err != nil {
		return nil, err
	}
	parser.skipSpace()
	if parser.position != len(data) {
		return nil, malformed("trailing skeleton data at byte %d", parser.position)
	}
	return node, nil
}

func Read(reader io.Reader) (*Node, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxSize+1))
	if err != nil {
		return nil, svn.Wrap(svn.ErrFSMalformedSkel, "read skeleton", err)
	}
	return Parse(data)
}

func (node *Node) MarshalBinary() ([]byte, error) {
	if node == nil {
		return nil, malformed("nil skeleton")
	}
	var output bytes.Buffer
	if err := node.write(&output, 0); err != nil {
		return nil, err
	}
	if output.Len() > maxSize {
		return nil, malformed("skeleton exceeds %d bytes", maxSize)
	}
	return output.Bytes(), nil
}

func (node *Node) Write(writer io.Writer) error {
	data, err := node.MarshalBinary()
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, bytes.NewReader(data))
	return err
}

func (node *Node) write(output *bytes.Buffer, depth int) error {
	if node == nil {
		return malformed("nil skeleton child")
	}
	if depth > maxDepth {
		return malformed("skeleton nesting exceeds %d", maxDepth)
	}
	if node.list {
		output.WriteByte('(')
		for index, child := range node.Children {
			if index != 0 {
				output.WriteByte(' ')
			}
			if err := child.write(output, depth+1); err != nil {
				return err
			}
		}
		output.WriteByte(')')
		return nil
	}
	if useImplicit(node.Atom) {
		output.Write(node.Atom)
		return nil
	}
	output.WriteString(strconv.Itoa(len(node.Atom)))
	output.WriteByte(' ')
	output.Write(node.Atom)
	return nil
}

type parser struct {
	data     []byte
	position int
}

func (parser *parser) parse(depth int) (*Node, error) {
	if depth > maxDepth {
		return nil, malformed("skeleton nesting exceeds %d", maxDepth)
	}
	if parser.position >= len(parser.data) {
		return nil, malformed("unexpected end of skeleton")
	}
	current := parser.data[parser.position]
	if current == '(' {
		return parser.parseList(depth)
	}
	if isName(current) {
		return parser.parseImplicit(), nil
	}
	return parser.parseExplicit()
}

func (parser *parser) parseList(depth int) (*Node, error) {
	parser.position++
	children := make([]*Node, 0)
	for {
		parser.skipSpace()
		if parser.position >= len(parser.data) {
			return nil, malformed("unterminated skeleton list")
		}
		if parser.data[parser.position] == ')' {
			parser.position++
			return NewList(children...), nil
		}
		child, err := parser.parse(depth + 1)
		if err != nil {
			return nil, err
		}
		children = append(children, child)
	}
}

func (parser *parser) parseImplicit() *Node {
	start := parser.position
	parser.position++
	for parser.position < len(parser.data) {
		current := parser.data[parser.position]
		if isSpace(current) || isParen(current) {
			break
		}
		parser.position++
	}
	return NewAtom(parser.data[start:parser.position])
}

func (parser *parser) parseExplicit() (*Node, error) {
	start := parser.position
	for parser.position < len(parser.data) && parser.data[parser.position] >= '0' && parser.data[parser.position] <= '9' {
		parser.position++
	}
	if parser.position == start || parser.position >= len(parser.data) || !isSpace(parser.data[parser.position]) {
		return nil, malformed("invalid explicit atom at byte %d", start)
	}
	length, err := strconv.ParseUint(string(parser.data[start:parser.position]), 10, 64)
	if err != nil || length > maxSize {
		return nil, malformed("invalid atom length at byte %d", start)
	}
	parser.position++
	if uint64(len(parser.data)-parser.position) < length {
		return nil, malformed("truncated explicit atom at byte %d", start)
	}
	end := parser.position + int(length)
	node := NewAtom(parser.data[parser.position:end])
	parser.position = end
	return node, nil
}

func (parser *parser) skipSpace() {
	for parser.position < len(parser.data) && isSpace(parser.data[parser.position]) {
		parser.position++
	}
}

func useImplicit(atom []byte) bool {
	if len(atom) == 0 || len(atom) >= 100 || !isName(atom[0]) {
		return false
	}
	for _, current := range atom[1:] {
		if isSpace(current) || isParen(current) {
			return false
		}
	}
	return true
}

func isName(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isSpace(value byte) bool {
	return value == '\t' || value == '\n' || value == '\f' || value == '\r' || value == ' '
}

func isParen(value byte) bool {
	return value == '(' || value == ')' || value == '[' || value == ']'
}

func malformed(format string, arguments ...any) error {
	return svn.NewError(svn.ErrFSMalformedSkel, fmt.Sprintf(format, arguments...))
}
