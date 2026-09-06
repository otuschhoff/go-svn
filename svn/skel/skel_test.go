package skel

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestParseImplicitAtomsAllCharacters(t *testing.T) {
	terminators := []byte{'\t', '\n', '\f', '\r', ' ', '(', ')', '[', ']'}
	for atom := byte('A'); atom <= 'Z'; atom++ {
		for _, terminator := range terminators {
			input := []byte{atom, terminator}
			if terminator == '(' || terminator == ')' || terminator == '[' || terminator == ']' {
				input = input[:1]
			}
			node, err := Parse(input)
			if err != nil || !node.IsAtom() || !bytes.Equal(node.Atom, []byte{atom}) {
				t.Fatalf("Parse(%q) = %#v, %v", input, node, err)
			}
		}
	}
	for atom := byte('a'); atom <= 'z'; atom++ {
		node, err := Parse([]byte{atom})
		if err != nil || !bytes.Equal(node.Atom, []byte{atom}) {
			t.Fatalf("Parse(%q) = %#v, %v", atom, node, err)
		}
	}
}

func TestParseExplicitAtomsAllBytesAndSeparators(t *testing.T) {
	separators := []byte{'\t', '\n', '\f', '\r', ' '}
	for value := 0; value < 256; value++ {
		for _, separator := range separators {
			input := []byte{'1', separator, byte(value)}
			node, err := Parse(input)
			if err != nil || !node.IsAtom() || !bytes.Equal(node.Atom, []byte{byte(value)}) {
				t.Fatalf("Parse explicit byte %d separator %d = %#v, %v", value, separator, node, err)
			}
		}
	}
	allBytes := make([]byte, 256)
	for index := range allBytes {
		allBytes[index] = byte(index)
	}
	input := append([]byte("256 "), allBytes...)
	node, err := Parse(input)
	if err != nil || !bytes.Equal(node.Atom, allBytes) {
		t.Fatalf("all-byte atom failed: %v", err)
	}
}

func TestParseAndMarshalLists(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"()", "()"},
		{"(  )", "()"},
		{"(foo bar)", "(foo bar)"},
		{"(\tfoo\n(bar 0 )\r)", "(foo (bar 0 ))"},
		{"(3 foo3 bar)", "(foo bar)"},
		{"((a)(b)(c))", "((a) (b) (c))"},
	}
	for _, test := range tests {
		node, err := Parse([]byte(test.input))
		if err != nil {
			t.Fatalf("Parse(%q): %v", test.input, err)
		}
		encoded, err := node.MarshalBinary()
		if err != nil || string(encoded) != test.want {
			t.Errorf("Marshal(Parse(%q)) = %q, %v; want %q", test.input, encoded, err, test.want)
		}
		reparsed, err := Parse(encoded)
		if err != nil || !reflect.DeepEqual(node, reparsed) {
			t.Errorf("round trip %q = %#v, %v", encoded, reparsed, err)
		}
	}
}

func TestMarshalCanonicalAtomForms(t *testing.T) {
	tests := []struct {
		atom []byte
		want string
	}{
		{[]byte("name"), "name"}, {nil, "0 "}, {[]byte("1mplicit"), "8 1mplicit"},
		{[]byte("has space"), "9 has space"}, {[]byte("[bracket]"), "9 [bracket]"},
		{bytes.Repeat([]byte{'a'}, 99), string(bytes.Repeat([]byte{'a'}, 99))},
		{bytes.Repeat([]byte{'a'}, 100), "100 " + string(bytes.Repeat([]byte{'a'}, 100))},
	}
	for _, test := range tests {
		encoded, err := NewAtom(test.atom).MarshalBinary()
		if err != nil || string(encoded) != test.want {
			t.Errorf("Marshal(%q) = %q, %v; want %q", test.atom, encoded, err, test.want)
		}
	}
}

func TestProplistConversion(t *testing.T) {
	properties := svn.Props{
		"svn:author": []byte("oli"),
		"binary":     {0, '\n', 0xff},
	}
	node := PropsToProplist(properties)
	if !IsValidProplist(node) {
		t.Fatal("PropsToProplist returned invalid skeleton")
	}
	encoded, err := node.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if want := "(binary 3 \x00\n\xff svn:author oli)"; string(encoded) != want {
		t.Fatalf("encoded proplist = %q, want %q", encoded, want)
	}
	parsed, err := Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ProplistToProps(parsed)
	if err != nil || !bytes.Equal(roundTrip["binary"], properties["binary"]) || !bytes.Equal(roundTrip["svn:author"], properties["svn:author"]) {
		t.Fatalf("proplist round trip = %#v, %v", roundTrip, err)
	}
	roundTrip["binary"][0] = 1
	if properties["binary"][0] != 0 {
		t.Fatal("proplist result aliases input properties")
	}
	for _, invalid := range []*Node{nil, NewAtom([]byte("x")), NewList(NewString("key")), NewList(NewList(), NewString("value"))} {
		if IsValidProplist(invalid) {
			t.Errorf("IsValidProplist(%#v) = true", invalid)
		}
		if _, err := ProplistToProps(invalid); !errors.Is(err, svn.ErrFSMalformedSkel) {
			t.Errorf("ProplistToProps(%#v) error = %v", invalid, err)
		}
	}
}

func TestRejectsMalformedSkeletons(t *testing.T) {
	tests := [][]byte{
		nil, {'('}, {')'}, {'['}, {']'}, {' '}, []byte("Hello, World!"),
		[]byte("1mplicit"), []byte("1"), []byte("12"), []byte("-1 x"),
		[]byte("(100 )"), []byte("())"), []byte("foo bar"),
	}
	for _, input := range tests {
		if _, err := Parse(input); !errors.Is(err, svn.ErrFSMalformedSkel) {
			t.Errorf("Parse(%q) error = %v", input, err)
		}
	}
	deep := []byte("a")
	for range maxDepth + 1 {
		deep = append(append([]byte{'('}, deep...), ')')
	}
	if _, err := Parse(deep); !errors.Is(err, svn.ErrFSMalformedSkel) {
		t.Errorf("deep Parse error = %v", err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("()"))
	f.Add([]byte("(foo (3 bar 0 ))"))
	f.Fuzz(func(t *testing.T, input []byte) {
		node, err := Parse(input)
		if err != nil {
			return
		}
		encoded, err := node.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(encoded); err != nil {
			t.Fatalf("Parse(Marshal(Parse(input))): %v", err)
		}
	})
}
