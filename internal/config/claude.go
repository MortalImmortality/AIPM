package config

import (
	"aipm/internal/provider"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

var claudeCloudKeys = []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "ANTHROPIC_AWS"}
var claudeGatewayKeys = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}

// Token decoding rejects duplicate keys at every depth, including unknown fields.
// UseNumber preserves unknown numeric preferences without float64 rounding.
func decodeClaudeValue(d *json.Decoder, depth int) (any, error) {
	if depth > 256 {
		return nil, fmt.Errorf("JSON nesting")
	}
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		m := map[string]any{}
		for d.More() {
			t, err := d.Token()
			if err != nil {
				return nil, err
			}
			k, ok := t.(string)
			if !ok {
				return nil, fmt.Errorf("JSON key")
			}
			if _, ok := m[k]; ok {
				return nil, fmt.Errorf("JSON duplicate")
			}
			v, err := decodeClaudeValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		t, err := d.Token()
		if err != nil || t != json.Delim('}') {
			return nil, fmt.Errorf("JSON object")
		}
		return m, nil
	case '[':
		a := []any{}
		for d.More() {
			v, err := decodeClaudeValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		t, err := d.Token()
		if err != nil || t != json.Delim(']') {
			return nil, fmt.Errorf("JSON array")
		}
		return a, nil
	default:
		return nil, fmt.Errorf("JSON delimiter")
	}
}
func parseClaude(b []byte) (map[string]any, map[string]any, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		b = []byte("{}")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	v, err := decodeClaudeValue(d, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("Claude JSON 配置无效或含重复字段")
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, nil, fmt.Errorf("Claude JSON 配置含多余内容")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("Claude 配置根节点必须为对象")
	}
	if err := stringFields(m, []string{"model", "apiKeyHelper"}); err != nil {
		return nil, nil, err
	}
	env := map[string]any{}
	if v, ok := m["env"]; ok {
		var valid bool
		env, valid = v.(map[string]any)
		if !valid {
			return nil, nil, fmt.Errorf("Claude env 必须为对象")
		}
	}
	for _, v := range env {
		if _, ok := v.(string); !ok {
			return nil, nil, fmt.Errorf("Claude env 字段必须为字符串")
		}
	}
	return m, env, nil
}
func claudeBlocked(m, env map[string]any) bool {
	if strings.TrimSpace(text(m, "apiKeyHelper")) != "" {
		return true
	}
	for _, k := range claudeCloudKeys {
		s := strings.ToLower(strings.TrimSpace(text(env, k)))
		if s == "true" || s == "1" {
			return true
		}
		// Be conservative about a root-level switch even though env is customary.
		if v, ok := m[k]; ok {
			if v == true || v == "true" || v == "1" {
				return true
			}
		}
	}
	return false
}
func claudeOfficial(m, env map[string]any) bool {
	endpoint := text(env, "ANTHROPIC_BASE_URL")
	return !claudeBlocked(m, env) && !(text(env, "ANTHROPIC_AUTH_TOKEN") != "" && text(env, "ANTHROPIC_API_KEY") != "") && (endpoint == "" || officialEndpoint(endpoint, "api.anthropic.com"))
}
func claudeModels(p provider.Provider) map[string]any {
	m := map[string]any{}
	for k, v := range p.Models {
		m[k] = v
	}
	if p.Model != "" {
		m["ANTHROPIC_MODEL"] = p.Model
	}
	return m
}
func claudeMatches(m, env map[string]any, p provider.Provider) bool {
	if p.Tool != provider.Claude || p.Validate() != nil || claudeBlocked(m, env) || text(env, "ANTHROPIC_BASE_URL") != p.BaseURL {
		return false
	}
	if _, ok := m["model"]; ok {
		return false
	}
	auth, other := "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"
	if p.AuthMode == "api_key" {
		auth, other = other, auth
	}
	if text(env, auth) != p.Secret {
		return false
	}
	if _, ok := env[other]; ok {
		return false
	}
	expected := claudeModels(p)
	for _, k := range provider.ModelKeys {
		v, ok := env[k]
		want, present := expected[k]
		if ok != present || !reflect.DeepEqual(v, want) {
			return false
		}
	}
	return true
}
func inspectClaude(b []byte, ps []provider.Provider) (Status, error) {
	m, env, err := parseClaude(b)
	if err != nil {
		return Status{}, err
	}
	s := Status{Kind: "external", Label: "外部配置"}
	if claudeBlocked(m, env) {
		s.Kind = "ambiguous"
		s.Warnings = []string{"apiKeyHelper 或云路由开关可能覆盖凭据和路由。"}
		return s, nil
	}
	if text(env, "ANTHROPIC_AUTH_TOKEN") != "" && text(env, "ANTHROPIC_API_KEY") != "" {
		s.Kind = "ambiguous"
		s.Warnings = []string{"Claude 存在多个凭据来源。"}
		return s, nil
	}
	if claudeOfficial(m, env) {
		s.Kind = "official"
		s.Label = "Official"
		return s, nil
	}
	for _, p := range ps {
		if claudeMatches(m, env, p) {
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
func planClaude(b []byte, p *provider.Provider, snapshot Snapshot) (Change, error) {
	m, env, err := parseClaude(b)
	if err != nil {
		return Change{}, err
	}
	if p != nil {
		if p.Tool != provider.Claude {
			return Change{}, fmt.Errorf("provider 客户端不匹配")
		}
		if err := p.Validate(); err != nil {
			return Change{}, err
		}
	}
	if claudeBlocked(m, env) {
		return Change{}, fmt.Errorf("请先手动处理 apiKeyHelper 或云路由开关")
	}
	if text(env, "ANTHROPIC_AUTH_TOKEN") != "" && text(env, "ANTHROPIC_API_KEY") != "" {
		return Change{}, fmt.Errorf("Claude 存在凭据冲突，请先手动处理")
	}
	if claudeOfficial(m, env) {
		snapshot = nil
	}
	if p == nil && !managedModelsUnchanged(snapshot, claudeModelState(m, env)) {
		return Change{}, fmt.Errorf("模型已被外部修改，无法恢复旧快照")
	}
	c := Change{Snapshot: copySnapshot(snapshot)}
	if snapshot == nil {
		c.Snapshot["_officialCaptured"] = claudeOfficial(m, env)
	}
	if snapshot == nil && claudeOfficial(m, env) {
		c.Snapshot = Snapshot{"_officialCaptured": true, "model": m["model"], "env.ANTHROPIC_API_KEY": env["ANTHROPIC_API_KEY"]}
		for _, k := range provider.ModelKeys {
			c.Snapshot["env."+k] = env[k]
		}
	}
	del := func(k string) {
		if _, ok := env[k]; ok {
			delete(env, k)
			c.Fields = append(c.Fields, "env."+k)
		}
	}
	for _, k := range claudeGatewayKeys {
		del(k)
	}
	for _, k := range provider.ModelKeys {
		del(k)
	}
	if _, ok := m["model"]; ok {
		delete(m, "model")
		c.Fields = append(c.Fields, "model")
	}
	set := func(k string, v any) { env[k] = v; c.Fields = append(c.Fields, "env."+k) }
	if p == nil {
		if v, ok := c.Snapshot["model"]; ok && v != nil {
			if _, ok := v.(string); !ok {
				return Change{}, fmt.Errorf("Claude 快照类型无效")
			}
			m["model"] = v
			c.Fields = append(c.Fields, "model")
		}
		for _, k := range append(append([]string{}, provider.ModelKeys...), "ANTHROPIC_API_KEY") {
			if v, ok := c.Snapshot["env."+k]; ok && v != nil {
				if _, ok := v.(string); !ok {
					return Change{}, fmt.Errorf("Claude 快照类型无效")
				}
				set(k, v)
			}
		}
	} else {
		set("ANTHROPIC_BASE_URL", p.BaseURL)
		key := "ANTHROPIC_AUTH_TOKEN"
		if p.AuthMode == "api_key" {
			key = "ANTHROPIC_API_KEY"
		}
		set(key, p.Secret)
		for _, k := range provider.ModelKeys {
			if v, ok := claudeModels(*p)[k]; ok {
				set(k, v)
			}
		}
	}
	if len(env) > 0 {
		m["env"] = env
	} else {
		if _, ok := m["env"]; ok {
			m["env"] = env
		}
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Change{}, fmt.Errorf("Claude 配置无法编码")
	}
	c.Bytes = append(out, '\n')
	c.Snapshot["_managedModels"] = claudeModelState(m, env)
	return c, nil
}
func claudeModelState(m, env map[string]any) map[string]any {
	state := map[string]any{"model": m["model"]}
	for _, k := range provider.ModelKeys {
		state["env."+k] = env[k]
	}
	return state
}
