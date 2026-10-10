package config

import (
	"aipm/internal/provider"
	"bytes"
	"strings"
	"testing"
)

func TestClaudeRestoreAndPreservation(t *testing.T) {
	p := testProvider(provider.Claude)
	p.Models = map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "mapped-opus"}
	original := []byte(`{"model":"official-root","env":{"ANTHROPIC_API_KEY":"official-test-key","ANTHROPIC_DEFAULT_SONNET_MODEL":"official-sonnet","OTHER":"keep"},"large":9007199254740993,"oauth":{"keep":true}}`)
	c, err := planClaude(original, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := inspectClaude(c.Bytes, []provider.Provider{p}); s.Kind != "managed" {
		t.Fatal(s)
	}
	if !bytes.Contains(c.Bytes, []byte("9007199254740993")) {
		t.Fatal("number rounded")
	}
	q := p
	q.ID = strings.Repeat("b", 32)
	q.AuthMode = "api_key"
	q.Secret = "second-test-key"
	q.Models = nil
	next, err := planClaude(c.Bytes, &q, c.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	_, env, _ := parseClaude(next.Bytes)
	if _, ok := env["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatal("old auth remains")
	}
	if _, ok := env["ANTHROPIC_DEFAULT_OPUS_MODEL"]; ok {
		t.Fatal("old mapping remains")
	}
	back, err := planClaude(next.Bytes, nil, next.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	m, env, err := parseClaude(back.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if m["model"] != "official-root" || env["ANTHROPIC_API_KEY"] != "official-test-key" || env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "official-sonnet" || env["OTHER"] != "keep" || m["oauth"] == nil {
		t.Fatal("official preferences not restored")
	}
	if _, ok := env["ANTHROPIC_BASE_URL"]; ok {
		t.Fatal("endpoint restored")
	}
}
func TestClaudeRejectAndRedact(t *testing.T) {
	for _, b := range []string{`[]`, `null`, `{"env":null}`, `{"env":[]}`, `{"env":{"ANTHROPIC_API_KEY":2}}`, `{"model":false}`, `{"x":1,"x":2}`, `{"unknown":{"x":1,"x":2}}`, `{"x":[{"y":1,"y":2}]}`, `{"key":"synthetic-test-secret"`, `{} {}`} {
		_, err := inspectClaude([]byte(b), nil)
		if err == nil || strings.Contains(err.Error(), "synthetic-test-secret") {
			t.Fatal("invalid JSON accepted or secret leaked", err)
		}
	}
	for _, b := range []string{`{"apiKeyHelper":"command"}`, `{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`, `{"env":{"CLAUDE_CODE_USE_VERTEX":"true"}}`, `{"env":{"CLAUDE_CODE_USE_FOUNDRY":"1"}}`, `{"env":{"CLAUDE_CODE_USE_ANTHROPIC_AWS":"1"}}`, `{"env":{"ANTHROPIC_API_KEY":"key","ANTHROPIC_AUTH_TOKEN":"token"}}`} {
		if _, err := planClaude([]byte(b), nil, nil); err == nil {
			t.Fatal("conflict accepted")
		}
		if s, _ := inspectClaude([]byte(b), nil); s.Kind != "ambiguous" {
			t.Fatal(s)
		}
	}
}
func TestClaudeInference(t *testing.T) {
	for _, b := range []string{"", `{}`, `{"env":{"ANTHROPIC_API_KEY":"key"}}`, `{"env":{"ANTHROPIC_BASE_URL":"https://api.anthropic.com/v1"}}`} {
		if s, err := inspectClaude([]byte(b), nil); err != nil || s.Kind != "official" {
			t.Fatal(s, err)
		}
	}
	for _, u := range []string{"https://external.example", "https://api.anthropic.com.evil.example", "https://api.anthropic.com/other", "http://api.anthropic.com"} {
		b := []byte(`{"env":{"ANTHROPIC_BASE_URL":"` + u + `"}}`)
		if s, _ := inspectClaude(b, nil); s.Kind != "external" {
			t.Fatal(s)
		}
	}
	p := testProvider(provider.Claude)
	c, err := planClaude(nil, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Secret = "wrong"
	if s, _ := inspectClaude(c.Bytes, []provider.Provider{p}); s.Kind != "external" {
		t.Fatal(s)
	}
	p.Secret = "synthetic-test-secret"
	p.Models = map[string]string{"ANTHROPIC_DEFAULT_OPUS_MODEL": "wrong"}
	if s, _ := inspectClaude(c.Bytes, []provider.Provider{p}); s.Kind != "external" {
		t.Fatal(s)
	}
}
func TestClaudeExternalNoSnapshotAndModelConflict(t *testing.T) {
	b := []byte(`{"model":"thirdparty","env":{"ANTHROPIC_BASE_URL":"https://external.example","ANTHROPIC_API_KEY":"thirdparty-key","ANTHROPIC_MODEL":"thirdparty-model"}}`)
	back, err := planClaude(b, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, env, _ := parseClaude(back.Bytes)
	if m["model"] != nil || len(env) != 0 {
		t.Fatal("third-party prefs restored")
	}
	p := testProvider(provider.Claude)
	c, err := planClaude(b, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Snapshot["_officialCaptured"] != false {
		t.Fatal("external captured")
	}
	changed := bytes.ReplaceAll(c.Bytes, []byte("gateway-model"), []byte("external-edit"))
	if _, err := planClaude(changed, nil, c.Snapshot); err == nil {
		t.Fatal("external model overwritten")
	}
	changed = bytes.ReplaceAll(c.Bytes, []byte("thirdparty"), []byte("unrelated"))
	if _, err := planClaude(changed, nil, c.Snapshot); err != nil {
		t.Fatal(err)
	}
}
