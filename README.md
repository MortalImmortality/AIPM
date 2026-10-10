# aipm

用于 **Codex CLI** 和 **Claude Code CLI** 的第三方 provider 管理工具，采用普通 shell 编号菜单交互，不清屏、不占用备用屏幕。命令为 `aipm`，支持新增、查看、编辑、删除、切换，以及切回官方路由。

这是本地配置管理工具，不是 API 网关，不发送模型请求，也不接管客户端登录。

## 构建与运行

需要 Go 1.26 或更高版本。首版面向 macOS / Linux。

```sh
go mod download
go build -o bin/aipm ./cmd/aipm
./bin/aipm
```

可将二进制放到自己的 PATH 目录，或在项目内运行以下命令安装到 Go bin 目录：

```sh
go install ./cmd/aipm
```

确保 `$(go env GOPATH)/bin` 在 PATH 中，然后运行 `aipm`。项目不会自动安装二进制或修改 shell profile。

只读命令无需交互终端：

```sh
aipm list
aipm status
aipm --help
aipm --version
```

`status` 显示实际用户配置，不仅是上次在 aipm 中选择的 provider；外部修改可能显示为外部配置。只读命令不会自动恢复未完成事务。

## Shell 交互

运行 `aipm` 后输入编号并回车：

```text
1 Codex
2 Claude
0 退出
客户端>
```

选择客户端后：

```text
1 查看 provider
2 新增 provider
3 编辑 provider
4 删除 provider
5 切换 provider
6 恢复官方
0 返回
操作>
```

编辑、删除、切换使用保存的 provider 编号（从 1 开始），`0` 取消选择。官方恢复使用操作 `6`，不出现在可编辑或可删除列表中。

- 表单逐项询问名称、URL、模型、凭据模式；只询问该模式需要的密钥或环境变量名。
- 编辑时回车保留原值；可选非密钥字段输入 `/clear` 清空。密钥输入不回显，也不显示旧密钥。
- 任意提示输入 `/cancel` 取消当前操作；Ctrl+C 或 EOF 退出。
- 写入前显示文件路径、变更字段和警告。输入 `y` 或 `yes` 确认；回车默认为 **不执行**。
- 菜单每次重新读取实际配置，历史输出保留在终端滚动记录中。

新增 provider 只保存定义，**不会立即切换客户端**。使用操作 `5` 选择并应用。活跃 provider 禁止直接删除；请先切回官方或切到其他 provider。编辑活跃 provider 需确认并同步更新客户端配置。

请等待密钥提示后再输入，勿将含密钥的整段操作提前粘贴到终端；尚未进入隐藏输入模式的内容可能被终端回显。交互模式不接受管道输入，脚本查询可使用 `list` / `status`。

### Codex provider

需要名称、Base URL、模型和凭据模式。仅支持 **Responses API**，`wire_api = "responses"`；不支持仅提供 Chat Completions API 的端点。

- `env`：推荐方式。填写已有环境变量名称，例如 `MY_CODEX_KEY`，配置写入 `env_key`。环境变量的值不保存在 aipm registry。需在启动 Codex 的环境中设置该变量；aipm 不修改 shell profile。
- `token`：在不回显的密钥提示中填写 token，配置写入官方支持的实验字段 `experimental_bearer_token`。此字段仍为实验配置，官方推荐环境变量方式。

Codex 配置中的 `model_provider` 与 `model_providers` 表键使用精确的 `anim_<名称>`，例如名称 `Gateway` 对应 `anim_Gateway`。名称不做大小写转换、空格替换或 slug 化；空格、中文、点号等由 TOML 编辑器按单个字面键正确引用。registry 内部随机 ID 保持不变，同一客户端的名称仍按忽略大小写规则去重。不会覆盖内置 provider，不会写入 `auth.json`，也不会修改系统 keyring。

兼容旧版 `aipm_<内部ID>` 配置：`status` 与活跃 provider 检查识别新旧两种键，下一次明确切换或编辑活跃 provider 时写入新键；启动、只读查询和新增定义不会自动迁移。活跃重命名同步更新路由与表键。只有表内容与注册定义完全匹配，或与该路径的成功操作快照哈希一致时，才清理旧表；仅有 `aipm_` / `anim_` 前缀不足以证明归属。目标新键已有不同的外部表时拒绝应用，不覆盖外部配置。

迁移沿用配置备份与 config/registry 事务保护；失败后不要删除未完成事务记录，按现有恢复流程处理。旧版 aipm 不识别新 `anim_` 键，降级前可先用当前版本恢复官方，再由旧版重新切换；如需手动回滚，请从对应备份提取旧路由和表项，不要用整份历史备份覆盖后续无关修改。

### Claude provider

需要 **Anthropic Messages API 兼容的网关**。仅有 OpenAI-compatible 接口的端点不能直接用于此模式。官方网关文档不支持将 Claude Code 通过网关路由至非 Claude 模型；aipm 不提供协议转换或保证此类用法兼容。

- `token`：写入 `ANTHROPIC_AUTH_TOKEN`，发送 `Authorization: Bearer`。
- `api_key`：写入 `ANTHROPIC_API_KEY`，发送 `x-api-key`。Claude Code 可能需要一次交互批准此 API key。

Base URL 写入 `env.ANTHROPIC_BASE_URL`。模型字段可选，另外可配置 Opus / Sonnet / Haiku / Fable 别名与默认模型。保留无关 permissions、hooks、plugins、statusLine 和环境变量。

已有 `apiKeyHelper` 或云 provider 选择配置时，工具会拒绝切换并提示冲突；不会擅自删除认证脚本或云认证设置。首版不管理 Bedrock / Vertex / Foundry / Claude Platform on AWS。

## 官方恢复与配置优先级

“官方”是不可编辑、不可删除的内置选项。恢复是针对路由、凭据和模型字段的局部修改，**不是用旧备份替换整份文件**。

- Codex：选择内置 `openai`，清理相关第三方路由覆盖。保留已存官方登录，不自动执行 `codex login`。
- Claude：清理第三方 Base URL、凭据和模型覆盖，保留已存 OAuth 登录。不会执行 `/logout`。
- 从明确官方配置首次切出时，保存该目标路径的官方模型偏好；切回时恢复。若最初已是外部第三方配置，则不能推断以前的官方模型，恢复时使用客户端默认值。
- 没有有效官方登录时，仍需使用客户端自己的官方登录流程。官方路由不等于一定使用订阅登录；官方 API key 或继承环境变量仍可能决定认证方式。

**切换后重启客户端。** aipm 只修改用户配置，不能保证覆盖所有会话的有效配置：

- Codex profile、命令行配置及启动环境可能改变路由。
- Claude 项目设置、managed settings、命令行和其他 provider 环境变量可能覆盖用户配置；官方文档说明，同名用户 settings `env` 值优先于 shell 值。
- aipm 不修改项目设置、managed policy、shell profile，也不强制结束正在运行的客户端。

## 文件与安全限制

| 数据 | 默认位置 |
| --- | --- |
| aipm registry | `os.UserConfigDir()/aipm`；macOS 通常为 `~/Library/Application Support/aipm`，Linux 通常为 `$XDG_CONFIG_HOME/aipm` 或 `~/.config/aipm` |
| Codex | `~/.codex/config.toml` |
| Claude | `~/.claude/settings.json` |

可用绝对路径环境变量覆盖：`AIPM_HOME`、`CODEX_HOME`、`CLAUDE_CONFIG_DIR`。官方模型快照与解析后的客户端配置路径绑定。

**直接输入的密钥以明文保存。** 输入隐藏只防止终端回显，不是加密。aipm 用户目录权限为 `0700`，registry、配置、事务记录与备份为 `0600`。客户端配置和历史备份也可能含密钥；切回官方或删除 provider 不会自动清除历史备份。备份放在目标配置同目录，使用 `.aipm-backup-*` 文件名。

工具先解析配置、预写备份，再原子替换文件；拒绝损坏配置和不安全符号链接。应用锁防止多个 aipm 实例同时写入，内容检查检测已观察到的外部修改。外部编辑器不遵守该锁，不能保证消除所有外部写入竞争。客户端配置与 registry 通过事务记录协调；启动交互菜单时恢复中断事务，存在外部冲突时停止，不覆盖新的用户修改。

Codex 使用局部 TOML 编辑，保留无关注释、空白和顺序。Claude JSON 保留未知字段及其值，但可能规范化缩进，不保证原始字节不变。密钥不会出现在 `list` / `status` 或应用预览中。

## 开发

```sh
make build
make test
make race
make vet
make fmt-check
```

等价命令：

```sh
go build -o bin/aipm ./cmd/aipm
go test ./...
go test -race ./...
go vet ./...
```

运行单个测试：

```sh
go test ./internal/config -run TestCodexRoundTrip -v
```

测试使用临时配置和假凭据，不读取真实用户密钥，不发模型请求。`make smoke` 使用 Python 3 标准库 PTY 驱动真实终端菜单，验证完整操作与隐藏输入中断后的终端恢复，无需 tmux。TOML 编辑依赖 `go-toml/v2/unstable/edit`，已固定版本；升级前必须重新验证格式保留测试。

## 官方文档依据

实现前查阅的配置文档（2026-10-10）：

- [Codex 高级配置](https://developers.openai.com/codex/config-advanced)（官方当前重定向至 `learn.chatgpt.com`）
- [Codex 配置参考](https://developers.openai.com/codex/config-reference)
- [Codex 认证](https://developers.openai.com/codex/auth)
- [Claude Code 网关连接](https://code.claude.com/docs/en/llm-gateway-connect)
- [Claude Code 设置优先级](https://code.claude.com/docs/en/settings)
- [Claude Code 环境变量](https://code.claude.com/docs/en/env-vars)

客户端配置协议可能变化；更新客户端或依赖后，需检查当前官方文档与本工具兼容性。
