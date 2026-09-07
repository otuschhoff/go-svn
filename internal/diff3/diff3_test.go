package diff3

import "testing"

func TestMerge(t *testing.T) {
	tests := []struct {
		name       string
		base, mine string
		theirs     string
		want       string
		conflict   bool
	}{
		{name: "separate edits", base: "a\nb\nc\n", mine: "A\nb\nc\n", theirs: "a\nb\nC\n", want: "A\nb\nC\n"},
		{name: "same edit", base: "a\n", mine: "b\n", theirs: "b\n", want: "b\n"},
		{name: "conflict", base: "a\n", mine: "mine\n", theirs: "theirs\n", want: "<<<<<<< .mine\nmine\n||||||| .r1\na\n=======\ntheirs\n>>>>>>> .r2\n", conflict: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Merge([]byte(test.base), []byte(test.mine), []byte(test.theirs), ".r1", ".r2")
			if string(result.Contents) != test.want || result.Conflicted != test.conflict {
				t.Fatalf("Merge = (%q, %v), want (%q, %v)", result.Contents, result.Conflicted, test.want, test.conflict)
			}
		})
	}
}
