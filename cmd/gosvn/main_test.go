package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/auth"
	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/repos"
	"github.com/otuschhoff/go-svn/svn"
)

func TestHelpAndVersion(t *testing.T) {
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"help"}, "usage: gosvn <subcommand>"},
		{[]string{"--version"}, "gosvn, version 0.1.0\n"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), test.args, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("run(%v) code=%d stderr=%q", test.args, code, stderr.String())
		}
		if !strings.Contains(stdout.String(), test.want) {
			t.Fatalf("run(%v) output=%q, want substring %q", test.args, stdout.String(), test.want)
		}
	}
}

func TestCommandHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"help", "co"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := stdout.String(); got != "usage: gosvn checkout [options] [args]\n" {
		t.Fatalf("output=%q", got)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"help", "unknown"}, strings.NewReader(""), &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"unknown"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if got := stderr.String(); !strings.Contains(got, "unknown subcommand") || !strings.HasPrefix(got, "gosvn: E") {
		t.Fatalf("stderr=%q", got)
	}
}

func TestParseRevisionRanges(t *testing.T) {
	ranges, err := parseRevisionRanges("5:2,9")
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 || ranges[0].Start.Number != 5 || ranges[0].End.Number != 2 || ranges[1].Start.Number != 9 || ranges[1].End.Number != 9 {
		t.Fatalf("ranges=%#v", ranges)
	}
}

func TestWriteXMLEscapesValues(t *testing.T) {
	var output bytes.Buffer
	value := "<value>"
	if err := writeXML(&output, propertiesXML{Targets: []propertiesXMLTarget{{Path: "a&b", Properties: []propertyXML{{Name: "x", Value: &value}}}}}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, `path="a&amp;b"`) || !strings.Contains(got, "&lt;value&gt;") {
		t.Fatalf("xml=%q", got)
	}
}

func TestParseGlobalOptions(t *testing.T) {
	options, args, err := parseGlobalOptions([]string{"status", "--username", "alice", "--config-option=config:miscellany:global-ignores=*.tmp", "--non-interactive", "."})
	if err != nil {
		t.Fatal(err)
	}
	if options.username != "alice" || !options.nonInteractive || len(options.configOptions) != 1 {
		t.Fatalf("options=%#v", options)
	}
	if got := strings.Join(args, " "); got != "status ." {
		t.Fatalf("args=%q", got)
	}
}

func TestStaticProviderAllowsProviderFallback(t *testing.T) {
	provider := staticProvider{username: "alice", password: "secret"}
	if _, err := provider.Get(context.Background(), auth.SSLServerTrust, "realm", ""); !errors.Is(err, svn.ErrAuthnCredsUnavailable) {
		t.Fatalf("get error = %v", err)
	}
	if err := provider.Save(context.Background(), &auth.Credentials{}); !errors.Is(err, svn.ErrAuthnCredsNotSaved) {
		t.Fatalf("save error = %v", err)
	}
}

func TestParseTrustFailures(t *testing.T) {
	failures, err := parseTrustFailures("unknown-ca,cn-mismatch")
	if err != nil {
		t.Fatal(err)
	}
	if failures != sslUnknownCA|sslCNMismatch {
		t.Fatalf("failures = %d", failures)
	}
	if _, err := parseTrustFailures("unknown-ca,everything"); err == nil {
		t.Fatal("expected unknown failure to be rejected")
	}
}

func TestParseAcceptAndRevnum(t *testing.T) {
	if choice, err := parseAccept("theirs-conflict"); err != nil || choice != 6 {
		t.Fatalf("choice=%v error=%v", choice, err)
	}
	if revision, err := parseRevnum("42"); err != nil || revision != 42 {
		t.Fatalf("revision=%v error=%v", revision, err)
	}
	if _, err := parseRevnum("BASE"); err == nil {
		t.Fatal("expected BASE to be rejected")
	}
}

func TestParseChangeRanges(t *testing.T) {
	ranges, err := parseChangeRanges([]string{"5,-7", "9"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]svn.Revnum{{4, 5}, {7, 6}, {8, 9}}
	if len(ranges) != len(want) {
		t.Fatalf("ranges = %#v", ranges)
	}
	for index, revisionRange := range ranges {
		if revisionRange.Start.Number != want[index][0] || revisionRange.End.Number != want[index][1] {
			t.Fatalf("range %d = %#v, want %v", index, revisionRange, want[index])
		}
	}
	if _, err := parseChangeRanges([]string{"0"}); err == nil {
		t.Fatal("expected revision zero to be rejected")
	}
}

func TestCLIReadAndWorkingCopyMatrix(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	instance := client.New(nil)
	if _, err := instance.Mucc(ctx, rootURL, []client.Action{
		{Kind: client.ActionMkdir, Path: "trunk"},
		{Kind: client.ActionPut, Path: "trunk/readme.txt", Content: []byte("hello\n")},
		{Kind: client.ActionSetProperty, Path: "trunk/readme.txt", PropertyName: "custom:name", PropertyValue: []byte("value")},
	}, client.MuccOptions{RevisionProperties: svn.Props{props.Log: []byte("seed")}}); err != nil {
		t.Fatal(err)
	}

	infoOutput := runCLI(t, "info", "--xml", rootURL+"/trunk/readme.txt")
	var info infoXML
	if err := xml.Unmarshal([]byte(infoOutput), &info); err != nil || len(info.Entries) != 1 || info.Entries[0].Kind != "file" {
		t.Fatalf("info XML=%q parsed=%#v error=%v", infoOutput, info, err)
	}
	if got := runCLI(t, "cat", rootURL+"/trunk/readme.txt"); got != "hello\n" {
		t.Fatalf("cat=%q", got)
	}
	if got := runCLI(t, "list", rootURL+"/trunk"); got != "readme.txt\n" {
		t.Fatalf("list=%q", got)
	}
	var listed listXML
	if output := runCLI(t, "list", "--xml", rootURL+"/trunk"); xml.Unmarshal([]byte(output), &listed) != nil || len(listed.Lists) != 1 || len(listed.Lists[0].Entries) != 1 || listed.Lists[0].Entries[0].Name != "readme.txt" {
		t.Fatalf("list XML=%q parsed=%#v", output, listed)
	}
	if got := runCLI(t, "propget", "custom:name", rootURL+"/trunk/readme.txt"); got != "value\n" {
		t.Fatalf("propget=%q", got)
	}
	if got := runCLI(t, "proplist", rootURL+"/trunk/readme.txt"); got != "Properties on '"+rootURL+"/trunk/readme.txt':\n  custom:name\n" {
		t.Fatalf("proplist=%q", got)
	}
	var propertyList propertiesXML
	if output := runCLI(t, "proplist", "--xml", rootURL+"/trunk/readme.txt"); xml.Unmarshal([]byte(output), &propertyList) != nil || len(propertyList.Targets) != 1 || len(propertyList.Targets[0].Properties) != 1 || strings.Contains(output, ">value<") {
		t.Fatalf("proplist XML=%q parsed=%#v", output, propertyList)
	}
	propertyList = propertiesXML{}
	if output := runCLI(t, "proplist", "--xml", "-v", rootURL+"/trunk/readme.txt"); xml.Unmarshal([]byte(output), &propertyList) != nil || propertyList.Targets[0].Properties[0].Value == nil || *propertyList.Targets[0].Properties[0].Value != "value" {
		t.Fatalf("verbose proplist XML=%q parsed=%#v", output, propertyList)
	}
	if got := runCLI(t, "log", rootURL+"/trunk/readme.txt"); !strings.Contains(got, "r1 |") || !strings.Contains(got, "seed") {
		t.Fatalf("log=%q", got)
	}
	var history logXML
	if output := runCLI(t, "log", "--xml", "-v", rootURL+"/trunk/readme.txt"); xml.Unmarshal([]byte(output), &history) != nil || len(history.Entries) != 1 || len(history.Entries[0].Paths) == 0 || history.Entries[0].Paths[0].CopyfromRev != nil {
		t.Fatalf("log XML=%q parsed=%#v", output, history)
	}
	if got := runCLI(t, "blame", rootURL+"/trunk/readme.txt"); !strings.Contains(got, "     1") || !strings.Contains(got, "hello") {
		t.Fatalf("blame=%q", got)
	}
	var attribution blameXML
	if output := runCLI(t, "blame", "--xml", rootURL+"/trunk/readme.txt"); xml.Unmarshal([]byte(output), &attribution) != nil || len(attribution.Targets) != 1 || len(attribution.Targets[0].Entries) != 1 || attribution.Targets[0].Entries[0].LineNumber != 1 {
		t.Fatalf("blame XML=%q parsed=%#v", output, attribution)
	}

	working := filepath.Join(t.TempDir(), "working")
	if got := runCLI(t, "checkout", rootURL+"/trunk", working); !strings.Contains(got, "Checked out revision 1.") {
		t.Fatalf("checkout=%q", got)
	}
	if err := os.WriteFile(filepath.Join(working, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, "add", filepath.Join(working, "new.txt"))
	statusOutput := runCLI(t, "status", working)
	if !strings.Contains(statusOutput, "A       ") || !strings.Contains(statusOutput, "new.txt") {
		t.Fatalf("status=%q", statusOutput)
	}
	commitOutput := runCLI(t, "commit", "-m", "add new", working)
	if !strings.Contains(commitOutput, "Committed revision 2.") {
		t.Fatalf("commit=%q", commitOutput)
	}
	if got := runCLI(t, "status", "--xml", working); !strings.Contains(got, `<status>`) || !strings.Contains(got, `<target path="`+working+`">`) {
		t.Fatalf("status XML=%q", got)
	}
	if got := runCLI(t, "diff", "--summarize", "-r", "1:2", rootURL+"/trunk"); !strings.Contains(got, "A       ") || !strings.Contains(got, "new.txt") {
		t.Fatalf("diff summarize=%q", got)
	}
	var summary diffSummaryXML
	if output := runCLI(t, "diff", "--summarize", "--xml", "-r", "1:2", rootURL+"/trunk"); xml.Unmarshal([]byte(output), &summary) != nil || len(summary.Paths) != 1 || summary.Paths[0].Item != "added" || !strings.HasSuffix(summary.Paths[0].Path, "/trunk/new.txt") {
		t.Fatalf("diff XML=%q parsed=%#v", output, summary)
	}
}

func TestCLIURLMutationMatrix(t *testing.T) {
	ctx := context.Background()
	repository, err := repos.Create(ctx, filepath.Join(t.TempDir(), "repository"), repos.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rootURL := repository.URL()
	if got := runCLI(t, "mkdir", "--parents", "-m", "mkdir", rootURL+"/trunk/source"); !strings.Contains(got, "Committed revision 1.") {
		t.Fatalf("mkdir=%q", got)
	}
	if got := runCLI(t, "copy", "-m", "copy", rootURL+"/trunk/source", rootURL+"/trunk/copied"); !strings.Contains(got, "Committed revision 2.") {
		t.Fatalf("copy=%q", got)
	}
	if got := runCLI(t, "delete", "-m", "delete", rootURL+"/trunk/copied"); !strings.Contains(got, "Committed revision 3.") {
		t.Fatalf("delete=%q", got)
	}
}

func runCLI(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), append([]string{"--non-interactive", "--no-auth-cache"}, args...), strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("gosvn %v: code=%d stderr=%q stdout=%q", args, code, stderr.String(), stdout.String())
	}
	return stdout.String()
}
