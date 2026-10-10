package main

import (
	"aipm/internal/app"
	"aipm/internal/provider"
	"aipm/internal/shell"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/x/term"
)

var version = "dev"

const help = `aipm — Codex / Claude Code provider 管理

用法：
  aipm              打开 shell 编号菜单
  aipm list         查看保存的 provider（不显示密钥）
  aipm status       查看实际用户配置状态
  aipm --help       帮助
  aipm --version    版本

客户端：1 Codex，2 Claude，0 退出
操作：1 查看，2 新增，3 编辑，4 删除，5 切换，6 恢复官方，0 返回
逐项输入；编辑留空保留，可选字段 /clear 清空，/cancel 取消。
密钥输入不回显；确认 y 执行，默认 N。Ctrl+C / EOF 退出。

目录覆盖：AIPM_HOME、CODEX_HOME、CLAUDE_CONFIG_DIR（绝对路径）
凭据与备份明文保存在私有用户文件（0600），不提供加密存储。
不修改登录、shell profile、项目或 managed settings。切换后重启客户端。
`

func run(args []string, out io.Writer) error {
	if len(args) > 1 {
		return fmt.Errorf("参数过多；使用 aipm --help")
	}
	if len(args) == 1 {
		switch args[0] {
		case "--help", "-h", "help":
			_, e := fmt.Fprint(out, help)
			return e
		case "--version", "version":
			_, e := fmt.Fprintln(out, "aipm "+version)
			return e
		}
	}
	a, err := app.New()
	if err != nil {
		return err
	}
	if len(args) == 1 {
		if args[0] != "list" && args[0] != "status" {
			return fmt.Errorf("未知命令；使用 aipm --help")
		}
		v, e := a.Refresh()
		if e != nil {
			return e
		}
		if a.Store.Pending() {
			fmt.Fprintln(out, "存在待恢复事务；运行 aipm 恢复。")
		}
		for _, t := range provider.Tools {
			if args[0] == "status" {
				if e := v.Errors[t]; e != "" {
					fmt.Fprintf(out, "%s: 配置错误：%s\n", t, e)
				} else {
					s := v.Status[t]
					fmt.Fprintf(out, "%s: %s [%s]\n", t, provider.Display(s.Label), s.Kind)
					for _, w := range s.Warnings {
						fmt.Fprintln(out, "  "+provider.Display(w))
					}
				}
				fmt.Fprintln(out, "  "+provider.Display(a.Paths[t]))
				continue
			}
			fmt.Fprintf(out, "%s:\n  官方 / Official\n", t)
			for _, p := range v.Registry.Providers {
				if p.Tool == t {
					fmt.Fprintf(out, "  %s | %s | %s | %s\n", provider.Display(p.Name), provider.Display(p.BaseURL), provider.Display(p.Model), p.CredentialLabel())
				}
			}
		}
		return nil
	}
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("编号菜单需要交互终端；使用 aipm list 或 aipm status")
	}
	if err := a.Store.Recover(); err != nil {
		return err
	}
	return shell.RunTerminal(a, os.Stdin, out)
}
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "aipm: "+err.Error())
		os.Exit(1)
	}
}
