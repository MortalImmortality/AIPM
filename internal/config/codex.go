package config

import (
	"aipm/internal/provider"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable/edit"
)

var legacyCodexKey = regexp.MustCompile(`^aipm_[a-f0-9]{32}$`)

func codexKeys(p provider.Provider) []string { return []string{p.Key(), "aipm_" + p.ID} }

func parseCodex(b []byte) (map[string]any, error) {
	m := map[string]any{}
	if err := toml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("Codex TOML 配置无效")
	}
	if err := stringFields(m, []string{"model_provider", "model", "openai_base_url", "profile"}); err != nil {
		return nil, err
	}
	if v, ok := m["profiles"]; ok {
		profiles, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Codex profiles 类型无效")
		}
		for _, v := range profiles {
			profile, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Codex profile 类型无效")
			}
			if err := stringFields(profile, []string{"model", "model_provider", "openai_base_url"}); err != nil {
				return nil, err
			}
		}
	}
	if v, ok := m["model_providers"]; ok {
		tables, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Codex model_providers 类型无效")
		}
		for _, v := range tables {
			table, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("Codex provider 表类型无效")
			}
			if err := stringFields(table, []string{"name", "base_url", "wire_api", "env_key", "experimental_bearer_token"}); err != nil {
				return nil, err
			}
			if v, ok := table["requires_openai_auth"]; ok {
				if _, ok := v.(bool); !ok {
					return nil, fmt.Errorf("Codex 鉴权字段类型无效")
				}
			}
		}
	}
	return m, nil
}

// Error messages intentionally contain neither parser diagnostics nor field values.
func stringFields(m map[string]any, keys []string) error {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("配置字段类型无效")
			}
		}
	}
	return nil
}
func text(m map[string]any, key string) string { s, _ := m[key].(string); return s }
func officialEndpoint(s, host string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host == host && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/" || u.Path == "/v1" || u.Path == "/v1/")
}
func codexWarnings(m map[string]any) []string {
	if _, ok := m["profiles"]; ok {
		return []string{"Codex profiles 可覆盖用户配置；未修改 profiles。"}
	}
	if _, ok := m["profile"]; ok {
		return []string{"Codex profile 可覆盖用户配置；未修改 profile。"}
	}
	return nil
}
func codexTable(m map[string]any, key string) map[string]any {
	tables, _ := m["model_providers"].(map[string]any)
	t, _ := tables[key].(map[string]any)
	return t
}
func codexOfficial(m map[string]any) bool {
	key := text(m, "model_provider")
	if key != "" && key != "openai" {
		return false
	}
	if s := text(m, "openai_base_url"); s != "" && !officialEndpoint(s, "api.openai.com") {
		return false
	}
	t := codexTable(m, "openai")
	// A built-in override may alter routing/authentication in ways we cannot own.
	for k, v := range t {
		switch k {
		case "name":
		case "base_url":
			if !officialEndpoint(v.(string), "api.openai.com") {
				return false
			}
		case "wire_api":
			if v != "responses" {
				return false
			}
		case "requires_openai_auth":
			if v != true {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func codexMatches(m map[string]any, p provider.Provider) bool {
	if p.Tool != provider.Codex || p.Validate() != nil || text(m, "model") != p.Model || text(m, "openai_base_url") != "" {
		return false
	}
	for _, key := range codexKeys(p) {
		if text(m, "model_provider") == key && reflect.DeepEqual(codexTable(m, key), codexDefinition(p)) {
			return true
		}
	}
	return false
}
func codexDefinition(p provider.Provider) map[string]any {
	t := map[string]any{"name": p.Name, "base_url": p.BaseURL, "wire_api": "responses", "requires_openai_auth": false}
	if p.AuthMode == "env" {
		t["env_key"] = p.EnvKey
	} else {
		t["experimental_bearer_token"] = p.Secret
	}
	return t
}
func inspectCodex(b []byte, ps []provider.Provider) (Status, error) {
	m, err := parseCodex(b)
	if err != nil {
		return Status{}, err
	}
	s := Status{Kind: "external", Label: "外部配置", Warnings: codexWarnings(m)}
	if codexOfficial(m) {
		s.Kind = "official"
		s.Label = "Official"
		return s, nil
	}
	for _, p := range ps {
		if codexMatches(m, p) {
			if s.Kind == "managed" {
				s.Kind = "ambiguous"
				s.ActiveID = ""
				s.Label = "配置匹配多个 provider"
				return s, nil
			}
			s.Kind = "managed"
			s.ActiveID = p.ID
			s.Label = provider.Display(p.Name)
		}
	}
	return s, nil
}

func planCodex(b []byte, p *provider.Provider, snapshot Snapshot, registered ...provider.Provider) (Change, error) {
	m, err := parseCodex(b)
	if err != nil {
		return Change{}, err
	}
	if p != nil {
		if p.Tool != provider.Codex {
			return Change{}, fmt.Errorf("provider 客户端不匹配")
		}
		if err := p.Validate(); err != nil {
			return Change{}, err
		}
	}
	if p == nil && !codexOfficial(map[string]any{"model_provider": "openai", "model_providers": map[string]any{"openai": codexTable(m, "openai")}}) {
		return Change{}, fmt.Errorf("内置 openai provider 存在外部路由覆盖，请先手动处理")
	}
	// File-level leading trivia is not owned by the first TOML key. Keep it
	// outside the editor so deleting that key cannot consume the header.
	header, body := codexHeader(b)
	d, err := edit.Parse(body)
	if err != nil {
		return Change{}, fmt.Errorf("Codex TOML 配置无法编辑")
	}
	owned := codexOwnedTables(m, snapshot, registered)
	if codexOfficial(m) {
		snapshot = nil
	}
	if p == nil && !managedModelsUnchanged(snapshot, map[string]any{"model": m["model"]}) {
		return Change{}, fmt.Errorf("模型已被外部修改，无法恢复旧快照")
	}
	c := Change{Snapshot: copySnapshot(snapshot), Warnings: codexWarnings(m)}
	if snapshot == nil {
		c.Snapshot = Snapshot{"_officialCaptured": codexOfficial(m)}
		if codexOfficial(m) {
			c.Snapshot["model"] = m["model"]
		}
	}
	set := func(path []string, v any) error {
		if err := d.Set(path, v); err != nil {
			return fmt.Errorf("Codex 配置无法编辑")
		}
		c.Fields = append(c.Fields, strings.Join(path, "."))
		return nil
	}
	del := func(path ...string) {
		if d.Delete(path) {
			c.Fields = append(c.Fields, strings.Join(path, "."))
		}
	}
	del("openai_base_url")
	activeOwned := owned[text(m, "model_provider")] && managedModelsUnchanged(snapshot, map[string]any{"model": m["model"]})
	// Registry definitions and successful-plan hashes prove ownership. Prefixes do not.
	for key := range owned {
		if p == nil || (key != p.Key() && (key == text(m, "model_provider") || key == "aipm_"+p.ID || key == codexSnapshotKey(m, snapshot))) {
			del("model_providers", key)
		}
	}
	if p == nil {
		if err := set([]string{"model_provider"}, "openai"); err != nil {
			return Change{}, err
		}
		if v, ok := c.Snapshot["model"]; ok && v != nil {
			if _, ok := v.(string); !ok {
				return Change{}, fmt.Errorf("Codex 快照类型无效")
			}
			if err := set([]string{"model"}, v); err != nil {
				return Change{}, err
			}
		} else {
			del("model")
		}
	} else {
		if t := codexTable(m, p.Key()); t != nil && !reflect.DeepEqual(t, codexDefinition(*p)) && !(activeOwned && text(m, "model_provider") == p.Key()) {
			return Change{}, fmt.Errorf("目标 provider 表已被外部配置占用")
		}
		if err := set([]string{"model_provider"}, p.Key()); err != nil {
			return Change{}, err
		}
		if err := set([]string{"model"}, p.Model); err != nil {
			return Change{}, err
		}
		if err := set([]string{"model_providers", p.Key()}, codexDefinition(*p)); err != nil {
			return Change{}, err
		}
	}
	c.Bytes = append(append([]byte{}, header...), d.Bytes()...)
	result, err := parseCodex(c.Bytes)
	if err != nil {
		return Change{}, fmt.Errorf("Codex 编辑结果无效")
	}
	c.Snapshot["_managedModels"] = map[string]any{"model": result["model"]}
	delete(c.Snapshot, "_managedProvider")
	delete(c.Snapshot, "_managedProviderKey")
	if p != nil {
		c.Snapshot["_managedProvider"] = codexProviderHash(p.Key(), codexTable(result, p.Key()))
		c.Snapshot["_managedProviderKey"] = p.Key()
	}
	return c, nil
}
func codexHeader(b []byte) ([]byte, []byte) {
	end := 0
	for end < len(b) {
		next := bytes.IndexByte(b[end:], '\n')
		if next < 0 {
			next = len(b) - end
		} else {
			next++
		}
		line := bytes.TrimSpace(b[end : end+next])
		if len(line) > 0 && line[0] != '#' {
			break
		}
		end += next
	}
	return b[:end], b[end:]
}
func codexProviderHash(key string, table map[string]any) string {
	b, _ := json.Marshal(map[string]any{"key": key, "table": table})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func codexSnapshotKey(m map[string]any, snapshot Snapshot) string {
	if key, ok := snapshot["_managedProviderKey"].(string); ok {
		return key
	}
	// Old snapshots hash the key but do not store it separately. Only their
	// active legacy key can be recovered, and the hash must still match.
	key := text(m, "model_provider")
	if legacyCodexKey.MatchString(key) {
		return key
	}
	return ""
}
func codexOwnedBySnapshot(m map[string]any, snapshot Snapshot) bool {
	key := codexSnapshotKey(m, snapshot)
	expected, ok := snapshot["_managedProvider"].(string)
	return ok && key != "" && codexTable(m, key) != nil && expected == codexProviderHash(key, codexTable(m, key))
}
func codexOwnedTables(m map[string]any, snapshot Snapshot, registered []provider.Provider) map[string]bool {
	owned := map[string]bool{}
	if codexOwnedBySnapshot(m, snapshot) {
		owned[codexSnapshotKey(m, snapshot)] = true
	}
	for _, p := range registered {
		if p.Tool != provider.Codex || p.Validate() != nil {
			continue
		}
		for _, key := range codexKeys(p) {
			if reflect.DeepEqual(codexTable(m, key), codexDefinition(p)) {
				owned[key] = true
			}
		}
	}
	return owned
}
func copySnapshot(s Snapshot) Snapshot {
	out := Snapshot{}
	for k, v := range s {
		out[k] = v
	}
	return out
}
func managedModelsUnchanged(s Snapshot, current map[string]any) bool {
	v, ok := s["_managedModels"]
	if !ok {
		return true
	}
	previous, ok := v.(map[string]any)
	if !ok {
		if snap, valid := v.(Snapshot); valid {
			previous = map[string]any(snap)
		} else {
			return false
		}
	}
	return reflect.DeepEqual(previous, current)
}
