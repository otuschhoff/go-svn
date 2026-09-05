package path

import (
	"os"
	"runtime"
	"testing"
)

type canonicalCase struct {
	input string
	want  string
}

var portableDirentCases = []canonicalCase{
	{"", ""}, {".", ""}, {"/", "/"}, {"/.", "/"}, {"./", ""},
	{"./.", ""}, {"//", "/"}, {"/////", "/"}, {"./././.", ""},
	{"////././.", "/"}, {"foo", "foo"}, {".foo", ".foo"}, {"foo.", "foo."},
	{"/foo", "/foo"}, {"foo/", "foo"}, {"foo./", "foo."},
	{"foo./.", "foo."}, {"foo././/.", "foo."}, {"/foo/bar", "/foo/bar"},
	{"foo/..", "foo/.."}, {"foo/../", "foo/.."}, {"foo/../.", "foo/.."},
	{"foo//.//bar", "foo/bar"}, {"//foo", "/foo"}, {"///foo", "/foo"},
	{"/.//./.foo", "/.foo"}, {".///.foo", ".foo"}, {"../foo", "../foo"},
	{"../../foo/", "../../foo"}, {"../../foo/..", "../../foo/.."},
	{"/../../", "/../.."}, {"X:/foo", "X:/foo"}, {"X:", "X:"},
	{"X:foo", "X:foo"}, {"C:/folder/subfolder/file", "C:/folder/subfolder/file"},
}

var portableRelpathCases = []canonicalCase{
	{"", ""}, {".", ""}, {"/", ""}, {"/.", ""}, {"./", ""},
	{"./.", ""}, {"//", ""}, {"/////", ""}, {"./././.", ""},
	{"////././.", ""}, {"foo", "foo"}, {".foo", ".foo"}, {"foo.", "foo."},
	{"/foo", "foo"}, {"foo/", "foo"}, {"foo./", "foo."}, {"foo./.", "foo."},
	{"foo././/.", "foo."}, {"/foo/bar", "foo/bar"}, {"foo/..", "foo/.."},
	{"foo/../", "foo/.."}, {"foo/../.", "foo/.."}, {"foo//.//bar", "foo/bar"},
	{"//foo", "foo"}, {"///foo", "foo"}, {"/.//./.foo", ".foo"},
	{".///.foo", ".foo"}, {"../foo", "../foo"}, {"../../foo/", "../../foo"},
	{"../../foo/..", "../../foo/.."}, {"/../../", "../.."},
	{"http://hst", "http:/hst"}, {"http://hst/foo/../bar", "http:/hst/foo/../bar"},
	{"http:///", "http:"}, {"svn+ssh:///", "svn+ssh:"},
}

var portableURICases = []canonicalCase{
	{"http://hst", "http://hst"}, {"http://hst/foo/../bar", "http://hst/foo/../bar"},
	{"http://hst/", "http://hst"}, {"http:///", "http://"},
	{"http:///example.com/", "http:///example.com"},
	{"http:////example.com/", "http:///example.com"},
	{"http://///////example.com/", "http:///example.com"},
	{"https://", "https://"}, {"file:///", "file://"}, {"file://", "file://"},
	{"svn:///", "svn://"}, {"svn+ssh:///", "svn+ssh://"},
	{"http://HST/", "http://hst"}, {"http://HST/FOO/BaR", "http://hst/FOO/BaR"},
	{"svn+ssh://jens@10.0.1.1", "svn+ssh://jens@10.0.1.1"},
	{"svn+ssh://j.raNDom@HST/BaR", "svn+ssh://j.raNDom@hst/BaR"},
	{"svn+SSH://j.random:jRaY@HST/BaR", "svn+ssh://j.random:jRaY@hst/BaR"},
	{"SVN+ssh://j.raNDom:jray@HST/BaR", "svn+ssh://j.raNDom:jray@hst/BaR"},
	{"fILe:///Users/jrandom/wc", "file:///Users/jrandom/wc"},
	{"file://SRV/shr/repos", "file://srv/shr/repos"},
	{"file://SRV/SHR/REPOS", "file://srv/SHR/REPOS"},
	{"http://server////", "http://server"},
	{"http://server/file//", "http://server/file"},
	{"http://server//.//f//", "http://server/f"},
	{"http://server/d/.", "http://server/d"},
	{"http://server/d/%2E", "http://server/d"},
	{"http://server/d/./q", "http://server/d/q"},
	{"http://server/d/%2E/q", "http://server/d/q"},
	{"http://server/%", "http://server/%25"},
	{"http://server/%25", "http://server/%25"},
	{"http://server/%/d", "http://server/%25/d"},
	{"http://server/%25/d", "http://server/%25/d"},
	{"http://server/+", "http://server/+"},
	{"http://server/%2B", "http://server/+"},
	{"http://server/ ", "http://server/%20"},
	{"http://server/#", "http://server/%23"},
	{"http://server/d/a%2Fb", "http://server/d/a/b"},
	{"http://server/d/.%2F.", "http://server/d"},
	{"http://server/d/%2E%2F%2E", "http://server/d"},
	{"file:///C%3a/temp", "file:///C:/temp"},
	{"http://server/cr%AB", "http://server/cr%AB"},
	{"http://server/cr%ab", "http://server/cr%AB"},
	{"file:///folder/c#", "file:///folder/c%23"},
	{"file:///fld/with space", "file:///fld/with%20space"},
	{"file:///%DE%AD%BE%EF", "file:///%DE%AD%BE%EF"},
	{"file:///%de%ad%be%ef", "file:///%DE%AD%BE%EF"},
	{"http://server:", "http://server"},
	{"http://server:/", "http://server"},
	{"http://server:80", "http://server"},
	{"http://SERVER:80", "http://server"},
	{"http://server:80/p", "http://server/p"},
	{"https://Server:443/q", "https://server/q"},
	{"svn://sERVER:3690/r", "svn://server/r"},
	{"svn://server:/r", "svn://server/r"},
	{"http://server:1", "http://server:1"},
	{"http://server:443", "http://server:443"},
	{"https://SERVER:80/", "https://server:80"},
	{"file:///C%7C/temp/REPOS", "file:///C%7C/temp/REPOS"},
	{"file:///C|/temp/REPOS", "file:///C%7C/temp/REPOS"},
	{"file:///C:/", "file:///C:"},
	{"http://[::1]/", "http://[::1]"},
	{"http://[::1]:80/", "http://[::1]"},
	{"https://[::1]:443", "https://[::1]"},
	{"http://[FACE:B00C::]/s", "http://[face:b00c::]/s"},
	{"svn+ssh://b@[1:2::3]/s", "svn+ssh://b@[1:2::3]/s"},
	{"file:///A%2f%2Fb%2fc", "file:///A/b/c"},
	{"file:///A%2fb%2f%2Fc", "file:///A/b/c"},
	{"file://./foo", "file://./foo"}, {"http://./foo", "http://./foo"},
	{"http://server:81:81/", "http://server:81:81"},
	{"http://server:81foo/", "http://server:81foo"},
	{"http://server::/", "http://server::"},
}

func TestCanonicalizeReferenceTables(t *testing.T) {
	for _, test := range portableDirentCases {
		if got := DirentCanonicalize(test.input); got != test.want {
			t.Errorf("DirentCanonicalize(%q) = %q, want %q", test.input, got, test.want)
		}
		if got := DirentIsCanonical(test.input); got != (test.input == test.want) {
			t.Errorf("DirentIsCanonical(%q) = %v", test.input, got)
		}
		if got := DirentCanonicalize(test.want); got != test.want {
			t.Errorf("DirentCanonicalize canonical %q = %q", test.want, got)
		}
	}
	for _, test := range portableRelpathCases {
		if got := RelpathCanonicalize(test.input); got != test.want {
			t.Errorf("RelpathCanonicalize(%q) = %q, want %q", test.input, got, test.want)
		}
		if got := RelpathIsCanonical(test.input); got != (test.input == test.want) {
			t.Errorf("RelpathIsCanonical(%q) = %v", test.input, got)
		}
		if got := RelpathCanonicalize(test.want); got != test.want {
			t.Errorf("RelpathCanonicalize canonical %q = %q", test.want, got)
		}
	}
	for _, test := range portableURICases {
		if got := URICanonicalize(test.input); got != test.want {
			t.Errorf("URICanonicalize(%q) = %q, want %q", test.input, got, test.want)
		}
		if got := URIIsCanonical(test.input); got != (test.input == test.want) {
			t.Errorf("URIIsCanonical(%q) = %v", test.input, got)
		}
		if got := URICanonicalize(test.want); got != test.want {
			t.Errorf("URICanonicalize canonical %q = %q", test.want, got)
		}
	}
}

func TestSplitReferenceTables(t *testing.T) {
	direntCases := []struct{ input, directory, basename string }{
		{"/foo/bar", "/foo", "bar"}, {"/foo", "/", "foo"}, {"foo", "", "foo"},
		{".bar", "", ".bar"}, {"foo./.bar", "foo.", ".bar"}, {"../foo", "..", "foo"},
		{"", "", ""}, {"/", "/", ""}, {"X:foo/bar", "X:foo", "bar"},
	}
	for _, test := range direntCases {
		directory, basename := DirentSplit(test.input)
		if directory != test.directory || basename != test.basename {
			t.Errorf("DirentSplit(%q) = (%q, %q), want (%q, %q)", test.input, directory, basename, test.directory, test.basename)
		}
	}
	uriCases := []struct{ input, directory, basename string }{
		{"http://server/foo/bar", "http://server/foo", "bar"},
		{"http://server/some%20dir/foo%20bar", "http://server/some%20dir", "foo bar"},
		{"http://server/foo", "http://server", "foo"}, {"http://server", "http://server", ""},
		{"file://", "file://", ""}, {"file:///a", "file://", "a"},
	}
	for _, test := range uriCases {
		directory, basename := URISplit(test.input)
		if directory != test.directory || basename != test.basename {
			t.Errorf("URISplit(%q) = (%q, %q), want (%q, %q)", test.input, directory, basename, test.directory, test.basename)
		}
	}
}

func TestAncestorReferenceTables(t *testing.T) {
	tests := []struct {
		parent, child, want string
		related             bool
	}{
		{"", "", "", true}, {"", "foo", "foo", true}, {"", "/foo", "", false},
		{"/", "/", "", true}, {"/", "/foo", "foo", true}, {"/foo", "/foot", "", false},
		{"/foo", "/foo/bar", "bar", true}, {"foo", "foo/bar", "bar", true},
		{"foo.", "foo./.bar", ".bar", true}, {"../foo", "..", "", false},
		{"/foo/bar/zig", "/foo/bar/zi", "", false},
	}
	for _, test := range tests {
		got, related := DirentSkipAncestor(test.parent, test.child)
		if got != test.want || related != test.related {
			t.Errorf("DirentSkipAncestor(%q, %q) = (%q, %v), want (%q, %v)", test.parent, test.child, got, related, test.want, test.related)
		}
	}
	uriTests := []struct {
		parent, child, want string
		related             bool
	}{
		{"http://test", "http://test", "", true},
		{"http://test", "http://test/foo", "foo", true},
		{"http://test", "http://taste", "", false},
		{"http://test", "file://test/foo", "", false},
		{"http://foo/bar", "http://foo/ba", "", false},
	}
	for _, test := range uriTests {
		got, related := URISkipAncestor(test.parent, test.child)
		if got != test.want || related != test.related {
			t.Errorf("URISkipAncestor(%q, %q) = (%q, %v), want (%q, %v)", test.parent, test.child, got, related, test.want, test.related)
		}
	}
}

func TestChildReferenceMatrices(t *testing.T) {
	type pair struct{ parent, child string }
	dirents := []string{
		"/foo/bar", "/foo/bars", "/foo/baz", "/foo/bar/baz", "/flu/blar/blaz",
		"/foo/bar/baz/bing/boom", "", "foo", ".foo", "/", "foo2",
	}
	direntRemainders := map[pair]string{
		{"/foo/bar", "/foo/bar/baz"}:               "baz",
		{"/foo/bar", "/foo/bar/baz/bing/boom"}:     "baz/bing/boom",
		{"/foo/bar/baz", "/foo/bar/baz/bing/boom"}: "bing/boom",
		{"", "foo"}:                     "foo",
		{"", ".foo"}:                    ".foo",
		{"", "foo2"}:                    "foo2",
		{"/", "/foo/bar"}:               "foo/bar",
		{"/", "/foo/bars"}:              "foo/bars",
		{"/", "/foo/baz"}:               "foo/baz",
		{"/", "/foo/bar/baz"}:           "foo/bar/baz",
		{"/", "/flu/blar/blaz"}:         "flu/blar/blaz",
		{"/", "/foo/bar/baz/bing/boom"}: "foo/bar/baz/bing/boom",
	}
	for _, parent := range dirents {
		for _, child := range dirents {
			want, expected := direntRemainders[pair{parent, child}]
			got, ok := DirentIsChild(parent, child)
			if got != want || ok != expected {
				t.Errorf("DirentIsChild(%q, %q) = (%q, %v), want (%q, %v)", parent, child, got, ok, want, expected)
			}
		}
	}

	relpaths := []string{"", "foo", "foo/bar", "foo/bars", "foo/bar/baz", ".foo", "bar", "bar/baz"}
	relpathRemainders := map[pair]string{
		{"", "foo"}:                "foo",
		{"", "foo/bar"}:            "foo/bar",
		{"", "foo/bars"}:           "foo/bars",
		{"", "foo/bar/baz"}:        "foo/bar/baz",
		{"", ".foo"}:               ".foo",
		{"", "bar"}:                "bar",
		{"", "bar/baz"}:            "bar/baz",
		{"foo", "foo/bar"}:         "bar",
		{"foo", "foo/bars"}:        "bars",
		{"foo", "foo/bar/baz"}:     "bar/baz",
		{"foo/bar", "foo/bar/baz"}: "baz",
		{"bar", "bar/baz"}:         "baz",
	}
	for _, parent := range relpaths {
		for _, child := range relpaths {
			want, expected := relpathRemainders[pair{parent, child}]
			got, ok := RelpathIsChild(parent, child)
			if got != want || ok != expected {
				t.Errorf("RelpathIsChild(%q, %q) = (%q, %v), want (%q, %v)", parent, child, got, ok, want, expected)
			}
		}
	}

	fspaths := []string{"/", "/f", "/foo", "/foo/bar", "/foo/bars", "/foo/bar/baz"}
	fspathRemainders := map[pair]string{
		{"/", "/f"}:                  "f",
		{"/", "/foo"}:                "foo",
		{"/", "/foo/bar"}:            "foo/bar",
		{"/", "/foo/bars"}:           "foo/bars",
		{"/", "/foo/bar/baz"}:        "foo/bar/baz",
		{"/foo", "/foo/bar"}:         "bar",
		{"/foo", "/foo/bars"}:        "bars",
		{"/foo", "/foo/bar/baz"}:     "bar/baz",
		{"/foo/bar", "/foo/bar/baz"}: "baz",
	}
	for _, parent := range fspaths {
		for _, child := range fspaths {
			want, expected := fspathRemainders[pair{parent, child}]
			got, ok := FspathIsChild(parent, child)
			if got != want || ok != expected {
				t.Errorf("FspathIsChild(%q, %q) = (%q, %v), want (%q, %v)", parent, child, got, ok, want, expected)
			}
		}
	}
}

func TestLongestAncestorReferenceTables(t *testing.T) {
	direntCases := []struct{ first, second, want string }{
		{"/foo", "/foo/bar", "/foo"}, {"/foo/bar", "foo/bar", ""}, {"/", "/foo", "/"},
		{"foo/bar", "foo", "foo"}, {"/rif", "/raf", "/"}, {"foo", "bar", ""},
		{"foo.", "foo./.bar", "foo."}, {"", "", ""}, {"X:foo", "Y:foo", ""},
	}
	for _, test := range direntCases {
		if got := DirentLongestAncestor(test.first, test.second); got != test.want {
			t.Errorf("DirentLongestAncestor(%q, %q) = %q, want %q", test.first, test.second, got, test.want)
		}
		if got := DirentLongestAncestor(test.second, test.first); got != test.want {
			t.Errorf("DirentLongestAncestor reverse = %q, want %q", got, test.want)
		}
	}
	uriCases := []struct{ first, second, want string }{
		{"http://test", "http://test", "http://test"},
		{"http://test", "http://taste", ""},
		{"http://test", "http://test/foo", "http://test"},
		{"http://test", "file://test/foo", ""},
		{"file:///A/C", "file:///A/D", "file:///A"},
	}
	for _, test := range uriCases {
		if got := URILongestAncestor(test.first, test.second); got != test.want {
			t.Errorf("URILongestAncestor(%q, %q) = %q, want %q", test.first, test.second, got, test.want)
		}
	}
}

func TestJoinRootAndUnderRootReferenceTables(t *testing.T) {
	joins := []struct{ base, component, want string }{
		{"abc", "def", "abc/def"}, {"/", "d", "/d"}, {"/abc", "/def", "/def"},
		{"", "/", "/"}, {"/", "", "/"}, {"", "abc", "abc"}, {"abc", "", "abc"},
	}
	for _, test := range joins {
		if got := DirentJoin(test.base, test.component); got != test.want {
			t.Errorf("DirentJoin(%q, %q) = %q, want %q", test.base, test.component, got, test.want)
		}
	}
	underRoot := []struct {
		base, relative, want string
		ok                   bool
	}{
		{"", "", "", true}, {"", "r", "r", true}, {"", "r/..", "", true},
		{"", "r/../..", "", false}, {"", "..", "", false}, {"b", "r/..", "b", true},
		{"b", "..", "", false}, {"/", "r", "/r", true}, {"/", "r/../..", "", false},
		{"/b", "r/./bb", "/b/r/bb", true}, {"/b", "r/../bb", "/b/bb", true},
		{"/b", "../bb", "", false}, {"/b", "/r", "", false}, {"b", "b", "b/b", true},
	}
	for _, test := range underRoot {
		got, ok := DirentIsUnderRoot(test.base, test.relative)
		if got != test.want || ok != test.ok {
			t.Errorf("DirentIsUnderRoot(%q, %q) = (%q, %v), want (%q, %v)", test.base, test.relative, got, ok, test.want, test.ok)
		}
	}
	rootCases := []struct {
		value string
		want  bool
	}{
		{"file://", true}, {"file://a", false}, {"file:///a", false},
		{"http://server", true}, {"http://server/file", false}, {"http://", true},
	}
	for _, test := range rootCases {
		if got := URIIsRoot(test.value); got != test.want {
			t.Errorf("URIIsRoot(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func TestFileURLReferenceTables(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("portable table contains POSIX file URL cases")
	}
	fromURL := []struct{ input, want string }{
		{"file://", "/"}, {"file:///dir", "/dir"}, {"file:///dir/path", "/dir/path"},
		{"file://localhost", "/"}, {"file://localhost/dir", "/dir"},
		{"file:///A%7C%5Cdir", "/A|\\dir"}, {"file:///A:%5Cdir", "/A:\\dir"},
	}
	for _, test := range fromURL {
		got, err := URIToDirent(test.input)
		if err != nil || got != test.want {
			t.Errorf("URIToDirent(%q) = (%q, %v), want %q", test.input, got, err, test.want)
		}
	}
	toURL := []struct{ input, want string }{
		{"/a/b", "file:///a/b"}, {"/a", "file:///a"}, {"/", "file://"},
		{"/File#$", "file:///File%23$"},
	}
	for _, test := range toURL {
		got, err := DirentToFileURL(test.input)
		if err != nil || got != test.want {
			t.Errorf("DirentToFileURL(%q) = (%q, %v), want %q", test.input, got, err, test.want)
		}
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := DirentAbsolute("abc")
	if err != nil || absolute != DirentJoin(DirentInternalStyle(workingDirectory), "abc") {
		t.Errorf("DirentAbsolute(abc) = (%q, %v)", absolute, err)
	}
}

func FuzzURICanonicalize(f *testing.F) {
	for _, test := range portableURICases {
		f.Add(test.input)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if !IsURL(value) {
			return
		}
		canonical := URICanonicalize(value)
		if got := URICanonicalize(canonical); got != canonical {
			t.Fatalf("canonicalization is not idempotent: %q -> %q -> %q", value, canonical, got)
		}
	})
}
