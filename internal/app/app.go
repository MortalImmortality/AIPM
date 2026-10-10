package app

import (
	"aipm/internal/config"
	"aipm/internal/provider"
	"aipm/internal/store"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type App struct {
	Store *store.Store
	Paths map[provider.Tool]string
}
type View struct {
	Registry store.Registry
	Status   map[provider.Tool]config.Status
	Errors   map[provider.Tool]string
}
type Action struct {
	Tool             provider.Tool
	Kind             string
	Provider         *provider.Provider
	Before           []byte
	Existed          bool
	Expected         store.Registry
	Next             store.Registry
	Change           config.Change
	Path             string
	GuardFingerprint string
}

func New() (*App, error) {
	paths, err := config.Paths()
	if err != nil {
		return nil, err
	}
	dir := os.Getenv("AIPM_HOME")
	if dir == "" {
		base, e := os.UserConfigDir()
		if e != nil {
			return nil, e
		}
		dir = filepath.Join(base, "aipm")
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("AIPM_HOME 必须为绝对路径")
	}
	return &App{Store: store.New(dir), Paths: paths}, nil
}
func (a *App) Refresh() (View, error) {
	r, err := a.Store.Load()
	if err != nil {
		return View{}, err
	}
	v := View{Registry: r, Status: map[provider.Tool]config.Status{}, Errors: map[provider.Tool]string{}}
	for _, t := range provider.Tools {
		b, _, e := store.ReadTarget(a.Paths[t])
		if e != nil {
			v.Errors[t] = e.Error()
			continue
		}
		s, e := config.Inspect(t, b, r.Providers)
		if e != nil {
			v.Errors[t] = e.Error()
			continue
		}
		s.Warnings = append(s.Warnings, config.Warnings(t, nil)...)
		v.Status[t] = s
	}
	return v, nil
}
func clone(r store.Registry) store.Registry {
	b, _ := json.Marshal(r)
	var n store.Registry
	_ = json.Unmarshal(b, &n)
	if n.Snapshots == nil {
		n.Snapshots = map[string]map[string]any{}
	}
	return n
}
func (a *App) snapshotKey(t provider.Tool) string { return string(t) + ":" + a.Paths[t] }
func checkName(r store.Registry, p provider.Provider) error {
	for _, old := range r.Providers {
		if old.Tool == p.Tool && old.ID != p.ID && strings.EqualFold(strings.TrimSpace(old.Name), strings.TrimSpace(p.Name)) {
			return fmt.Errorf("同一客户端已有同名 provider")
		}
	}
	return nil
}
func (a *App) PrepareSave(p provider.Provider) (*Action, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	v, err := a.Refresh()
	if err != nil {
		return nil, err
	}
	if err = checkName(v.Registry, p); err != nil {
		return nil, err
	}
	n := clone(v.Registry)
	found := false
	for i, old := range n.Providers {
		if old.ID == p.ID {
			if old.Tool != p.Tool {
				return nil, fmt.Errorf("不能更改 provider 客户端")
			}
			n.Providers[i] = p
			found = true
		}
	}
	if !found {
		n.Providers = append(n.Providers, p)
	}
	if found {
		if e := v.Errors[p.Tool]; e != "" {
			return nil, fmt.Errorf("无法验证活跃 provider：%s", e)
		}
		status := v.Status[p.Tool]
		if status.ActiveID == p.ID {
			return a.prepare(p.Tool, &p, v.Registry, n, "apply")
		}
		if status.Kind == "external" || status.Kind == "ambiguous" {
			return nil, fmt.Errorf("配置存在外部修改；先切换或恢复官方，再编辑已有 provider")
		}
	}
	action := &Action{Tool: p.Tool, Kind: "save", Provider: &p, Expected: v.Registry, Next: n, Path: a.Store.RegistryPath()}
	if found {
		action.GuardFingerprint = v.Status[p.Tool].Fingerprint
	}
	return action, nil
}
func (a *App) PrepareDelete(id string) (*Action, error) {
	v, err := a.Refresh()
	if err != nil {
		return nil, err
	}
	n := clone(v.Registry)
	var p *provider.Provider
	for i, old := range n.Providers {
		if old.ID == id {
			copy := old
			p = &copy
			n.Providers = append(n.Providers[:i], n.Providers[i+1:]...)
			break
		}
	}
	if p == nil {
		return nil, fmt.Errorf("provider 不存在")
	}
	if e := v.Errors[p.Tool]; e != "" {
		return nil, fmt.Errorf("无法验证活跃 provider：%s", e)
	}
	s := v.Status[p.Tool]
	if s.ActiveID == id {
		return nil, fmt.Errorf("不能删除活跃 provider；请先切换或恢复官方")
	}
	if s.Kind == "external" || s.Kind == "ambiguous" {
		return nil, fmt.Errorf("配置存在外部修改；先切换或恢复官方，再删除 provider")
	}
	return &Action{Tool: p.Tool, Kind: "delete", Provider: p, Expected: v.Registry, Next: n, Path: a.Store.RegistryPath(), GuardFingerprint: s.Fingerprint}, nil
}
func (a *App) PrepareSwitch(t provider.Tool, id string) (*Action, error) {
	r, err := a.Store.Load()
	if err != nil {
		return nil, err
	}
	var p *provider.Provider
	if id != "" {
		for _, x := range r.Providers {
			if x.ID == id && x.Tool == t {
				copy := x
				p = &copy
			}
		}
		if p == nil {
			return nil, fmt.Errorf("provider 不存在")
		}
	}
	return a.prepare(t, p, r, clone(r), "apply")
}
func (a *App) prepare(t provider.Tool, p *provider.Provider, r, n store.Registry, kind string) (*Action, error) {
	path, ok := a.Paths[t]
	if !ok {
		return nil, fmt.Errorf("未知客户端")
	}
	b, exists, err := store.ReadTarget(path)
	if err != nil {
		return nil, err
	}
	change, err := config.Plan(t, b, p, config.Snapshot(r.Snapshots[a.snapshotKey(t)]), r.Providers...)
	if err != nil {
		return nil, err
	}
	if change.Snapshot != nil {
		n.Snapshots[a.snapshotKey(t)] = map[string]any(change.Snapshot)
	}
	change.Warnings = append(change.Warnings, config.Warnings(t, p)...)
	previous, err := config.Inspect(t, b, r.Providers)
	if err != nil {
		return nil, err
	}
	if previous.Kind == "external" || previous.Kind == "ambiguous" {
		change.Warnings = append(change.Warnings, "当前为外部配置；此次操作将替换列出的路由、凭据与模型字段。")
	}
	s, err := config.Inspect(t, change.Bytes, n.Providers)
	if err != nil {
		return nil, err
	}
	if p == nil && s.Kind != "official" {
		return nil, fmt.Errorf("官方配置验证未通过")
	}
	if p != nil && s.ActiveID != p.ID {
		return nil, fmt.Errorf("provider 配置验证未通过")
	}
	return &Action{Tool: t, Kind: kind, Provider: p, Before: b, Existed: exists, Expected: r, Next: n, Change: change, Path: path}, nil
}
func (a *App) Execute(action *Action) (string, error) {
	if action == nil {
		return "", fmt.Errorf("无待执行操作")
	}
	if action.Kind == "apply" {
		backup, err := a.Store.Apply(action.Path, action.Before, action.Existed, action.Change.Bytes, action.Expected, action.Next)
		if err != nil {
			return "", err
		}
		if backup != "" {
			return "已应用；备份：" + backup, nil
		}
		return "已应用；新建客户端配置", nil
	}
	if action.GuardFingerprint != "" {
		b, _, err := store.ReadTarget(a.Paths[action.Tool])
		if err != nil {
			return "", err
		}
		s, err := config.Inspect(action.Tool, b, action.Expected.Providers)
		if err != nil {
			return "", err
		}
		if s.Fingerprint != action.GuardFingerprint {
			return "", fmt.Errorf("客户端配置已改变；请取消并刷新后重试")
		}
	}
	if err := a.Store.Save(action.Expected, action.Next); err != nil {
		return "", err
	}
	if action.Kind == "delete" {
		return "已删除 provider；历史备份仍保留", nil
	}
	return "已保存 provider", nil
}
