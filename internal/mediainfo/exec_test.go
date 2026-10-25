package mediainfo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeBinary(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("script shell")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "mediainfo")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExecStripsPath(t *testing.T) {
	bin := fakeBinary(t, `printf 'General\nComplete name                            : %s\nFormat : Matroska\n\nVideo\nWidth : 1920 pixels\n' "$2"`)
	e := &Exec{Path: bin}
	out, err := e.Analyze(context.Background(), Request{Source: "local", Path: "/data/Film.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "/data/Film.mkv") || strings.Contains(out, "Complete name") {
		t.Fatalf("chemin local dans la sortie : %q", out)
	}
	if out != "General\nFormat : Matroska\n\nVideo\nWidth : 1920 pixels" {
		t.Fatalf("sortie : %q", out)
	}
}

func TestExecFailure(t *testing.T) {
	bin := fakeBinary(t, `echo "boom" >&2; exit 2`)
	e := &Exec{Path: bin}
	if _, err := e.Analyze(context.Background(), Request{Source: "local", Path: "/x"}); err == nil {
		t.Fatal("erreur attendue")
	}
}

func TestExecRemoteRefused(t *testing.T) {
	e := &Exec{Path: "/bin/true"}
	if _, err := e.Analyze(context.Background(), Request{Source: "ssh", Path: "/x"}); err == nil {
		t.Fatal("source distante : erreur attendue")
	}
}

func TestFind(t *testing.T) {
	t.Setenv("D3X_MEDIAINFO", "")
	t.Setenv("PATH", t.TempDir())
	if Find() != nil {
		t.Fatal("aucun binaire : nil attendu")
	}
	bin := fakeBinary(t, "exit 0")
	t.Setenv("D3X_MEDIAINFO", bin)
	if e := Find(); e == nil || e.Path != bin {
		t.Fatalf("D3X_MEDIAINFO ignoré : %+v", e)
	}
}
