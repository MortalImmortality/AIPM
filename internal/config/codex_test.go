package config

import (
	"aipm/internal/provider"
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func testProvider(tool provider.Tool) provider.Provider {
	return provider.Provider{ID: strings.Repeat("a", 32), Tool: tool, Name: "Gateway", BaseURL: "https://gateway.example/v1", Model: "gateway-model", AuthMode: "token", Secret: "synthetic-test-secret"}
}
func TestCodexRoundTrip(t *testing.T) {
	p := testProvider(provider.Codex)
	original := []byte("# keep comment\r\nmodel = 'official-model' # keep inline\r\nreasoning = 'high'\r\n[projects.\"/tmp/project\"]\r\ntrust_level = 'trusted'\r\n[profiles.work]\r\nmodel = 'profile-model'\r\n")
	c, err := planCodex(original, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := inspectCodex(c.Bytes, []provider.Provider{p})
	if err != nil || s.Kind != "managed" || s.ActiveID != p.ID {
		t.Fatalf("status %#v %v", s, err)
	}
	if !bytes.Contains(c.Bytes, []byte("# keep comment\r\n")) || !bytes.Contains(c.Bytes, []byte("trust_level = 'trusted'")) || len(c.Warnings) == 0 {
		t.Fatal("unrelated TOML changed")
	}
	back, err := planCodex(c.Bytes, nil, c.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseCodex(back.Bytes)
	if err != nil || m["model"] != "official-model" || m["model_provider"] != "openai" {
		t.Fatal("restore", err)
	}
	if codexTable(m, p.Key()) != nil {
		t.Fatal("owned table remains")
	}
	if s, _ := inspectCodex(back.Bytes, nil); s.Kind != "official" {
		t.Fatal(s)
	}
}
func TestCodexInferenceAndSafety(t *testing.T) {
	p := testProvider(provider.Codex)
	c, err := planCodex(nil, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Secret = "different"
	if s, _ := inspectCodex(c.Bytes, []provider.Provider{p}); s.Kind != "external" {
		t.Fatal(s)
	}
	for _, b := range []string{"model = 1", "model_providers = []", "[model_providers.x]\nenv_key = 1", "model = 'synthetic-test-secret\n"} {
		_, err := inspectCodex([]byte(b), nil)
		if err == nil || strings.Contains(err.Error(), "synthetic-test-secret") {
			t.Fatal("unsafe parse error", err)
		}
	}
	b := []byte("[model_providers.openai]\nbase_url='https://external.example'\n")
	if s, _ := inspectCodex(b, nil); s.Kind != "external" {
		t.Fatal(s)
	}
	if _, err := planCodex(b, nil, nil); err == nil {
		t.Fatal("built-in override accepted")
	}
}
func TestCodexInlineAndAbsent(t *testing.T) {
	p := testProvider(provider.Codex)
	p.AuthMode = "env"
	p.EnvKey = "TEST_GATEWAY_KEY"
	p.Secret = ""
	b := []byte("unrelated = { nested = [1, 2], note = '''keep\nmultiline''' }\nmodel_providers = { foreign = { name = 'foreign', base_url = 'https://foreign.example' } }\n")
	c, err := planCodex(b, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := inspectCodex(c.Bytes, []provider.Provider{p}); s.Kind != "managed" {
		t.Fatal(s)
	}
	back, err := planCodex(c.Bytes, nil, c.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := parseCodex(back.Bytes)
	if _, ok := m["model"]; ok {
		t.Fatal("absent model restored incorrectly")
	}
	if codexTable(m, "foreign") == nil {
		t.Fatal("foreign lost")
	}
}
func TestCodexExternalDefinitions(t *testing.T) {
	p := testProvider(provider.Codex)
	b := []byte("model_provider='foreign'\nmodel='external'\n[model_providers.foreign]\nbase_url='https://foreign.example'\n[model_providers.aipm_not_generated]\nname='keep'\n")
	c, err := planCodex(b, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := parseCodex(c.Bytes)
	if codexTable(m, "foreign") == nil || codexTable(m, "aipm_not_generated") == nil {
		t.Fatal("external table removed")
	}
	if c.Snapshot["_officialCaptured"] != false || c.Snapshot["model"] != nil {
		t.Fatal("external model captured")
	}
	p.Secret = "changed"
	if _, err := planCodex(c.Bytes, &p, nil); err == nil {
		t.Fatal("foreign collision overwritten")
	}
	back, err := planCodex(c.Bytes, nil, c.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	m, _ = parseCodex(back.Bytes)
	if m["model"] != nil || codexTable(m, "aipm_not_generated") == nil {
		t.Fatal("external restore unsafe")
	}
	s, err := Inspect(provider.Codex, back.Bytes, nil)
	if err != nil || s.Fingerprint == "" {
		t.Fatal("wrapper fingerprint missing")
	}
}
func TestCodexActiveEditAndOwnedCleanup(t *testing.T) {
	p := testProvider(provider.Codex)
	c, err := planCodex([]byte("model='original'\n"), &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Model = "edited-model"
	p.Secret = "edited-secret"
	edited, err := planCodex(c.Bytes, &p, c.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := inspectCodex(edited.Bytes, []provider.Provider{p}); s.Kind != "managed" {
		t.Fatal(s)
	}
	if strings.Contains(edited.Snapshot["_managedProvider"].(string), p.Secret) {
		t.Fatal("metadata leaks secret")
	}
	tampered := bytes.ReplaceAll(edited.Bytes, []byte("edited-secret"), []byte("foreign-secret"))
	if _, err := planCodex(tampered, &p, edited.Snapshot); err == nil {
		t.Fatal("external table edited")
	}
	q := p
	q.ID = strings.Repeat("b", 32)
	q.Name = "Second gateway"
	next, err := planCodex(edited.Bytes, &q, edited.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := parseCodex(next.Bytes)
	if codexTable(m, p.Key()) != nil {
		t.Fatal("owned previous table retained")
	}
	back, err := planCodex(next.Bytes, nil, next.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	m, _ = parseCodex(back.Bytes)
	if m["model"] != "original" {
		t.Fatal("original lost across active edit")
	}
}
func TestCodexHeaderWithoutOriginalModel(t *testing.T) {
	p := testProvider(provider.Codex)
	for _, b := range []string{"# header\nmodel='thirdparty'\nmodel_provider='foreign'\n", "# header\n\n"} {
		c, err := planCodex([]byte(b), &p, nil)
		if err != nil {
			t.Fatal(err)
		}
		back, err := planCodex(c.Bytes, nil, c.Snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.HasPrefix(back.Bytes, []byte("# header\n")) {
			t.Fatal("file header lost")
		}
	}
}
func TestOfficialPreferenceCycles(t *testing.T) {
	for _, tool := range provider.Tools {
		t.Run(string(tool), func(t *testing.T) {
			p := testProvider(tool)
			initial := []byte("model='official-A'\n")
			if tool == provider.Claude {
				initial = []byte(`{"model":"official-A"}`)
			}
			c, err := Plan(tool, initial, &p, nil)
			if err != nil {
				t.Fatal(err)
			}
			back, err := Plan(tool, c.Bytes, nil, c.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			edited := bytes.ReplaceAll(back.Bytes, []byte("official-A"), []byte("official-B"))
			same, err := Plan(tool, edited, nil, back.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(same.Bytes, []byte("official-B")) {
				t.Fatal("official idempotence lost preference")
			}
			next, err := Plan(tool, edited, &p, back.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := Plan(tool, next.Bytes, nil, next.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(restored.Bytes, []byte("official-B")) {
				t.Fatal("cycle restored stale preference")
			}
		})
	}
}

// legacyCodexFixture reproduces the old writer and persisted snapshot shape.
func legacyCodexFixture(t *testing.T, p provider.Provider) ([]byte, Snapshot) {
	t.Helper()
	key := "aipm_" + p.ID
	b, err := toml.Marshal(map[string]any{"model_provider": key, "model": p.Model, "model_providers": map[string]any{key: codexDefinition(p)}})
	if err != nil {
		t.Fatal(err)
	}
	return b, Snapshot{"_officialCaptured": true, "model": "original", "_managedModels": map[string]any{"model": p.Model}, "_managedProvider": codexProviderHash(key, codexDefinition(p))}
}

func TestCodexNamedKeysAreLiteral(t *testing.T) {
	for _, name := range []string{"Gateway", "my gateway", "中文.网关", `dot.quote"[bracket]\\path`, "  Exact Name  "} {
		t.Run(name, func(t *testing.T) {
			p := testProvider(provider.Codex)
			p.Name = name
			c, err := planCodex([]byte("# keep\nreasoning = 'high'\n"), &p, nil)
			if err != nil {
				t.Fatal(err)
			}
			m, err := parseCodex(c.Bytes)
			if err != nil || text(m, "model_provider") != "anim_"+name || !reflectCodexDefinition(m, p) {
				t.Fatalf("literal key not preserved: %v", err)
			}
			tables := m["model_providers"].(map[string]any)
			if len(tables) != 1 || !bytes.HasPrefix(c.Bytes, []byte("# keep\n")) {
				t.Fatal("name interpreted as path or header changed")
			}
			again, err := planCodex(c.Bytes, &p, c.Snapshot)
			if err != nil || !bytes.Equal(c.Bytes, again.Bytes) {
				t.Fatalf("switch not idempotent: %v", err)
			}
			back, err := planCodex(c.Bytes, nil, c.Snapshot)
			if err != nil {
				t.Fatal(err)
			}
			m, _ = parseCodex(back.Bytes)
			if codexTable(m, p.Key()) != nil || m["reasoning"] != "high" {
				t.Fatal("literal-key cleanup failed")
			}
		})
	}
}
func reflectCodexDefinition(m map[string]any, p provider.Provider) bool {
	return reflect.DeepEqual(codexTable(m, p.Key()), codexDefinition(p))
}

func TestCodexLegacyMigrationAndRename(t *testing.T) {
	for _, rename := range []bool{false, true} {
		for _, withSnapshot := range []bool{false, true} {
			p := testProvider(provider.Codex)
			b, snapshot := legacyCodexFixture(t, p)
			if s, err := inspectCodex(b, []provider.Provider{p}); err != nil || s.ActiveID != p.ID {
				t.Fatal("legacy provider not recognized", err)
			}
			if !withSnapshot {
				snapshot = nil
			}
			old := p
			if rename {
				p.Name = "Renamed.网关"
				p.Secret = "new-dummy-secret"
			}
			c, err := planCodex(b, &p, snapshot, old)
			if err != nil {
				t.Fatal(err)
			}
			m, _ := parseCodex(c.Bytes)
			if codexTable(m, "aipm_"+p.ID) != nil || text(m, "model_provider") != p.Key() || !reflectCodexDefinition(m, p) {
				t.Fatal("migration failed")
			}
			// Serialize metadata exactly as the registry does before a named rename.
			data, _ := json.Marshal(c.Snapshot)
			var persisted Snapshot
			if err := json.Unmarshal(data, &persisted); err != nil {
				t.Fatal(err)
			}
			oldKey := p.Key()
			p.Name = "Another gateway"
			renamed, err := planCodex(c.Bytes, &p, persisted)
			if err != nil {
				t.Fatal(err)
			}
			m, _ = parseCodex(renamed.Bytes)
			if codexTable(m, oldKey) != nil || !reflectCodexDefinition(m, p) {
				t.Fatal("snapshot ownership did not survive named rename")
			}
		}
	}
}

func TestCodexLegacySnapshotOnlySwitch(t *testing.T) {
	p := testProvider(provider.Codex)
	b, snapshot := legacyCodexFixture(t, p)
	// A legacy registry snapshot has no explicit table key. It remains sufficient
	// proof when the table and active legacy key are unchanged.
	for _, target := range []*provider.Provider{&p, nil} {
		c, err := planCodex(b, target, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := parseCodex(c.Bytes)
		if codexTable(m, "aipm_"+p.ID) != nil {
			t.Fatal("trusted legacy table not removed")
		}
		if target == nil {
			if m["model"] != "original" || m["model_provider"] != "openai" {
				t.Fatal("old snapshot official restoration failed")
			}
		} else if !reflectCodexDefinition(m, p) || m["model_provider"] != p.Key() {
			t.Fatal("same-provider switch did not migrate")
		}
	}
}

func TestCodexCleanupRequiresOwnership(t *testing.T) {
	p := testProvider(provider.Codex)
	q := p
	q.ID = strings.Repeat("b", 32)
	q.Name = "Inactive gateway"
	legacy := "aipm_" + p.ID
	unknownLegacy := "aipm_" + strings.Repeat("c", 32)
	for _, active := range []string{legacy, p.Key(), "foreign"} {
		tables := map[string]any{legacy: codexDefinition(p), p.Key(): codexDefinition(p), q.Key(): codexDefinition(q), "anim_external": map[string]any{"name": "keep"}, unknownLegacy: map[string]any{"name": "keep"}, "foreign": map[string]any{"name": "keep"}}
		b, _ := toml.Marshal(map[string]any{"model_provider": active, "model": p.Model, "model_providers": tables})
		back, err := planCodex(b, nil, nil, p, q)
		if err != nil {
			t.Fatal(err)
		}
		m, _ := parseCodex(back.Bytes)
		for _, key := range []string{legacy, p.Key(), q.Key()} {
			if codexTable(m, key) != nil {
				t.Fatal("registered table retained", key)
			}
		}
		for _, key := range []string{"anim_external", unknownLegacy, "foreign"} {
			if codexTable(m, key) == nil {
				t.Fatal("external table removed", key)
			}
		}
	}
	b, snapshot := legacyCodexFixture(t, p)
	b = bytes.ReplaceAll(b, []byte(p.Secret), []byte("external-dummy-secret"))
	back, err := planCodex(b, nil, snapshot, p)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := parseCodex(back.Bytes)
	if codexTable(m, legacy) == nil {
		t.Fatal("modified legacy table removed")
	}
}

func TestCodexRenameCollisionAndTamperedOldTable(t *testing.T) {
	p := testProvider(provider.Codex)
	c, err := planCodex(nil, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := p.Key()
	p.Name = "Destination"
	foreign := "\n# external table\n[model_providers.anim_Destination]\nname = 'external'\nbase_url = 'https://external.example'\n"
	b := append(append([]byte{}, c.Bytes...), []byte(foreign)...)
	if _, err := planCodex(b, &p, c.Snapshot); err == nil || strings.Contains(err.Error(), p.Secret) {
		t.Fatal("rename collision not rejected safely", err)
	}
	modified := bytes.ReplaceAll(c.Bytes, []byte(p.Secret), []byte("externally-modified"))
	renamed, err := planCodex(modified, &p, c.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := parseCodex(renamed.Bytes)
	if codexTable(m, oldKey) == nil {
		t.Fatal("untrusted old named table removed")
	}
	back, err := planCodex(renamed.Bytes, nil, renamed.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	m, _ = parseCodex(back.Bytes)
	if codexTable(m, oldKey) == nil || codexTable(m, p.Key()) != nil {
		t.Fatal("official cleanup ignored ownership")
	}
}

func TestCodexModelConflict(t *testing.T) {
	p := testProvider(provider.Codex)
	c, err := planCodex([]byte("model='official'\n"), &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.ReplaceAll(c.Bytes, []byte("gateway-model"), []byte("external-model"))
	if _, err := planCodex(changed, nil, c.Snapshot); err == nil {
		t.Fatal("external model overwritten")
	}
	if _, err := planCodex(append(c.Bytes, []byte("\n# unrelated edit\n")...), nil, c.Snapshot); err != nil {
		t.Fatal(err)
	}
}
