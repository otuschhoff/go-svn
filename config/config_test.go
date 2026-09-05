package config

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePreservesAndExpands(t *testing.T) {
	input := "# comment\n[DEFAULT]\nWho = world\n[Section]\nBase = earth\nGreeting = hello %(who)s\nLong = first\n  second\n"
	document, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got := document.Get("SECTION", "greeting", ""); got != "hello world" {
		t.Fatalf("greeting = %q", got)
	}
	if got := document.Get("section", "long", ""); got != "first second" {
		t.Fatalf("long = %q", got)
	}
	var unchanged bytes.Buffer
	if err := document.Write(&unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.String() != input {
		t.Fatalf("round trip = %q, want %q", unchanged.String(), input)
	}
	document.Set("section", "base", "earth")
	var output bytes.Buffer
	if err := document.Write(&output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "# comment\n") || !strings.Contains(output.String(), "Base = earth\n") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestParseRejectsIndentedSyntaxAndCollapsesCycles(t *testing.T) {
	for _, input := range []string{" [section]\n", "[section]\n  orphan\n", " [# comment]\n"} {
		if _, err := Parse(strings.NewReader(input)); err == nil {
			t.Errorf("Parse(%q) succeeded", input)
		}
	}
	document, err := Parse(strings.NewReader("[section]\na = %(b)s\nb = %(a)s\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := document.Get("section", "a", "fallback"); got != "" {
		t.Fatalf("cycle = %q", got)
	}
}

func TestParsePreservesLineEndingsBOMAndFinalLine(t *testing.T) {
	input := "\ufeff[section] ignored\r\nvalue = first\r\n  second"
	document, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got := document.Get("section", "value", ""); got != "first second" {
		t.Fatalf("value = %q", got)
	}
	var output bytes.Buffer
	if err := document.Write(&output); err != nil {
		t.Fatal(err)
	}
	if output.String() != input {
		t.Fatalf("round trip = %q, want %q", output.String(), input)
	}
	if _, err := Parse(strings.NewReader("[section]\nbad name = value\n")); err == nil {
		t.Fatal("option name containing whitespace was accepted")
	}
}

func TestTypedGettersAndOverrides(t *testing.T) {
	config := New()
	config.File("config").Set("x", "bool", "on")
	config.File("config").Set("x", "int", "42")
	if got, err := config.GetBool("x", "bool", false); err != nil || !got {
		t.Fatalf("bool = %v, %v", got, err)
	}
	if got, err := config.GetInt("x", "int", 0); err != nil || got != 42 {
		t.Fatalf("int = %v, %v", got, err)
	}
	if err := config.ApplyOverride("config:x:int=7"); err != nil {
		t.Fatal(err)
	}
	if got, _ := config.GetInt("x", "int", 0); got != 7 {
		t.Fatalf("override = %d", got)
	}
}

func TestServerGroups(t *testing.T) {
	config := New()
	servers := config.File("servers")
	servers.Set("groups", "corp", "*.example.com, exact.test")
	servers.Set("groups", "pattern", "svn?.internal.[cl]an")
	servers.Set("corp", "http-timeout", "30")
	if got := config.ServerGroups("svn.example.com"); len(got) != 1 || got[0] != "corp" {
		t.Fatalf("groups = %v", got)
	}
	if got := config.ServerOption("svn.example.com", "http-timeout", "5"); got != "30" {
		t.Fatalf("timeout = %q", got)
	}
	if got := config.ServerGroups("example.com"); len(got) != 0 {
		t.Fatalf("apex groups = %v", got)
	}
	if got := config.ServerGroups("svn1.internal.lan"); len(got) != 1 || got[0] != "pattern" {
		t.Fatalf("glob groups = %v", got)
	}
}

func TestLoadConfigDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "config"), []byte("[miscellany]\nenable-auto-props = yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "servers"), []byte("[global]\nhttp-timeout = 12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	if value, _ := loaded.GetBool("miscellany", "enable-auto-props", false); !value {
		t.Fatal("config was not loaded")
	}
	if got := loaded.GetFile("servers", "global", "http-timeout", ""); got != "12" {
		t.Fatalf("server timeout = %q", got)
	}
}

func TestDefaultTemplatesParse(t *testing.T) {
	for _, value := range []string{DefaultConfig, DefaultServers} {
		if _, err := Parse(strings.NewReader(value)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParseReferenceTemplates(t *testing.T) {
	svnPath, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn executable is unavailable")
	}
	directory := t.TempDir()
	if output, err := exec.Command(svnPath, "--config-dir", directory, "help").CombinedOutput(); err != nil {
		t.Fatalf("generate reference templates: %v\n%s", err, output)
	}
	for _, name := range []string{"config", "servers"} {
		file, err := os.Open(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		_, parseErr := Parse(file)
		closeErr := file.Close()
		if parseErr != nil {
			t.Fatalf("parse reference %s: %v", name, parseErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("[section]\nname = value\n"))
	f.Add([]byte("# comment\n[one]\nx: y\\\nz\n"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = Parse(bytes.NewReader(data)) })
}
