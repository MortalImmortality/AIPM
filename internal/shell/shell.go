// Package shell provides a line-oriented UI; terminal secret handling belongs to its caller.
package shell

import (
	"aipm/internal/app"
	"aipm/internal/provider"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var errCancelled = errors.New("已取消")

type session struct {
	a                 *app.App
	in                io.Reader
	out               io.Writer
	secret            func() (string, error)
	writeErr, readErr error
}

// Run never buffers ahead of a newline, so ordinary input cannot consume a secret.
// Switch selection is 0 cancel, 1..N saved providers; official restoration is operation 6.
func Run(a *app.App, in io.Reader, out io.Writer, readSecret func() (string, error)) error {
	s := &session{a: a, in: in, out: out, secret: readSecret}
	for {
		if _, err := a.Refresh(); err != nil {
			return err
		}
		s.print("1 Codex\n2 Claude\n0 退出\n")
		choice, err := s.line("客户端> ")
		if errors.Is(err, errCancelled) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch choice {
		case "0":
			return s.writeErr
		case "1", "2":
			tool := provider.Codex
			if choice == "2" {
				tool = provider.Claude
			}
			err = s.client(tool)
		default:
			s.print("无效选择\n")
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if s.writeErr != nil {
			return s.writeErr
		}
	}
}

func (s *session) print(format string, args ...any) {
	if s.writeErr == nil {
		_, s.writeErr = fmt.Fprintf(s.out, format, args...)
	}
}
func (s *session) line(prompt string) (string, error) {
	s.print("%s", prompt)
	if s.writeErr != nil {
		return "", s.writeErr
	}
	var b []byte
	oversized := false
	for {
		var one [1]byte
		n, err := s.in.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				if oversized {
					return "", fmt.Errorf("输入不能超过 8192 字节")
				}
				value := strings.TrimSpace(string(b))
				if value == "/cancel" {
					return "", errCancelled
				}
				return value, nil
			}
			if len(b) < 8192 {
				b = append(b, one[0])
			} else {
				oversized = true
			}
		}
		if err != nil {
			s.readErr = err
			return "", err
		}
	}
}
func (s *session) client(tool provider.Tool) error {
	for {
		v, err := s.a.Refresh()
		if err != nil {
			return err
		}
		s.print("\n%s 当前：%s\n", tool, provider.Display(v.Status[tool].Label))
		if v.Errors[tool] != "" {
			s.print("配置错误：%s\n", provider.Display(v.Errors[tool]))
		}
		s.print("1 查看\n2 新增\n3 编辑\n4 删除\n5 切换\n6 官方恢复\n0 返回\n")
		choice, err := s.line("操作> ")
		if errors.Is(err, errCancelled) {
			continue
		}
		if err != nil {
			return err
		}
		if choice == "0" {
			return nil
		}
		err = s.operation(tool, choice)
		if errors.Is(err, io.EOF) || errors.Is(err, errInterrupted) || errors.Is(err, errTerminalRestore) {
			return err
		}
		if s.readErr != nil {
			return s.readErr
		}
		if s.writeErr != nil {
			return s.writeErr
		}
		if err != nil {
			s.print("%s\n", provider.Display(err.Error()))
		}
	}
}
func (s *session) providers(tool provider.Tool) ([]provider.Provider, error) {
	v, err := s.a.Refresh()
	if err != nil {
		return nil, err
	}
	var result []provider.Provider
	for _, p := range v.Registry.Providers {
		if p.Tool == tool {
			result = append(result, p)
		}
	}
	return result, nil
}
func (s *session) choose(tool provider.Tool) (provider.Provider, error) {
	for {
		list, err := s.providers(tool)
		if err != nil {
			return provider.Provider{}, err
		}
		s.print("0 取消\n")
		for i, p := range list {
			s.print("%d %s\n", i+1, provider.Display(p.Name))
		}
		value, err := s.line("Provider> ")
		if err != nil {
			return provider.Provider{}, err
		}
		if value == "0" {
			return provider.Provider{}, errCancelled
		}
		n, err := strconv.Atoi(value)
		if err == nil && n > 0 && n <= len(list) {
			return list[n-1], nil
		}
		s.print("无效选择\n")
	}
}
func (s *session) operation(tool provider.Tool, choice string) error {
	var action *app.Action
	var err error
	switch choice {
	case "1":
		list, err := s.providers(tool)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			s.print("暂无 Provider\n")
		}
		for i, p := range list {
			s.print("%d %s | %s | %s | %s\n", i+1, provider.Display(p.Name), provider.Display(p.BaseURL), provider.Display(p.Model), p.CredentialLabel())
			for _, key := range provider.ModelKeys {
				if value := p.Models[key]; value != "" {
					s.print("%s: %s\n", key, provider.Display(value))
				}
			}
		}
		return nil
	case "2", "3":
		p := provider.Provider{ID: provider.NewID(), Tool: tool, AuthMode: "token"}
		editing := choice == "3"
		if editing {
			p, err = s.choose(tool)
			if err != nil {
				return err
			}
		}
		p, err = s.form(p)
		if err == nil {
			action, err = s.a.PrepareSave(p)
		}
	case "4", "5":
		p, e := s.choose(tool)
		if e != nil {
			return e
		}
		if choice == "4" {
			action, err = s.a.PrepareDelete(p.ID)
		} else {
			action, err = s.a.PrepareSwitch(tool, p.ID)
		}
	case "6":
		action, err = s.a.PrepareSwitch(tool, "")
	default:
		return fmt.Errorf("无效选择")
	}
	if err != nil {
		return err
	}
	return s.confirm(action)
}
func (s *session) field(label string, current *string, optional bool) error {
	prompt := label
	if *current != "" {
		prompt += " [" + provider.Display(*current) + "]"
	}
	value, err := s.line(prompt + "> ")
	if err != nil {
		return err
	}
	if value == "/clear" {
		if !optional {
			return fmt.Errorf("/clear 仅用于可选模型字段")
		}
		*current = ""
	} else if value != "" {
		*current = value
	}
	return nil
}
func (s *session) form(p provider.Provider) (provider.Provider, error) {
	models := make(map[string]string, len(p.Models))
	for k, v := range p.Models {
		models[k] = v
	}
	p.Models = models
	for {
		s.print("表单：/cancel 取消；空白保留已有值；可选模型可用 /clear 清除。\n")
		for _, f := range []struct {
			label    string
			value    *string
			optional bool
		}{
			{"名称 Name", &p.Name, false}, {"Base URL", &p.BaseURL, false}, {"模型 Model", &p.Model, p.Tool == provider.Claude},
		} {
			if err := s.field(f.label, f.value, f.optional); err != nil {
				return p, err
			}
		}
		alternative := "env"
		if p.Tool == provider.Claude {
			alternative = "api_key"
		}
		for {
			s.print("1 token\n2 %s\n当前：%s\n", alternative, p.AuthMode)
			value, err := s.line("凭据 AuthMode> ")
			if err != nil {
				return p, err
			}
			if value == "" {
				break
			}
			if value == "1" {
				p.AuthMode = "token"
				break
			}
			if value == "2" {
				p.AuthMode = alternative
				break
			}
			s.print("无效选择\n")
		}
		if p.AuthMode == "env" {
			if err := s.field("EnvKey", &p.EnvKey, false); err != nil {
				return p, err
			}
			p.Secret = ""
		} else {
			s.print("Secret> ")
			if s.writeErr != nil {
				return p, s.writeErr
			}
			if s.secret == nil {
				return p, fmt.Errorf("未提供安全凭据读取器")
			}
			value, err := s.secret()
			s.print("\n")
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, errInterrupted) || errors.Is(err, errTerminalRestore) {
					return p, err
				}
				return p, fmt.Errorf("无法读取凭据")
			}
			if value == "/cancel" {
				return p, errCancelled
			}
			if value == "/clear" || len(value) > 8192 {
				return p, fmt.Errorf("凭据输入无效；/clear 仅用于可选模型字段，凭据最多 8192 字节")
			}
			if value != "" {
				p.Secret = value
			}
			p.EnvKey = ""
		}
		if p.Tool == provider.Claude {
			for _, alias := range []struct{ label, key string }{
				{"Opus", "ANTHROPIC_DEFAULT_OPUS_MODEL"}, {"Sonnet", "ANTHROPIC_DEFAULT_SONNET_MODEL"},
				{"Haiku", "ANTHROPIC_DEFAULT_HAIKU_MODEL"}, {"Fable", "ANTHROPIC_DEFAULT_FABLE_MODEL"}, {"Default", "ANTHROPIC_DEFAULT_MODEL"},
			} {
				value := p.Models[alias.key]
				if err := s.field(alias.label, &value, true); err != nil {
					return p, err
				}
				if value == "" {
					delete(p.Models, alias.key)
				} else {
					p.Models[alias.key] = value
				}
			}
		}
		if err := p.Validate(); err != nil {
			s.print("字段错误：%s\n", err)
			value, e := s.line("重新填写 [y/N]> ")
			if e != nil {
				return p, e
			}
			if strings.EqualFold(value, "y") || strings.EqualFold(value, "yes") {
				continue
			}
			return p, errCancelled
		}
		return p, nil
	}
}
func (s *session) confirm(action *app.Action) error {
	kind := map[string]string{"save": "保存 Provider", "delete": "删除 Provider", "apply": "应用 Provider"}[action.Kind]
	if action.Kind == "apply" && action.Provider == nil {
		kind = "恢复官方"
	}
	s.print("操作：%s\n目标：%s\nRegistry：%s\n", kind, provider.Display(action.Path), provider.Display(s.a.Store.RegistryPath()))
	if action.Provider != nil {
		s.print("凭据：%s\n", action.Provider.CredentialLabel())
	}
	for _, field := range action.Change.Fields {
		s.print("变更：%s\n", provider.Display(field))
	}
	for _, warning := range action.Change.Warnings {
		s.print("警告：%s\n", provider.Display(warning))
	}
	s.print("警告：直接凭据、配置及历史备份可能以明文保存；界面遮罩不是加密。\n")
	value, err := s.line("确认执行 [y/N]> ")
	if err != nil {
		return err
	}
	if !strings.EqualFold(value, "y") && !strings.EqualFold(value, "yes") {
		return errCancelled
	}
	result, err := s.a.Execute(action)
	if err == nil {
		s.print("%s\n", result)
	}
	return err
}
