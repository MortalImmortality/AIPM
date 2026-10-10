package config

import (
	"aipm/internal/provider"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

func Paths() (map[provider.Tool]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	paths := map[provider.Tool]string{}
	for _, t := range provider.Tools {
		dir := os.Getenv("CODEX_HOME")
		name := "config.toml"
		fallback := filepath.Join(home, ".codex")
		if t == provider.Claude {
			dir = os.Getenv("CLAUDE_CONFIG_DIR")
			name = "settings.json"
			fallback = filepath.Join(home, ".claude")
		}
		if dir == "" {
			dir = fallback
		}
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("%s 配置目录必须为绝对路径", t)
		}
		paths[t] = filepath.Join(filepath.Clean(dir), name)
	}
	return paths, nil
}
func Inspect(tool provider.Tool, b []byte, providers []provider.Provider) (Status, error) {
	var s Status
	var err error
	if tool == provider.Codex {
		s, err = inspectCodex(b, providers)
	} else {
		s, err = inspectClaude(b, providers)
	}
	s.Fingerprint = fmt.Sprintf("%x", sha256.Sum256(b))
	return s, err
}

// registered supplies existing definitions for ownership-checked Codex cleanup.
func Plan(tool provider.Tool, b []byte, p *provider.Provider, snapshot Snapshot, registered ...provider.Provider) (Change, error) {
	if p != nil {
		if p.Tool != tool {
			return Change{}, fmt.Errorf("provider 客户端不匹配")
		}
		if err := p.Validate(); err != nil {
			return Change{}, err
		}
	}
	if tool == provider.Codex {
		return planCodex(b, p, snapshot, registered...)
	}
	if tool == provider.Claude {
		return planClaude(b, p, snapshot)
	}
	return Change{}, fmt.Errorf("未知客户端")
}
func Warnings(tool provider.Tool, p *provider.Provider) []string {
	warnings := []string{"仅修改用户配置；项目、managed settings、命令行及 profile 可覆盖。请重启客户端。"}
	keys := []string{"OPENAI_BASE_URL", "CODEX_PROFILE"}
	if tool == provider.Claude {
		keys = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS"}
	}
	for _, k := range keys {
		if os.Getenv(k) != "" {
			warnings = append(warnings, "当前进程存在 "+k+"，未来客户端环境可能影响路由。")
		}
	}
	if p != nil && p.AuthMode == "env" && os.Getenv(p.EnvKey) == "" {
		warnings = append(warnings, "凭据环境变量 "+p.EnvKey+" 当前未设置；客户端启动前需配置。")
	}
	if p != nil && len(p.BaseURL) >= 7 && p.BaseURL[:7] == "http://" {
		warnings = append(warnings, "HTTP 未加密；凭据可能在传输中暴露。")
	}
	return warnings
}
