# GoAgentFramework (SAISON)

GoAgentFramework 是一个用 Go 从零实现的个人 Agent 项目，涵盖模型流式协议解析、工具调用与用户确认、SQLite 会话持久化，以及终端 CLI 和原生桌面 GUI 两种交互形态。项目开发遵循 [Agent 工作原则](AGENTS.md)。

## 当前状态

核心链路与功能已全部打通，当前已完成架构边界拆分与命名收口，下一阶段将聚焦桌面 GUI 的视觉与交互细节打磨（详见 [GUI 功能开发路线](docs/gui-roadmap.md) 与 [改进项清单](docs/improvements.md)）。

- **CLI 入口 (`cmd/cli`)**：支持新建或按 `sessionID` 恢复会话、流式输出思考与正文、工具调用前交互确认、单轮 Ctrl-C 安全取消与持久化。
- **桌面 GUI (`cmd/gui`)**：基于 [MyGo](https://github.com/egoist/mygo) 构建原生窗口（应用名 SAISON），支持多会话切换、懒创建新对话、重命名与删除会话、实时流式输出与可折叠思考块、工具执行确认卡片、会话内选项调节（思考开关/强度/工具开关）、应用级配置保存与停止生成。

## 目录与分层

依赖方向严格自底向上，核心层不反向依赖上层配置或交互实现：

| 目录 | 职责 |
| --- | --- |
| `internal/apis` | OpenAI 兼容 Chat Completions 流式客户端（`Client`）、SSE 解析、分片 `tool_calls` 组装与合法性校验 |
| `internal/tools` | 统一工具接口（`Tool`）、注册表（`Registry`）与内建 `Bash` 工具（含参数校验与用户确认回调） |
| `internal/storage` | SQLite 封装（`Store`）、表结构按 `PRAGMA user_version` 自动迁移、`sessions` / `messages` 持久化与历史加载 |
| `internal/config` | 环境变量/`.env` 加载（`LoadFromEnv`）及本地 `config.json` 读写（`Load` / `Save`，`0600` 权限原子写入） |
| `internal/agent` | 对话循环（`Loop`）、按轮传入的 `TurnRequest`、结构化 `OutputEvent` 回调以及中断/取消一致性保障 |
| `internal/interaction/cli` | 终端交互封装（`CLI`）：会话选择、输入读取、ANSI 分色渲染、历史回放与命令确认 |
| `internal/interaction/gui` | MyGo 桌面 GUI（按状态、主控、侧边栏、聊天区、消息、输入区、弹框拆分为 7 个文件） |
| `cmd/cli`、`cmd/gui` | 各自负责组装配置、存储、工具注册表与交互层并启动程序 |

## 核心契约

- **工具调用与中断一致性**：工具执行前必须经过用户确认；模型流式输出被取消时保留已生成内容落库，工具确认或执行被中断时自动为未完成的 `tool_call_id` 补写 `"已取消"` 结果，确保后续重载会话继续请求模型合法。
- **配置优先级（GUI）**：`config.json` 非空项 > `.env` / 环境变量 > 内置默认值（`DEEPSEEK_API_KEY`、`DEEPSEEK_ENDPOINT` 见 [.env.example](.env.example)）。`config.json`、`.env`、`agent.db` 均已加入 `.gitignore`，禁止提交到版本库。

## 运行与测试

```bash
# 1. 复制环境变量模板并填入 API Key（CLI 必填；GUI 也可启动后在“设置”中填写）
cp .env.example .env

# 2. 启动 CLI 或桌面 GUI
go run ./cmd/cli
go run ./cmd/gui

# 3. 运行静态检查与全仓竞态测试
go vet ./...
go test -race ./...
```
