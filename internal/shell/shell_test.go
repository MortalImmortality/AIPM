package shell

import (
	"aipm/internal/app"
	"aipm/internal/provider"
	"aipm/internal/store"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testApp(t *testing.T) *app.App {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &app.App{Store: store.New(filepath.Join(root, "registry")), Paths: map[provider.Tool]string{
		provider.Codex: filepath.Join(root, "codex", "config.toml"), provider.Claude: filepath.Join(root, "claude", "settings.json"),
	}}
}
func runScript(t *testing.T, a *app.App, input string, secrets ...string) string {
	t.Helper()
	var output bytes.Buffer
	next := 0
	err := Run(a, strings.NewReader(input), &output, func() (string, error) {
		if !strings.HasSuffix(output.String(), "Secret> ") {
			t.Fatal("secret callback without prompt")
		}
		if next == len(secrets) {
			t.Fatal("unexpected secret request")
		}
		value := secrets[next]
		next++
		return value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if next != len(secrets) {
		t.Fatalf("used %d/%d secrets", next, len(secrets))
	}
	for _, secret := range secrets {
		if secret != "" && secret != "/cancel" && strings.Contains(output.String(), secret) {
			t.Fatal("secret leaked")
		}
	}
	return output.String()
}
func saved(t *testing.T, a *app.App) []provider.Provider {
	t.Helper()
	v, err := a.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	return v.Registry.Providers
}
func seed(t *testing.T, a *app.App, tool provider.Tool) provider.Provider {
	t.Helper()
	p := provider.Provider{ID: provider.NewID(), Tool: tool, Name: "gateway", BaseURL: "https://gateway.example/v1", Model: "model", AuthMode: "token", Secret: "dummy-original-secret"}
	if tool == provider.Claude {
		p.Models = map[string]string{"ANTHROPIC_MODEL": "retained-mapping", "ANTHROPIC_DEFAULT_OPUS_MODEL": "opus-model"}
	}
	action, err := a.PrepareSave(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Execute(action); err != nil {
		t.Fatal(err)
	}
	return p
}
func assertStatus(t *testing.T, a *app.App, tool provider.Tool, kind string) {
	t.Helper()
	v, err := a.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if v.Errors[tool] != "" || v.Status[tool].Kind != kind {
		t.Fatalf("status %s, error %s", v.Status[tool].Kind, v.Errors[tool])
	}
}
func TestLifecycle(t *testing.T) {
	for i, tool := range provider.Tools {
		t.Run(string(tool), func(t *testing.T) {
			a := testApp(t)
			initial := []byte("# user comment\nmodel = \"official-model\"\napproval_policy = \"never\"\n")
			marker := "approval_policy"
			if tool == provider.Claude {
				initial = []byte(`{"model":"official-model","permissions":{"allow":["Read"]},"env":{"UNRELATED":"keep"}}`)
				marker = "UNRELATED"
			}
			path := a.Paths[tool]
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, initial, 0600); err != nil {
				t.Fatal(err)
			}
			aliases := ""
			if tool == provider.Claude {
				aliases = "opus\nsonnet\nhaiku\nfable\ndefault\n"
			}
			runScript(t, a, fmt.Sprintf("%d\n2\ngateway\nhttps://gateway.example/v1\nmodel\n\n%sy\n0\n0\n", i+1, aliases), "dummy-create-secret")
			p := saved(t, a)[0]
			if p.AuthMode != "token" {
				t.Fatal("default auth not token")
			}
			b, _ := os.ReadFile(path)
			if !bytes.Equal(b, initial) {
				t.Fatal("inactive save changed target")
			}
			output := runScript(t, a, fmt.Sprintf("%d\n1\n5\n1\nyes\n0\n0\n", i+1))
			assertStatus(t, a, tool, "managed")
			action, err := a.PrepareSwitch(tool, p.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range append(append([]string{path, a.Store.RegistryPath(), "确认执行 [y/N]> ", p.CredentialLabel()}, action.Change.Fields...), action.Change.Warnings...) {
				if !strings.Contains(output, want) {
					t.Fatalf("missing preview %q", want)
				}
			}
			output = runScript(t, a, fmt.Sprintf("%d\n4\n1\n0\n0\n", i+1))
			if !strings.Contains(output, "不能删除活跃") {
				t.Fatal("active deletion not protected")
			}
			blanks := ""
			if tool == provider.Claude {
				blanks = "\n\n\n\n\n"
			}
			runScript(t, a, fmt.Sprintf("%d\n3\n1\nrenamed\n\nnew-model\n\n%sy\n0\n0\n", i+1, blanks), "")
			edited := saved(t, a)[0]
			if edited.ID != p.ID || edited.Secret != p.Secret || edited.Tool != p.Tool {
				t.Fatal("edit lost identity/secret")
			}
			b, _ = os.ReadFile(path)
			if !strings.Contains(string(b), "new-model") || !strings.Contains(string(b), marker) {
				t.Fatal("active edit not applied/preserved")
			}
			runScript(t, a, fmt.Sprintf("%d\n6\ny\n0\n0\n", i+1))
			assertStatus(t, a, tool, "official")
			b, _ = os.ReadFile(path)
			if !strings.Contains(string(b), "official-model") || !strings.Contains(string(b), marker) || bytes.Contains(b, []byte(p.Secret)) {
				t.Fatal("restore failed")
			}
			runScript(t, a, fmt.Sprintf("%d\n4\n1\ny\n0\n0\n", i+1))
			if len(saved(t, a)) != 0 {
				t.Fatal("not deleted")
			}
		})
	}
}
func TestCancelEOFAndDefaultNo(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		secrets     []string
	}{
		{"EOF", "", nil}, {"partial", "1\n2\nunfinished", nil}, {"formCancel", "1\n2\n/cancel\n0\n0\n", nil},
		{"secretCancel", "1\n2\ngateway\nhttps://example.test\nmodel\n\n0\n0\n", []string{"/cancel"}},
		{"confirmEOF", "1\n2\ngateway\nhttps://example.test\nmodel\n\n", []string{"dummy-secret"}},
		{"defaultNo", "1\n2\ngateway\nhttps://example.test\nmodel\n\n\n0\n0\n", []string{"dummy-secret"}},
		{"confirmCancel", "1\n2\ngateway\nhttps://example.test\nmodel\n\n/cancel\n0\n0\n", []string{"dummy-secret"}},
		{"badChoice", "wrong\n1\nwrong\n0\n0\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testApp(t)
			runScript(t, a, tc.input, tc.secrets...)
			if len(saved(t, a)) != 0 {
				t.Fatal("unexpected save")
			}
			if _, err := os.Stat(a.Store.RegistryPath()); !os.IsNotExist(err) {
				t.Fatal("cancel wrote registry")
			}
		})
	}
}
func TestEditingClearAndMappings(t *testing.T) {
	a := testApp(t)
	p := seed(t, a, provider.Claude)
	runScript(t, a, "2\n3\n0\n3\n99\n1\n\n\n/clear\n\n/clear\n\n\n\n\ny\n0\n0\n", "")
	got := saved(t, a)[0]
	if got.Model != "" || got.Models["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "" || got.Models["ANTHROPIC_MODEL"] != "retained-mapping" || got.Secret != p.Secret || got.ID != p.ID {
		t.Fatal("clear/preservation failed")
	}
	var out bytes.Buffer
	_ = Run(a, strings.NewReader("2\n1\n0\n0\n"), &out, nil)
	if strings.Contains(out.String(), p.Secret) {
		t.Fatal("view leaked stored secret")
	}
}
func TestEnvAndValidationRetry(t *testing.T) {
	a := testApp(t)
	output := runScript(t, a, "1\n2\ngateway\nbad-url\nmodel\n2\nDUMMY_ENV\ny\n\nhttps://example.test\n\n\n\ny\n0\n0\n")
	got := saved(t, a)[0]
	if got.AuthMode != "env" || got.EnvKey != "DUMMY_ENV" || got.Secret != "" || !strings.Contains(output, "字段错误") {
		t.Fatal("env/retry failed")
	}
	b := testApp(t)
	runScript(t, b, "2\n2\ngateway\nhttps://example.test\n\n2\n\n\n\n\n\ny\n0\n0\n", "dummy-api-key")
	if saved(t, b)[0].AuthMode != "api_key" {
		t.Fatal("Claude auth choice failed")
	}
}

// A reader refusing large requests proves line input does not prefetch the callback's bytes.
type oneByteReader struct{ io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) {
	if len(p) != 1 {
		return 0, errors.New("read ahead")
	}
	return r.Reader.Read(p)
}
func TestInputBoundaryAndSecretErrors(t *testing.T) {
	a := testApp(t)
	var out bytes.Buffer
	input := oneByteReader{strings.NewReader("1\n2\ngateway\nhttps://example.test\nmodel\n\ndummy-secret\ny\n0\n0\n")}
	err := Run(a, input, &out, func() (string, error) {
		var value []byte
		for {
			var b [1]byte
			n, e := input.Read(b[:])
			if e != nil {
				return "", e
			}
			if n == 1 && b[0] == '\n' {
				return string(value), nil
			}
			value = append(value, b[0])
		}
	})
	if err != nil || len(saved(t, a)) != 1 || strings.Contains(out.String(), "dummy-secret") {
		t.Fatal("input/secret boundary failed")
	}
	for _, e := range []error{io.EOF, fmt.Errorf("wrapped: %w", errInterrupted), errTerminalRestore, errors.New("dummy-secret-error")} {
		a = testApp(t)
		out.Reset()
		err = Run(a, strings.NewReader("1\n2\ngateway\nhttps://example.test\nmodel\n\n0\n0\n"), &out, func() (string, error) { return "", e })
		if errors.Is(e, errInterrupted) && !errors.Is(err, errInterrupted) {
			t.Fatal("interrupt swallowed")
		}
		if errors.Is(e, errTerminalRestore) && !errors.Is(err, errTerminalRestore) {
			t.Fatal("terminal restore error swallowed")
		}
		if !errors.Is(e, errInterrupted) && !errors.Is(e, errTerminalRestore) && err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "dummy-secret-error") || len(saved(t, a)) != 0 {
			t.Fatal("callback error leaked/saved")
		}
	}
	s := &session{in: strings.NewReader(strings.Repeat("x", 8192) + "\n"), out: io.Discard}
	if value, err := s.line(""); err != nil || len(value) != 8192 {
		t.Fatal("8192 rejected")
	}
	s.in = strings.NewReader(strings.Repeat("x", 8193) + "\n0\n")
	if _, err := s.line(""); err == nil {
		t.Fatal("oversized line accepted")
	}
	if value, err := s.line(""); err != nil || value != "0" {
		t.Fatal("oversized remainder became input")
	}
}
