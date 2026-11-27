# 改进项清单

基于 2026-11-20 的代码审查整理。CLI 链路（`cmd/cli` → `interaction/cli` → `agent` → `apis` → `storage` / `tools` / `config`）为主，GUI 仍在开发中，单列一节。

## 建议推进顺序

每一步都可独立验证、单独提交。

1. [X] 修复「tool_calls 无配对结果」bug（见 §0）
2. [X] `apis`：抽出 `Client`，去掉对 `config` 的依赖与全局 client，顺手改文件名（§1.1、§1.2；保留 `apis` 包名）
3. [X] `storage`：改为 `Store` 类型，收回 JSON 序列化与消息类型转换（§1.4）
4. [X] `tools`：工具定义、参数解析、执行合到一个包里，消除两个 main 中重复的 schema（§1.3）
5. [X] `agent`：`RunTurn` 直接接收输入，输出改为结构化事件（§2），GUI 也随之受益
6. [X] 统一修正命名（§6），建议单独一个 commit

---

## 0. 需优先修复的 bug

- [X] **tool_calls 没有配对的 tool 结果**：`RunTurn` 先把带 `tool_calls` 的 assistant 消息写入数据库，再逐个执行工具。中途一旦出错（确认时读到 EOF、ctx 取消、写库失败），库里就会留下没有对应 tool 结果的 `tool_calls`。之后 `BindSession` 这个会话再请求模型，OpenAI 兼容接口会直接返回 400。（已采用方案 A：中断/取消时通过 `cancelRemainingToolCalls` 补写 `"已取消"` 结果，并补齐 `TestRunTurnConfirmInterrupted`）
  - 方案 A：出错时为每个未完成的 `call_id` 补一条「已中断」的 tool 消息。
  - 方案 B：assistant 消息和它的全部 tool 结果在同一个事务里一起提交。
  - 需补测试：确认阶段读到 EOF、ctx 取消后，重新 `BindSession` 得到的历史依然能合法发给模型。

---

## 1. 包结构与依赖方向

### 1.1 `apis` 反向依赖 `config`

- [X] `apis.StreamChat(ctx, msgs, opts, cfg config.Config)` 让底层协议客户端依赖了上层的应用配置。
- [X] `sse.go` 里的全局 `var client = initHttpClient()` 说明缺少一个客户端对象。

- 建议：

  ```go
  type Client struct {
      Endpoint, APIKey, Model string
      HTTP *http.Client
  }
  func (c *Client) StreamChat(ctx context.Context, msgs []Message, opts StreamOptions) iter.Seq2[StreamEvent, error]
  ```

  由 main 根据 config 组装 `Client`，`agent` 不再需要保存 `cfg`。

### 1.2 包名与文件名

- [X] `apis` → `llm` 或 `chat`。（经讨论决定保留 `apis` 包名）
- [X] `apis/init.go` 改名。`init` 在 Go 中有特殊含义，按内容改为 `client.go` / `request.go`。（已改为 `client.go`）
- [X] `storage/types.go`、`crud.go` 按领域拆分：`session.go`、`message.go`、`migrate.go`。（已拆为 `sqlite.go`、`session.go`、`message.go`、`schemas.go`）

### 1.3 工具相关代码散落在三处

| 内容                                                | 现在所在位置                                            |
| --------------------------------------------------- | ------------------------------------------------------- |
| schema 定义                                         | `cmd/cli/main.go`，`cmd/gui/main.go` 里还有一份副本 |
| 参数结构 `bashArgs`、按名称分发 `switch "bash"` | `agent/loop.go`                                       |
| 实际执行                                            | `tools/gitbash.go`                                    |

- [X] 由 `tools` 包统一负责「定义 + 参数解析 + 执行」，`agent` 只依赖接口：（已实现 `tools.Tool` / `tools.Registry` / `tools.Bash`，文件改为 `bash.go`）

  ```go
  type Tool interface {
      Definition() llm.Tool
      Run(ctx context.Context, args json.RawMessage) (string, error)
  }
  ```

  用户确认是所有工具共用的逻辑，留在 `agent`；工具只需提供确认时展示的内容。
- [ ] Windows 上 `exec.Command("bash", …)` 可能找到 WSL 的 `System32\bash.exe`，而不是 Git Bash，需要显式配置 Git Bash 路径。

### 1.4 `storage` 把序列化细节泄露给调用方

- [X] `storage.Message.ToolCalls` 是 JSON 字符串，导致 `agent.appendMessage` 要做 Marshal、`agent.LoadHistory` 要做 Unmarshal。序列化方式应由存储层自己处理。
- [X] 空值 `"[]"` 这个默认值出现在三处：`appendMessage`、`CreateMessage`、建表语句的 DEFAULT。（已收敛至 `storage` 层内）
- [X] 每个函数都要显式传 `*sql.DB`。改为 `type Store struct{ db *sql.DB }` 加方法，避免 `*sql.DB` 泄露到 agent 和 main；`agent` 依赖一个小接口即可。
  - 好处：`agent` 目前的测试都要启动真实 SQLite 和 httptest，本质上是集成测试。有了接口才能写真正的单元测试。
- [X] `agent.LoadHistory` 做的是 `storage.Message → apis.Message` 的转换，应该挪到存储层。
- [X] `messages.session_id` 没有外键约束。SQLite 默认也不检查外键，需要 `PRAGMA foreign_keys = ON`。（`storage.Open` 已启用 `PRAGMA foreign_keys = ON`；v1 建表 DDL 保持兼容，删会话通过单事务联动清理）
- [X] `PRAGMA user_version` 建议在迁移循环里统一设置，而不是写进每个版本的 DDL。

### 1.5 导出的标识符过多

- [X] `ChatResponseChunk`、`ChatRequest` 只在包内和测试中使用，同包测试不导出也能访问，改为不导出。
- [X] `storage.Schema` 类型、`ApiCall` 结构体目前没有用到。（已移除）
- [X] `AgentLoop.SessionId` 是导出字段，外部修改后会和 `msgs` 不一致。改为私有字段，另提供 `SessionID()` 方法读取。

---

## 2. `agent` 接口设计

### 2.1 `RunTurn` 主动拉取输入

- [X] `RunTurn(ctx, inputFunc, outputFunc, confirmFunc)` 由 loop 自己读取输入，带来两个问题：（已改为 `RunTurn(ctx, TurnRequest)`，直接接收 `Input` 及按轮生效的 `Client`/`Opts`/`WorkDir`/`Output`/`Confirm`）
  - 退出信号 `io.EOF` 一路穿过 `RunTurn` 传回 main。确认阶段读到 EOF 也会被 main 当作正常退出（与 §0 相关）。
  - GUI 只能伪造一个直接返回草稿的 `inputFunc`。

- 建议：调用方负责读取输入，loop 只负责推进一轮对话。

  ```go
  func (l *Loop) RunTurn(ctx context.Context, input string) error
  ```

  输出、确认这类不随轮次变化的依赖，在构造 loop 时注入。

### 2.2 回调与枚举

- [X] `ConfirmFunc func(context.Context, string, string)` 中两个 string 分不清哪个是目录、哪个是命令。在类型里写上参数名：`func(ctx context.Context, dir, command string) (bool, error)`。
- [X] `OutputRole` 和消息的 role 概念重名，改为 `OutputKind`。`IsContent`、`IsReasoningContent` 读起来像布尔值，改为 `OutputContent`、`OutputReasoning`、`OutputToolResult`。它和 `apis.EventKind` 大体重复，可以考虑合并。
- [X] 输出改为结构化事件（GUI 需要实时显示工具调用）：（已升级为 `OutputEvent`，含 `OutputToolCall`）

  ```go
  type Event struct {
      Kind     EventKind // Reasoning / Content / ToolCall / ToolResult
      Text     string
      ToolCall *llm.ToolCall
  }
  ```

### 2.3 循环控制

- [X] `RunTurn` 先设 `needReply = false`，再用 `output.Role == ""` 表示空消息，最后靠 `continue` 退出，读起来绕。改为在判断处直接 return：

  ```go
  for {
      reply, finish, err := l.readReply(ctx)
      // 处理 err
      if finish != "tool_calls" {
          if !reply.IsEmpty() { /* 追加 */ }
          return nil
      }
      // 追加 reply、执行工具……
  }
  ```
- [ ] `finishReason` 定义为具名类型加常量。`length`、`content_filter` 目前被当作正常结束，没有任何提示。
- [X] `os.Getwd()` 每次工具调用都会执行。工作目录应作为 loop 的字段或配置项（GUI 以后可能需要按会话指定）。（已支持 `TurnRequest.WorkDir`，为空时回退 `os.Getwd()`）

### 2.4 职责

- [X] `readReply` 里的 `splitMark = "\n"` 是 CLI 的排版需求，不应出现在 agent 层。另外 `if reasoning == true` 直接写 `if reasoning`。
- [X] `apis.CheckToolCalls` 由 agent 调用。可以考虑放到流式层，保证发出 `EventFinish` 时工具调用已经合法，agent 就不用记着去调它。
- [ ] 会话管理（`NewSession`、`BindSession`、`LoadHistory`）和对话推进（`RunTurn`）在同一个类型里，现阶段可以接受，后续可以拆开。
- [X] 用一句注释写清 `runToolCall` 的错误约定：返回字符串的是交给模型处理的错误，返回 `error` 的是程序级错误。

---

## 3. `interaction/cli`

| 状态   | 位置                           | 问题                                                                             | 建议                                                                     |
| ------ | ------------------------------ | -------------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| 已完成 | `Cli` / `NewCli`           | 缩写词大小写不规范，且与包名重复                                                 | `cli.CLI`、`cli.New`                                                 |
| 已完成 | `Scanner` 导出、`out` 私有 | 可见性不一致                                                                     | 都改为私有                                                               |
| 已完成 | `ConfirmOperateByUser`       | 英文用法别扭，名字冗长                                                           | `Confirm`                                                              |
| 已完成 | `session.go` 第 11 行        | 用 `fmt.Println` 输出，没有写到 `c.out`，测试捕获不到                        | `fmt.Fprintln(c.out, …)`                                              |
| 已完成 | `SelectSession` 返回值       | 用 `-1`、`0` 表示特殊状态；输入 `0` 或负数时返回 `(-1, nil)`，错误被吞掉 | 返回 `(id int64, isNew bool, err error)`，并定义 `ErrInvalidSession` |
| 已完成 | `PrintHistory`               | 忽略了 `WriteOutput` 返回的 error                                              | 返回 error，或注释说明为什么忽略                                         |
| 已完成 | ANSI 转义码                    | 散落在函数内                                                                     | 提成包级常量                                                             |
| 说明   | `ReadInput(ctx)`             | `Scanner` 阻塞读取时无法响应 ctx                                               | 可以不改，但要清楚这里的 ctx 实际不起作用                                |

---

## 4. `cmd/cli/main.go`

- [X] `log.Fatal` 会调用 `os.Exit`，导致 `defer db.Close()` 不执行。改为 `func main() { if err := run(); err != nil { log.Fatal(err) } }`。（`cmd/gui/main.go` 同步收口）
- [X] `selectDone` 标志位循环可以抽成 `chooseSession(...)` 函数。
- [X] 用 `signal.NotifyContext(ctx, os.Interrupt)` 处理 Ctrl-C：取消当前请求，而不是直接杀掉进程。（仅在单轮 `RunTurn` 期间拦截，避免阻塞空等输入退出）
- [ ] 硬编码的 `"agent.db"`、会话标题 `"test"`、thinking/effort 参数移到 config。
- [X] 第 74 行拼写错误：`sesionId`。

## 5. `config`

- [X] `NewConfigFromEnv` 改名为 `config.Load()` 或 `config.FromEnv()`。（已改为 `config.LoadFromEnv()`，并与 `config.Load(path)` / `config.Save(path, cfg)` 配套）
- [ ] `DefaultModel` 从未从环境变量读取，`if cfg.DefaultModel == ""` 恒为真。二选一：增加 `DEEPSEEK_MODEL` 环境变量，或者删掉这个判断。字段名可简化为 `Model`。（经讨论保留 `DefaultModel` 字段与 `json:"default_model"` 兼容；如需支持 `DEEPSEEK_MODEL` 可后续补齐）

---

## 6. 命名与注释

- [X] 缩写词：`Id → ID`、`Api → API`、`Http → HTTP`、`Cli → CLI`。涉及 `SessionId`、`ToolCallId`、`Id`、`ApiCall`、`initHttpClient` 等，JSON tag 不受影响。
- [X] 与包名重复：`agent.AgentLoop → agent.Loop`，`NewAgentLoop → agent.NewLoop`。
- [ ] 导出标识符的文档注释以名称开头，例如 `// RunTurn 推进一轮对话……`。（部分已补齐，按需持续完善）
- [ ] 删掉 `// 发送请求`、`// 初始化Header` 这类复述代码的注释，注释用来解释为什么这样做。
- [X] 错误信息要能区分出错环节：`crud.go` 中更新 sessions 失败也报「插入 messages 失败」。`ListSessions` 第一处返回的错误没有包装，和其他地方不一致。

---

## 7. GUI（开发中，逐步改进）

- [X] **按会话收拢状态**：`loops`、`history`、`running` 三个 map 用同一个 key，`sessionErr` 却是全局的一个。合并为 `map[int64]*sessionState`：

  ```go
  type sessionState struct {
      loop      *agent.Loop
      msgs      []*chatMessage // 已提交的消息
      streaming *chatMessage   // 正在流式生成的消息
      running   bool
      err       error
      confirm   *confirmRequest
  }
  ```
- [X] **历史只保留一个来源**：`w.history` 由回调拼出来，`AgentLoop.msgs` 才是写入数据库的版本，两者会不一致（实时显示时看不到工具调用；流中断后界面上残留半截回复；同一会话被读库两次）。已提交的消息以 loop/DB 为准，界面只额外维护正在流式生成的那一条；每轮结束后用 `loop.History()` 的快照替换。（已通过 `syncHistory` 与单来源对齐）
- [X] **命令确认**：`confirmFunc` 目前固定返回 false。改为用确认卡片加 channel，后台 goroutine 在 channel 上阻塞，同时 `select` ctx 取消。
- [X] **错误展示**：`fmt.Printf` 输出到 stdout，界面上看不到，应写入会话的 `err` 并渲染出来。「切换会话失败」这条提示也被提交失败共用了。
- [X] **新建会话入口**：数据库为空时 `selectedSession.Id == 0`，点「提交」没有任何反应。（已实现草稿态懒创建）
- [X] **生成中状态与取消**：按钮应禁用或显示「生成中」，并提供停止按钮（每轮使用独立 ctx）；关闭窗口时取消后台 goroutine。
- [X] **分层**：GUI 已拆分为 7 个文件实现纯状态与视图分层并补齐 `go test -race` 单测（会话列表与历史加载当前通过 `storage.Store` 访问）。
- [X] **视觉**：去掉 `[用户] xxx\n` 这类 CLI 风格的前缀和换行，按角色区分样式；侧边栏高亮当前会话；输入框和按钮放在同一个 `Row` 中（`TextArea` 设 `Grow(1)`），支持回车发送；流式输出时自动滚动到底部。（见 `gui-roadmap.md §8`）
- [X] `submitButton` 中的 `TrimSpace` 判断重复了一次。

---

## 8. 仓库与文档

- [X] `go.mod` 中的直接依赖都被标为 `// indirect`，运行 `go mod tidy`。
- [ ] `.gitignore` 中的 `*.txt` 范围过大；`bash_target.txt` 这类测试文件放到 `testdata/`。
- [X] README 中的「当前状态」仍写着 MVP 尚未实现，已过时。
- [X] `AGENTS.md` 和 `CLAUDE.md` 内容完全相同，只保留一份，另一份用软链接或引用。（已将 `CLAUDE.md` 改为指向 `AGENTS.md` 的相对软链接）
