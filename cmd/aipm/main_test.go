package main

import (
	"aipm/internal/app"
	"aipm/internal/provider"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateCLI(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIPM_HOME", filepath.Join(root, "registry"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	return root
}
func TestHelpAndVersion(t *testing.T) {
	root := isolateCLI(t)
	for _, arg := range []string{"--help", "-h", "help", "--version", "version"} {
		t.Run(arg, func(t *testing.T) {
			var out bytes.Buffer
			if err := run([]string{arg}, &out); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(arg, "version") {
				if out.String() != "aipm "+version+"\n" {
					t.Fatalf("version = %q", out.String())
				}
			} else if !strings.Contains(out.String(), "aipm list") || !strings.Contains(out.String(), "AIPM_HOME") {
				t.Fatal("incomplete help")
			}
		})
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("help/version created configuration")
	}
}
func TestReadonlyMissingDirectoriesDoNotWrite(t *testing.T) {
	root := isolateCLI(t)
	for _, arg := range []string{"list", "status"} {
		var out bytes.Buffer
		if err := run([]string{arg}, &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "codex:") || !strings.Contains(out.String(), "claude:") {
			t.Fatalf("missing tool output: %q", out.String())
		}
		if !strings.Contains(out.String(), "Official") {
			t.Fatal("missing official status")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("readonly command created files: %v", entries)
	}
}
func TestListAndStatusNeverExposeSecrets(t *testing.T) {
	isolateCLI(t)
	a, err := app.New()
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{"codex-test-secret-private", "claude-test-secret-private"}
	for i, tool := range provider.Tools {
		p := provider.Provider{ID: provider.NewID(), Tool: tool, Name: "Saved " + string(tool), BaseURL: "https://gateway.example/v1", Model: "test-model", AuthMode: "token", Secret: secrets[i]}
		action, err := a.PrepareSave(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Execute(action); err != nil {
			t.Fatal(err)
		}
		action, err = a.PrepareSwitch(tool, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Execute(action); err != nil {
			t.Fatal(err)
		}
	}
	for _, arg := range []string{"list", "status"} {
		var out bytes.Buffer
		if err := run([]string{arg}, &out); err != nil {
			t.Fatal(err)
		}
		s := out.String()
		for _, secret := range secrets {
			if strings.Contains(s, secret) {
				t.Fatalf("%s exposed secret", arg)
			}
		}
		for _, tool := range provider.Tools {
			if !strings.Contains(s, "Saved "+string(tool)) {
				t.Fatalf("%s omitted saved/active provider", arg)
			}
		}
		if arg == "list" && !strings.Contains(s, "••") {
			t.Fatal("list missing masked credential")
		}
		if arg == "status" && !strings.Contains(s, "[managed]") {
			t.Fatal("status missing observed kind")
		}
	}
}
func TestInvalidCommandsAndNonTTY(t *testing.T) {
	root := isolateCLI(t)
	for _, args := range [][]string{{"unknown"}, {"list", "extra"}} {
		var out bytes.Buffer
		if err := run(args, &out); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
	// Use a regular file rather than depending on the test runner's terminal.
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	old := os.Stdin
	os.Stdin = stdin
	defer func() { os.Stdin = old }()
	var out bytes.Buffer
	err = run(nil, &out)
	if err == nil || !strings.Contains(err.Error(), "交互终端") {
		t.Fatalf("non-TTY error = %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("invalid/non-TTY invocation created configuration")
	}
}
