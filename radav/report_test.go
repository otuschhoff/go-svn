package radav

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

func TestReadReports(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == "OPTIONS" {
			writer.Header().Set("SVN-Repository-Root", "/repo")
			writer.Header().Set("SVN-Repository-UUID", "uuid")
			writer.Header().Set("SVN-Youngest-Rev", "7")
			writer.Header().Set("SVN-Rev-Root-Stub", "/repo/!svn/rvr")
			writer.Header().Set("SVN-Rev-Stub", "/repo/!svn/rev")
			return
		}
		if request.Method != "REPORT" {
			t.Fatalf("method=%s", request.Method)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		writer.Header().Set("Content-Type", "application/xml")
		switch {
		case strings.Contains(string(body), "<S:get-locations"):
			assertReportPath(t, request, "/repo/!svn/rvr/7")
			_, _ = io.WriteString(writer, `<S:get-locations-report xmlns:S="svn:"><S:location rev="7" path="/trunk/file"/><S:location rev="3" path="/old/file"/></S:get-locations-report>`)
		case strings.Contains(string(body), "<S:get-location-segments"):
			assertReportPath(t, request, "/repo/!svn/rvr/7")
			_, _ = io.WriteString(writer, `<S:get-location-segments-report xmlns:S="svn:"><S:location-segment path="trunk/file" range-start="5" range-end="7"/><S:location-segment range-start="4" range-end="4"/></S:get-location-segments-report>`)
		case strings.Contains(string(body), "<S:get-deleted-rev-report"):
			_, _ = io.WriteString(writer, `<S:get-deleted-rev-report xmlns:S="svn:" xmlns:D="DAV:"><D:version-name>6</D:version-name></S:get-deleted-rev-report>`)
		case strings.Contains(string(body), "<S:mergeinfo-report"):
			_, _ = io.WriteString(writer, `<S:mergeinfo-report xmlns:S="svn:"><S:mergeinfo-item><S:mergeinfo-path>/trunk</S:mergeinfo-path><S:mergeinfo-info>/branch:1-3</S:mergeinfo-info></S:mergeinfo-item></S:mergeinfo-report>`)
		case strings.Contains(string(body), "<S:inherited-props-report"):
			encoded := base64.StdEncoding.EncodeToString([]byte{0, 1, 2})
			_, _ = io.WriteString(writer, `<S:inherited-props-report xmlns:S="svn:" xmlns:V="http://subversion.tigris.org/xmlns/dav/"><S:iprop-item><S:iprop-path>trunk</S:iprop-path><S:iprop-propname>custom</S:iprop-propname><S:iprop-propval V:encoding="base64">`+encoded+`</S:iprop-propval></S:iprop-item></S:inherited-props-report>`)
		case strings.Contains(string(body), "<S:get-locks-report"):
			assertReportPath(t, request, "/repo/trunk")
			if !strings.Contains(string(body), `depth="infinity"`) {
				t.Fatalf("lock body=%s", body)
			}
			_, _ = io.WriteString(writer, `<S:get-locks-report xmlns:S="svn:"><S:lock><S:path>/trunk/file</S:path><S:token>token</S:token><S:owner>alice</S:owner><S:comment>note</S:comment><S:creationdate>2020-01-01T00:00:00Z</S:creationdate></S:lock></S:get-locks-report>`)
		case strings.Contains(string(body), "<S:log-report"):
			_, _ = io.WriteString(writer, `<S:log-report xmlns:S="svn:" xmlns:D="DAV:"><S:log-item><D:version-name>7</D:version-name><D:creator-displayname>alice</D:creator-displayname><S:date>2020-01-01T00:00:00Z</S:date><D:comment>message</D:comment><S:added-path node-kind="file" text-mods="true" prop-mods="false" copyfrom-path="/old" copyfrom-rev="6">/trunk/file</S:added-path><S:has-children/></S:log-item></S:log-report>`)
		case strings.Contains(string(body), "<S:file-revs-report"):
			_, _ = io.WriteString(writer, `<S:file-revs-report xmlns:S="svn:"><S:file-rev path="/trunk/file" rev="7"><S:rev-prop name="svn:author">alice</S:rev-prop><S:set-prop name="custom">value</S:set-prop><S:remove-prop name="gone"/><S:merged-revision/></S:file-rev></S:file-revs-report>`)
		default:
			t.Fatalf("report body=%s", body)
		}
	}))
	defer server.Close()
	session := openTestSession(t, server.URL+"/repo")
	ctx := context.Background()

	locations, err := session.GetLocations(ctx, "trunk/file", 7, []svn.Revnum{7, 3})
	if err != nil || locations[7] != "/trunk/file" || locations[3] != "/old/file" {
		t.Fatalf("locations=%v error=%v", locations, err)
	}
	var segments []ra.LocationSegment
	err = session.GetLocationSegments(ctx, "trunk/file", 7, 7, 1, func(segment ra.LocationSegment) error { segments = append(segments, segment); return nil })
	if err != nil || len(segments) != 2 || segments[1].Path != "" || segments[1].RangeStart != 4 {
		t.Fatalf("segments=%v error=%v", segments, err)
	}
	deleted, err := session.GetDeletedRev(ctx, "trunk/file", 7, 7)
	if err != nil || deleted != 6 {
		t.Fatalf("deleted=%d error=%v", deleted, err)
	}
	merge, err := session.GetMergeinfo(ctx, []string{"trunk"}, 7, mergeinfo.InheritanceInherited, false)
	if err != nil || merge["trunk"] == nil {
		t.Fatalf("mergeinfo=%v error=%v", merge, err)
	}
	inherited, err := session.GetInheritedProps(ctx, "trunk/file", 7)
	if err != nil || len(inherited) != 1 || string(inherited[0].Props["custom"]) != string([]byte{0, 1, 2}) {
		t.Fatalf("inherited=%v error=%v", inherited, err)
	}
	locks, err := session.GetLocks(ctx, "trunk", svn.DepthInfinity)
	if err != nil || locks["/trunk/file"] == nil || locks["/trunk/file"].Owner != "alice" {
		t.Fatalf("locks=%v error=%v", locks, err)
	}
	var logs []*svn.LogEntry
	err = session.Log(ctx, ra.LogOptions{Paths: []string{"trunk"}, Start: 7, End: 7, DiscoverChangedPaths: true}, func(entry *svn.LogEntry) error { logs = append(logs, entry); return nil })
	if err != nil || len(logs) != 1 || !logs[0].HasChildren || len(logs[0].ChangedPaths) != 1 || logs[0].ChangedPaths[0].CopyfromRev != 6 {
		t.Fatalf("logs=%#v error=%v", logs, err)
	}
	var fileRevision ra.FileRevision
	err = session.GetFileRevs(ctx, "trunk/file", 1, 7, true, func(_ context.Context, revision ra.FileRevision) error { fileRevision = revision; return nil })
	if err != nil || fileRevision.Revision != 7 || !fileRevision.Merged || string(fileRevision.RevProps["svn:author"]) != "alice" || string(fileRevision.PropDiffs["custom"]) != "value" {
		t.Fatalf("file revision=%#v error=%v", fileRevision, err)
	}
	if value, ok := fileRevision.PropDiffs["gone"]; !ok || value != nil {
		t.Fatalf("removed property=%v, present=%v", value, ok)
	}
}

func assertReportPath(t *testing.T, request *http.Request, expected string) {
	t.Helper()
	if request.URL.Path != expected {
		t.Fatalf("report path=%q, want %q", request.URL.Path, expected)
	}
}
