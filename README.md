# GoAgentFramework (SAISON)

GoAgentFramework（应用名 **SAISON**）是一个用 Go 从零实现的个人 Agent 项目，涵盖模型流式协议解析、多模态图片与文档解析（Files API）、本地命令与联网工具调用、SQLite 会话持久化，以及终端 CLI 和原生桌面 GUI 两种交互形态。项目开发遵循 [Agent 工作原则](AGENTS.md)。

## 当前状态

核心链路、多模态附件、联网检索与原生桌面 GUI 均已落地；下一阶段将聚焦 Token 统计、System Prompt、上下文压缩、`/` 斜杠命令、Skills 渐进式加载与 MCP 注册发现（详见 [下一阶段能力演进计划](docs/agent-roadmap.md)，历史里程碑见 [GUI 功能开发路线](docs/gui-roadmap.md) 与 [改进项清单](docs/improvements.md)）。

- **CLI 入口 (`cmd/cli`)**：支持新建或按 `sessionID` 恢复会话、流式输出思考与正文、本地命令执行前交互确认、联网搜索与网页抓取、单轮 Ctrl-C 安全取消与持久化，历史回放兼容展示图片/文件附件占位。
- **桌面 GUI (`cmd/gui`)**：基于 [MyGo](https://github.com/egoist/mygo) 构建原生跨平台窗口（支持 macOS / Windows），支持：
  - 多会话隔离与可折叠侧边栏、新建会话懒创建、重命名与删除；
  - 实时流式 Markdown 渲染、可折叠思考块、长工具参数/输出折叠与滚动保护、工具执行确认卡片与随时停止生成；
  - 会话级独立选项浮层（思考开关与强度 `low`/`high`/`max`、本地命令开关、联网搜索开关、文档图表识别开关）及应用级配置热更新持久化；
  - 剪贴板截图/图片粘贴（内联 Base64）、`+ 附件` 多选与拖拽上传（DeepSeek Files API 独立后台预上传，支持进度/失败重试/取消清理与本地预览打开）。

## 目录与分层

依赖方向严格自底向上，核心层不反向依赖上层配置或交互实现：

| 目录 | 职责 |
| --- | --- |
| `internal/apis` | OpenAI/DeepSeek 兼容流式客户端（`Client`）、SSE 解析与分片 `tool_calls` 组装、多模态图片与 Files API 上传/删除、联网 Search/Fetch 接口；`types.go` 集中对外共享领域契约 |
| `internal/tools` | 统一工具接口（`Tool`）、注册表（`Registry`）、跨平台本地命令工具（`bash` / Windows `powershell`，含参数校验与用户确认）以及联网工具（`web_search`、`web_fetch`） |
| `internal/storage` | SQLite 封装（`Store`）、表结构按 `PRAGMA user_version` 自动迁移（当前 v3）、`sessions` / `messages` 持久化与历史加载，以及 `attachments/pic`、`attachments/file` 本地附件原子写入与级联清理 |
| `internal/config` | 环境变量/`.env` 加载（`LoadFromEnv`）及本地 `config.json` 读写（`Load` / `Save`，`0600` 权限原子写入） |
| `internal/agent` | 对话循环（`Loop`）、按轮传入的 `TurnRequest`（含文本、图片、文件附件）、结构化 `OutputEvent` 回调以及中断/取消一致性保障 |
| `internal/interaction/cli` | 终端交互封装（`CLI`）：会话选择、输入读取、ANSI 分色渲染、历史回放与命令确认 |
| `internal/interaction/gui` | MyGo 原生桌面 GUI，按纯状态（`state.go`）、应用主控（`app.go`）、附件预上传编排（`attachments.go`）、缩略图与文件嗅探（`image.go`）、样式原语（`style.go`）及各视图组件（`sidebar.go`、`chat.go`、`message.go`、`composer.go`、`dialog.go`）分层组织 |
| `cmd/cli`、`cmd/gui` | 各自负责组装配置、存储、工具注册表与交互层并启动程序 |

## 核心契约

- **工具调用与中断一致性**：本地命令执行前必须经过用户确认（只读联网工具默认免确认）；流式输出被取消时保留已生成内容落库，工具确认或执行被中断时自动为未完成的 `tool_call_id` 补写 `"已取消"` 结果，确保后续重载会话继续请求模型合法。
- **附件双通道策略**：
  - 剪贴板粘贴截图/图片走内联 Base64，发送时原子落盘至 `attachments/pic/<sessionID>/`；
  - `+ 附件` 与拖拽文件走 DeepSeek Files API，每个文件独立 goroutine 后台预上传（默认 30 天有效期），PDF/Word/PPT 默认 `text_recognition` 纯文本解析（可在会话选项中按需勾选「识别文档图表」切换 `rich_layout_recognition`）；移除草稿、切换凭据或删除会话时自动清理对应云端 `file_id` 与本地副本。
- **配置优先级与隐私边界**：GUI 配置遵循 `config.json` 非空项 > `.env` / 环境变量 > 内置默认值（环境变量模板见 [.env.example](.env.example)）。`config.json`、`.env`、`agent.db`、`attachments/` 与 `/bin/` 均已加入 [.gitignore](.gitignore)，禁止提交任何密钥或本地会话数据到版本库。

## 运行、构建与测试

```bash
# 1. 复制环境变量模板并填入 API Key（CLI 必填；GUI 也可启动后在“设置”中填写）
cp .env.example .env

# 2. 直接运行 CLI 或桌面 GUI
go run ./cmd/cli
go run ./cmd/gui

# 3. 编译可执行文件（输出到 .gitignore 已忽略的 bin/ 目录）
go build -ldflags="-s -w" -o bin/SAISON ./cmd/gui
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H=windowsgui" -o bin/SAISON.exe ./cmd/gui

# 4. 运行静态检查与全仓竞态测试
go vet ./...
go test -race ./...
```
