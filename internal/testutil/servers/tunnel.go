package servers

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/internal/testutil"
)

func TunnelScript(t testing.TB, repositoryRoot string) string {
	t.Helper()
	svnserve := testutil.RequireTool(t, "svnserve", "GOSVN_SVNSERVE")
	repositoryRoot = absoluteDirectory(t, repositoryRoot)

	if runtime.GOOS == "windows" {
		path := filepath.Join(t.TempDir(), "svn-tunnel.cmd")
		content := fmt.Sprintf("@echo off\r\n\"%s\" -t -r \"%s\"\r\n", escapeBatch(svnserve), escapeBatch(repositoryRoot))
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatalf("write tunnel script: %v", err)
		}
		return path
	}

	path := filepath.Join(t.TempDir(), "svn-tunnel")
	content := fmt.Sprintf("#!/bin/sh\nexec %s -t -r %s\n", shellQuote(svnserve), shellQuote(repositoryRoot))
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write tunnel script: %v", err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func escapeBatch(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}
