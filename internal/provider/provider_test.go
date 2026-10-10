package provider

import (
	"strings"
	"testing"
)

func validProvider(tool Tool) Provider {
	return Provider{ID: NewID(), Tool: tool, Name: "Gateway", BaseURL: "https://gateway.example/v1", Model: "test-model", AuthMode: "token", Secret: "test-secret-do-not-display"}
}

func TestValidateSupportedAuthentication(t *testing.T) {
	for _, tool := range Tools {
		for _, mode := range []string{"token", "env", "api_key"} {
			t.Run(string(tool)+"/"+mode, func(t *testing.T) {
				p := validProvider(tool)
				p.AuthMode = mode
				if mode == "env" {
					p.Secret = ""
					p.EnvKey = "TEST_API_KEY"
				}
				err := p.Validate()
				valid := mode == "token" || (tool == Codex && mode == "env") || (tool == Claude && mode == "api_key")
				if (err == nil) != valid {
					t.Fatalf("Validate() = %v, valid = %v", err, valid)
				}
			})
		}
	}
	p := validProvider(Claude)
	p.Model = ""
	p.Models = map[string]string{}
	for _, key := range ModelKeys {
		p.Models[key] = "mapped-model"
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsUnsafeFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Provider)
	}{
		{"invalid ID", func(p *Provider) { p.ID = "../../provider" }},
		{"uppercase ID", func(p *Provider) { p.ID = strings.Repeat("A", 32) }},
		{"short ID", func(p *Provider) { p.ID = "abc" }},
		{"unknown tool", func(p *Provider) { p.Tool = "unknown" }},
		{"blank name", func(p *Provider) { p.Name = "  " }},
		{"long name", func(p *Provider) { p.Name = strings.Repeat("界", 81) }},
		{"name control", func(p *Provider) { p.Name = "gateway\x1b[31m" }},
		{"model control", func(p *Provider) { p.Model = "model\nnext" }},
		{"missing model", func(p *Provider) { p.Model = " " }},
		{"empty secret", func(p *Provider) { p.Secret = " " }},
		{"secret newline", func(p *Provider) { p.Secret = "secret\nvalue" }},
		{"secret DEL", func(p *Provider) { p.Secret = "secret\x7f" }},
		{"unknown mode", func(p *Provider) { p.AuthMode = "password" }},
		{"bad env name", func(p *Provider) { p.AuthMode = "env"; p.Secret = ""; p.EnvKey = "1BAD" }},
		{"env injection", func(p *Provider) { p.AuthMode = "env"; p.Secret = ""; p.EnvKey = "KEY; command" }},
		{"env plus secret", func(p *Provider) { p.AuthMode = "env"; p.EnvKey = "API_KEY" }},
		{"unknown mapping", func(p *Provider) { p.Models = map[string]string{"UNRELATED": "model"} }},
		{"empty mapping", func(p *Provider) { p.Models = map[string]string{ModelKeys[0]: " "} }},
		{"mapping control", func(p *Provider) { p.Models = map[string]string{ModelKeys[0]: "bad\rmodel"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProvider(Codex)
			tc.mutate(&p)
			if p.Validate() == nil {
				t.Fatal("unsafe provider accepted")
			}
		})
	}
}

func TestValidateURL(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "file:///tmp/key", "https:///v1", "//gateway.example/v1", "https://user:pass@gateway.example/v1", "https://gateway.example/v1?key=secret", "https://gateway.example/v1#fragment", "https://gateway.example/\nheader", "https://gateway.example/%zz"} {
		t.Run(raw, func(t *testing.T) {
			p := validProvider(Codex)
			p.BaseURL = raw
			if p.Validate() == nil {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
	for _, raw := range []string{"https://gateway.example/v1", "http://localhost:8080/v1", "http://[::1]:8080/v1"} {
		p := validProvider(Codex)
		p.BaseURL = raw
		if err := p.Validate(); err != nil {
			t.Fatalf("valid URL %q: %v", raw, err)
		}
	}
}

func TestIdentifiersAndRedactedDisplay(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if !safeID.MatchString(id) || seen[id] {
			t.Fatalf("invalid or duplicate ID %q", id)
		}
		seen[id] = true
	}
	p := validProvider(Codex)
	if p.Key() != "anim_"+p.Name {
		t.Fatal("unexpected config key")
	}
	id := p.ID
	for _, name := range []string{"CaseSensitive", "my gateway", "中文.网关", "  Exact Name  "} {
		p.Name = name
		if p.Key() != "anim_"+name || p.ID != id {
			t.Fatal("config key normalized name or changed internal ID")
		}
	}
	if strings.Contains(p.CredentialLabel(), p.Secret) || !strings.Contains(p.CredentialLabel(), "••") {
		t.Fatal("credential not redacted")
	}
	p.AuthMode = "env"
	p.EnvKey = "TEST_API_KEY"
	if p.CredentialLabel() != "env: TEST_API_KEY" {
		t.Fatal("missing env credential label")
	}
	if Display("name\n\x1b\x7f") != "name   " {
		t.Fatal("display retains control characters")
	}
}
