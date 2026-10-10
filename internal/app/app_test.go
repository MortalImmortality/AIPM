package app

import (
	"aipm/internal/config"
	"aipm/internal/provider"
	"aipm/internal/store"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedApp(t *testing.T) *App {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &App{Store: store.New(filepath.Join(root, "registry")), Paths: map[provider.Tool]string{provider.Codex: filepath.Join(root, "codex", "config.toml"), provider.Claude: filepath.Join(root, "claude", "settings.json")}}
}
func testProvider(tool provider.Tool) provider.Provider {
	return provider.Provider{ID: provider.NewID(), Tool: tool, Name: "Test gateway", BaseURL: "https://gateway.example/v1", Model: "test-model", AuthMode: "token", Secret: "test-secret-not-personal"}
}
func writeTarget(t *testing.T, a *App, tool provider.Tool, b []byte) {
	t.Helper()
	path := a.Paths[tool]
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func execute(t *testing.T, a *App, action *Action, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Execute(action); err != nil {
		t.Fatal(err)
	}
}
func saveProvider(t *testing.T, a *App, p provider.Provider) {
	t.Helper()
	action, err := a.PrepareSave(p)
	execute(t, a, action, err)
}
func switchProvider(t *testing.T, a *App, tool provider.Tool, id string) {
	t.Helper()
	action, err := a.PrepareSwitch(tool, id)
	execute(t, a, action, err)
}
func assertStatus(t *testing.T, a *App, tool provider.Tool, kind, id string) {
	t.Helper()
	v, err := a.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if v.Errors[tool] != "" {
		t.Fatal(v.Errors[tool])
	}
	s := v.Status[tool]
	if s.Kind != kind || s.ActiveID != id {
		t.Fatalf("status = %+v, want %s %s", s, kind, id)
	}
}
func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProviderLifecycle(t *testing.T) {
	for _, tool := range provider.Tools {
		t.Run(string(tool), func(t *testing.T) {
			a := isolatedApp(t)
			initial := []byte("# keep user comment\nmodel = \"official-model\"\napproval_policy = \"never\"\n[projects.\"/test/project\"]\ntrust_level = \"trusted\"\n")
			preserved := []string{"# keep user comment", "approval_policy = \"never\"", "trust_level = \"trusted\""}
			if tool == provider.Claude {
				initial = []byte(`{"model":"official-model","permissions":{"allow":["Read"]},"env":{"UNRELATED":"keep"},"custom":{"count":9007199254740993}}`)
				preserved = []string{`"UNRELATED": "keep"`, `"Read"`, `9007199254740993`}
			}
			writeTarget(t, a, tool, initial)
			assertStatus(t, a, tool, "official", "")
			p := testProvider(tool)
			saveProvider(t, a, p)
			if !bytes.Equal(fileBytes(t, a.Paths[tool]), initial) {
				t.Fatal("saving inactive provider modified config")
			}
			p.Name = "Renamed gateway"
			saveProvider(t, a, p)
			switchProvider(t, a, tool, p.ID)
			assertStatus(t, a, tool, "managed", p.ID)
			if _, err := a.PrepareDelete(p.ID); err == nil {
				t.Fatal("active provider deletion allowed")
			}
			p.Secret = "edited-secret-not-personal"
			p.Model = "edited-model"
			saveProvider(t, a, p)
			assertStatus(t, a, tool, "managed", p.ID)
			b := fileBytes(t, a.Paths[tool])
			if !bytes.Contains(b, []byte(p.Secret)) || !bytes.Contains(b, []byte(p.Model)) {
				t.Fatal("active edit was not reapplied")
			}
			for _, s := range preserved {
				if !strings.Contains(string(b), s) {
					t.Fatalf("unrelated config lost: %q", s)
				}
			}
			switchProvider(t, a, tool, "")
			assertStatus(t, a, tool, "official", "")
			b = fileBytes(t, a.Paths[tool])
			if bytes.Contains(b, []byte(p.Secret)) {
				t.Fatal("official config retained managed secret")
			}
			if !bytes.Contains(b, []byte("official-model")) {
				t.Fatal("official model not restored")
			}
			for _, s := range preserved {
				if !strings.Contains(string(b), s) {
					t.Fatalf("official restoration lost unrelated config: %q", s)
				}
			}
			action, err := a.PrepareDelete(p.ID)
			execute(t, a, action, err)
			v, err := a.Refresh()
			if err != nil {
				t.Fatal(err)
			}
			if len(v.Registry.Providers) != 0 {
				t.Fatal("provider was not deleted")
			}
			if !bytes.Equal(b, fileBytes(t, a.Paths[tool])) {
				t.Fatal("inactive deletion modified target")
			}
		})
	}
}

func TestCodexLegacyAndNamedActiveRename(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, snapshot := range []bool{false, true} {
			a := isolatedApp(t)
			p := testProvider(provider.Codex)
			saveProvider(t, a, p)
			key := p.Key()
			if legacy {
				key = "aipm_" + p.ID
			}
			b := []byte(fmt.Sprintf("# preserved fixture\nmodel_provider = %q\nmodel = %q\n[model_providers.%q]\nname = %q\nbase_url = %q\nwire_api = 'responses'\nrequires_openai_auth = false\nexperimental_bearer_token = %q\n", key, p.Model, key, p.Name, p.BaseURL, p.Secret))
			writeTarget(t, a, provider.Codex, b)
			if snapshot {
				r, err := a.Store.Load()
				if err != nil {
					t.Fatal(err)
				}
				n := clone(r)
				table := map[string]any{"name": p.Name, "base_url": p.BaseURL, "wire_api": "responses", "requires_openai_auth": false, "experimental_bearer_token": p.Secret}
				hashed, _ := json.Marshal(map[string]any{"key": key, "table": table})
				s := map[string]any{"_officialCaptured": true, "model": "original", "_managedModels": map[string]any{"model": p.Model}, "_managedProvider": fmt.Sprintf("%x", sha256.Sum256(hashed))}
				if !legacy {
					s["_managedProviderKey"] = key
				}
				n.Snapshots[a.snapshotKey(provider.Codex)] = s
				if err := a.Store.Save(r, n); err != nil {
					t.Fatal(err)
				}
			}
			assertStatus(t, a, provider.Codex, "managed", p.ID)
			if _, err := a.PrepareDelete(p.ID); err == nil {
				t.Fatal("legacy/named active deletion allowed")
			}
			before := fileBytes(t, a.Paths[provider.Codex])
			registry := fileBytes(t, a.Store.RegistryPath())
			p.Name = "Renamed.中文 gateway"
			p.Secret = "renamed-dummy-secret"
			action, err := a.PrepareSave(p)
			if err != nil {
				t.Fatal(err)
			}
			if action.Kind != "apply" || !bytes.Equal(before, fileBytes(t, a.Paths[provider.Codex])) || !bytes.Equal(registry, fileBytes(t, a.Store.RegistryPath())) {
				t.Fatal("rename preparation wrote or failed to apply")
			}
			execute(t, a, action, nil)
			assertStatus(t, a, provider.Codex, "managed", p.ID)
			b = fileBytes(t, a.Paths[provider.Codex])
			if bytes.Contains(b, []byte(key)) || !bytes.Contains(b, []byte(p.Key())) || !bytes.HasPrefix(b, []byte("# preserved fixture\n")) {
				t.Fatal("active rename failed to remove old key or preserve unrelated bytes")
			}
			switchProvider(t, a, provider.Codex, p.ID)
			if !bytes.Equal(b, fileBytes(t, a.Paths[provider.Codex])) {
				t.Fatal("named switch not idempotent")
			}
			switchProvider(t, a, provider.Codex, "")
			if bytes.Contains(fileBytes(t, a.Paths[provider.Codex]), []byte(p.Key())) {
				t.Fatal("official cleanup retained named table")
			}
			r, err := a.Store.Load()
			if err != nil || len(r.Providers) != 1 || r.Providers[0].ID != p.ID {
				t.Fatal("migration changed internal ID", err)
			}
		}
	}
}

func TestCodexNameCollisionAndMigrationCAS(t *testing.T) {
	a := isolatedApp(t)
	p := testProvider(provider.Codex)
	saveProvider(t, a, p)
	foreign := []byte("# foreign must remain\n[model_providers.\"anim_Test gateway\"]\nname='external'\n")
	writeTarget(t, a, provider.Codex, foreign)
	registry := fileBytes(t, a.Store.RegistryPath())
	if _, err := a.PrepareSwitch(provider.Codex, p.ID); err == nil {
		t.Fatal("external target collision accepted")
	}
	if !bytes.Equal(foreign, fileBytes(t, a.Paths[provider.Codex])) || !bytes.Equal(registry, fileBytes(t, a.Store.RegistryPath())) {
		t.Fatal("collision modified persisted state")
	}
	writeTarget(t, a, provider.Codex, nil)
	switchProvider(t, a, provider.Codex, p.ID)
	foreign = append(fileBytes(t, a.Paths[provider.Codex]), []byte("\n[model_providers.anim_Destination]\nname='external'\n")...)
	writeTarget(t, a, provider.Codex, foreign)
	registry = fileBytes(t, a.Store.RegistryPath())
	p.Name = "Destination"
	if _, err := a.PrepareSave(p); err == nil {
		t.Fatal("active rename collided with external table")
	}
	if !bytes.Equal(foreign, fileBytes(t, a.Paths[provider.Codex])) || !bytes.Equal(registry, fileBytes(t, a.Store.RegistryPath())) {
		t.Fatal("failed rename modified persisted state")
	}
	p.Name = "Safe destination"
	action, err := a.PrepareSave(p)
	if err != nil {
		t.Fatal(err)
	}
	changed := append(append([]byte{}, foreign...), []byte("\n# concurrent edit\n")...)
	writeTarget(t, a, provider.Codex, changed)
	if _, err := a.Execute(action); err == nil {
		t.Fatal("concurrent change overwritten")
	}
	if !bytes.Equal(changed, fileBytes(t, a.Paths[provider.Codex])) || !bytes.Equal(registry, fileBytes(t, a.Store.RegistryPath())) {
		t.Fatal("failed CAS changed config/registry")
	}
	// Once retried explicitly, registry ID remains stable and the foreign table survives.
	saveProvider(t, a, p)
	assertStatus(t, a, provider.Codex, "managed", p.ID)
	b := fileBytes(t, a.Paths[provider.Codex])
	if !bytes.Contains(b, []byte("[model_providers.anim_Destination]")) || !bytes.Contains(b, []byte("# concurrent edit")) {
		t.Fatal("retry discarded external data")
	}
	s, err := config.Inspect(provider.Codex, b, []provider.Provider{p})
	if err != nil || s.ActiveID != p.ID {
		t.Fatal("retry did not apply", err)
	}
}

func TestPrepareDoesNotWrite(t *testing.T) {
	for _, tool := range provider.Tools {
		t.Run(string(tool), func(t *testing.T) {
			a := isolatedApp(t)
			p := testProvider(tool)
			action, err := a.PrepareSave(p)
			if err != nil {
				t.Fatal(err)
			}
			if action == nil {
				t.Fatal("missing save action")
			}
			if _, err := os.Stat(a.Store.RegistryPath()); !os.IsNotExist(err) {
				t.Fatalf("prepare created registry: %v", err)
			}
			saveProvider(t, a, p)
			writeTarget(t, a, tool, []byte{})
			before := fileBytes(t, a.Paths[tool])
			registry := fileBytes(t, a.Store.RegistryPath())
			if _, err := a.PrepareSwitch(tool, p.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := a.PrepareDelete(p.ID); err != nil {
				t.Fatal(err)
			}
			p.Name = "Cancelled edit"
			if _, err := a.PrepareSave(p); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, fileBytes(t, a.Paths[tool])) || !bytes.Equal(registry, fileBytes(t, a.Store.RegistryPath())) {
				t.Fatal("preparation modified persisted state")
			}
			if a.Store.Pending() {
				t.Fatal("preparation left pending transaction")
			}
		})
	}
}

func TestDuplicateNameAndObservedDrift(t *testing.T) {
	for _, tool := range provider.Tools {
		t.Run(string(tool), func(t *testing.T) {
			a := isolatedApp(t)
			p := testProvider(tool)
			saveProvider(t, a, p)
			duplicate := p
			duplicate.ID = provider.NewID()
			duplicate.Name = "  TEST GATEWAY  "
			if _, err := a.PrepareSave(duplicate); err == nil {
				t.Fatal("duplicate name accepted")
			}
			other := testProvider(provider.Claude)
			if tool == provider.Claude {
				other.Tool = provider.Codex
			}
			saveProvider(t, a, other)
			switchProvider(t, a, tool, p.ID)
			b := fileBytes(t, a.Paths[tool])
			b = bytes.ReplaceAll(b, []byte(p.Secret), []byte("externally-edited-secret"))
			writeTarget(t, a, tool, b)
			assertStatus(t, a, tool, "external", "")
			v, err := a.Refresh()
			if err != nil {
				t.Fatal(err)
			}
			if v.Status[tool].Label != "外部配置" {
				t.Fatalf("drift label = %q", v.Status[tool].Label)
			}
			if strings.Contains(v.Status[tool].Label, "externally-edited-secret") {
				t.Fatal("status leaked credential")
			}
			if _, err := a.PrepareDelete(p.ID); err == nil {
				t.Fatal("delete allowed under drift")
			}
			p.Name = "Edit after drift"
			if _, err := a.PrepareSave(p); err == nil {
				t.Fatal("edit allowed under drift")
			}
		})
	}
}
