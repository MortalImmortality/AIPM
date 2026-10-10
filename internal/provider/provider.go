package provider

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type Tool string

const (
	Codex  Tool = "codex"
	Claude Tool = "claude"
)

var Tools = []Tool{Codex, Claude}

type Provider struct {
	ID       string            `json:"id"`
	Tool     Tool              `json:"tool"`
	Name     string            `json:"name"`
	BaseURL  string            `json:"base_url"`
	Model    string            `json:"model,omitempty"`
	AuthMode string            `json:"auth_mode"`
	Secret   string            `json:"secret,omitempty"`
	EnvKey   string            `json:"env_key,omitempty"`
	Models   map[string]string `json:"models,omitempty"`
}

var safeID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var ModelKeys = []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL"}

func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (p Provider) Validate() error {
	if !safeID.MatchString(p.ID) {
		return fmt.Errorf("provider ID 无效")
	}
	if p.Tool != Codex && p.Tool != Claude {
		return fmt.Errorf("客户端无效")
	}
	if strings.TrimSpace(p.Name) == "" || len([]rune(p.Name)) > 80 {
		return fmt.Errorf("名称必须为 1–80 个字符")
	}
	if hasControl(p.Name) || hasControl(p.Model) {
		return fmt.Errorf("名称或模型不能包含控制字符")
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return fmt.Errorf("URL 必须为 HTTP(S)，不能含凭据、查询或片段")
	}
	if p.Tool == Codex {
		if strings.TrimSpace(p.Model) == "" {
			return fmt.Errorf("Codex 模型不能为空")
		}
		if p.AuthMode != "token" && p.AuthMode != "env" {
			return fmt.Errorf("Codex 凭据模式为 token 或 env")
		}
	} else if p.AuthMode != "token" && p.AuthMode != "api_key" {
		return fmt.Errorf("Claude 凭据模式为 token 或 api_key")
	}
	if p.AuthMode == "env" {
		if !envName.MatchString(p.EnvKey) {
			return fmt.Errorf("环境变量名称无效")
		}
		if p.Secret != "" {
			return fmt.Errorf("env 模式不能同时保存 token")
		}
	} else if strings.TrimSpace(p.Secret) == "" || hasControl(p.Secret) {
		return fmt.Errorf("凭据不能为空或含控制字符")
	}
	for k, v := range p.Models {
		found := false
		for _, allowed := range ModelKeys {
			if k == allowed {
				found = true
			}
		}
		if !found || strings.TrimSpace(v) == "" || hasControl(v) {
			return fmt.Errorf("模型映射无效")
		}
	}
	return nil
}
func hasControl(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 || r == 0x1b {
			return true
		}
	}
	return false
}
func Display(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
}

// Key is a literal Codex table key, not a TOML dotted path or internal ID.
func (p Provider) Key() string { return "anim_" + p.Name }
func (p Provider) CredentialLabel() string {
	if p.AuthMode == "env" {
		return "env: " + Display(p.EnvKey)
	}
	return p.AuthMode + ": ••••••••"
}
