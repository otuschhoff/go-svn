package mergeinfo

import (
	"reflect"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestParseAndStringCanonical(t *testing.T) {
	info, err := Parse("/trunk:1-3,3-5,7*\n/branch:9,10")
	if err != nil {
		t.Fatal(err)
	}
	want := "/branch:9-10\n/trunk:1-5,7*"
	if got := String(info); got != want {
		t.Fatalf("String = %q, want %q", got, want)
	}
}

func TestRangelistAlgebra(t *testing.T) {
	first, _ := ParseRangelist("1-5,8-10*")
	second, _ := ParseRangelist("4-8")
	if got := MergeRangelists(first, second).String(); got != "1-8,9-10*" {
		t.Errorf("merge = %q", got)
	}
	if got := IntersectRangelists(first, second, false).String(); got != "4-5,8" {
		t.Errorf("intersection = %q", got)
	}
	if got := RemoveRangelist(second, first, false).String(); got != "1-3,9-10*" {
		t.Errorf("remove = %q", got)
	}
	deleted, added := DiffRangelists(first, second, false)
	if deleted.String() != "1-3,9-10*" || added.String() != "6-7" {
		t.Errorf("diff = %q, %q", deleted.String(), added.String())
	}
}

func TestMergeinfoInternalPredicates(t *testing.T) {
	if !hasDotSegment("/a/../b") {
		t.Fatal("dot segment was not detected")
	}
	first, _ := ParseRangelist("1-5,8-10*")
	second, _ := ParseRangelist("4-8")
	if got := RemoveRangelist(second, first, false).String(); got != "1-3,9-10*" {
		t.Fatalf("from minus to = %q", got)
	}
}

func TestInheritanceAndReverse(t *testing.T) {
	ranges, _ := ParseRangelist("1-2*,3-4")
	if got := Inheritable(ranges).String(); got != "3-4" {
		t.Fatalf("inheritable = %q", got)
	}
	reversed := Reverse(ranges)
	want := Rangelist{{Start: 4, End: 2, Inheritable: true}, {Start: 2, End: 0, Inheritable: false}}
	if !reflect.DeepEqual(reversed, want) {
		t.Fatalf("reverse = %#v", reversed)
	}
	if got := reversed.String(); got != "4-3,2-1*" {
		t.Fatalf("reversed string = %q", got)
	}
	if got := ToRevs(ranges); !reflect.DeepEqual(got, []svn.Revnum{1, 2, 3, 4}) {
		t.Fatalf("revisions = %v", got)
	}
}

func TestReferenceInheritanceAlgebra(t *testing.T) {
	first, _ := ParseRangelist("1-6,12-16,30-32*,40-42")
	second, _ := ParseRangelist("1,3-4*,7,9,11-12,31-34*,38-44")
	if got := IntersectRangelists(first, second, true).String(); got != "1,12,31-32*,40-42" {
		t.Fatalf("inheritance-aware intersection = %q", got)
	}
	if got := IntersectRangelists(first, second, false).String(); got != "1,3-4,12,31-32*,40-42" {
		t.Fatalf("inheritance-agnostic intersection = %q", got)
	}
	whiteboard, _ := ParseRangelist("1-44*")
	eraser, _ := ParseRangelist("5")
	if got := RemoveRangelist(eraser, whiteboard, true).String(); got != "1-44*" {
		t.Fatalf("inheritance-aware removal = %q", got)
	}
	if got := RemoveRangelist(eraser, whiteboard, false).String(); got != "1-4*,6-44*" {
		t.Fatalf("inheritance-agnostic removal = %q", got)
	}
}

func TestMergeinfoMapDiffIntersectAndRemove(t *testing.T) {
	from, _ := Parse("/trunk:1,3-4,7,9,11-12,31-34\n/old:2")
	to, _ := Parse("/trunk:1-6,12-16,30-32\n/new:4")
	deleted, added := Diff(from, to, false)
	if got := String(deleted); got != "/old:2\n/trunk:7,9,11,33-34" {
		t.Fatalf("deleted = %q", got)
	}
	if got := String(added); got != "/new:4\n/trunk:2,5-6,13-16,30" {
		t.Fatalf("added = %q", got)
	}
	if got := String(Intersect(from, to, false)); got != "/trunk:1,3-4,12,31-32" {
		t.Fatalf("intersection = %q", got)
	}
	if got := String(Remove(to, from, false)); got != String(deleted) {
		t.Fatalf("remove = %q", got)
	}
}

func TestMergeinfoAndCatalogOperations(t *testing.T) {
	first, _ := Parse("/trunk:1-3")
	second, _ := Parse("/trunk:3-5\n/branch:7")
	if got := String(Merge(first, second)); got != "/branch:7\n/trunk:1-5" {
		t.Fatalf("merge = %q", got)
	}
	catalog := MergeCatalogs(Catalog{"/wc": first}, Catalog{"/wc": second, "/other": first})
	if len(catalog) != 2 || String(catalog["/wc"]) != "/branch:7\n/trunk:1-5" {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestParseRejectsInvalidMergeinfo(t *testing.T) {
	for _, value := range []string{"trunk:1", "/trunk:0", "/trunk:3-1", "/trunk:3-3", "/trunk:3-7*,4-8", "/trunk:", "/a/../b:1"} {
		if _, err := Parse(value); err == nil {
			t.Errorf("Parse(%q) succeeded", value)
		}
	}
	if info, err := Parse("/:path:3"); err != nil || info["/:path"].String() != "3" {
		t.Fatalf("colon path = %#v, %v", info, err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add("/trunk:1-3,5*\n/branch:7")
	f.Add("invalid")
	f.Fuzz(func(t *testing.T, value string) {
		parsed, err := Parse(value)
		if err != nil {
			return
		}
		reparsed, err := Parse(String(parsed))
		if err != nil {
			t.Fatal(err)
		}
		if String(reparsed) != String(parsed) {
			t.Fatal("canonical mergeinfo is unstable")
		}
	})
}
