package gui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var reasoningEfforts = []string{"low", "high", "max"}

func (a *app) view(c *ui.Context) {
	ui.Row(c).Fill().Children(func() {
		a.sidebar(c)
		a.chat(c)
	})
	a.renameModal(c)
	a.deleteModal(c)
}

func (a *app) sidebar(c *ui.Context) {
	ui.Column(c).Width(200).FillHeight().Children(func() {
		if ui.Button(c, "新建会话").Clicked() {
			a.sessions.selectedId = 0
			a.err = nil
		}
		for _, session := range a.sessions.list {
			ui.Row(c.Key(session.Id)).FillWidth().Children(func() {
				if ui.Button(c, session.Title).Grow(1).Clicked() {
					_ = a.selectSession(session.Id)
				}
				ui.PopoverMenuButton(c, "...", func(m *ui.Menu) {
					if m.Item("重命名").Chosen() {
						a.renameDialog = renameDialog{
							open:      true,
							sessionId: session.Id,
							title:     session.Title,
						}
					}
					if m.Item("删除").Chosen() {
						a.deleteDialog = deleteDialog{
							open:      true,
							sessionId: session.Id,
							title:     session.Title,
						}
					}
				}).Label(fmt.Sprintf("更多操作 %d", session.Id))
			})
		}
	})
}

func (a *app) renameModal(c *ui.Context) {
	ui.Modal(c, &a.renameDialog.open, func() {
		ui.Column(c).Children(func() {
			ui.Text(c, "重命名会话")
			submitted := ui.TextInput(c.Key(a.renameDialog.sessionId), &a.renameDialog.title).Submitted()
			if a.renameDialog.err != nil {
				ui.Text(c, a.renameDialog.err.Error())
			}
			ui.Row(c).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					a.renameDialog.open = false
					a.renameDialog.err = nil
				}
				if ui.Button(c, "保存").Clicked() || submitted {
					if err := a.renameSession(a.renameDialog.sessionId, a.renameDialog.title); err != nil {
						a.renameDialog.err = err
						return
					}
					a.renameDialog.open = false
					a.renameDialog.err = nil
				}
			})
		})
	})
}

func (a *app) deleteModal(c *ui.Context) {
	ui.Modal(c, &a.deleteDialog.open, func() {
		ui.Column(c).Children(func() {
			ui.Text(c, "删除会话")
			ui.Text(c, fmt.Sprintf("确定删除“%s”吗？", a.deleteDialog.title))
			if a.deleteDialog.err != nil {
				ui.Text(c, a.deleteDialog.err.Error())
			}
			ui.Row(c).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					a.deleteDialog.open = false
					a.deleteDialog.err = nil
				}
				if ui.Button(c, "确认删除").Clicked() {
					if err := a.deleteSession(a.deleteDialog.sessionId); err != nil {
						a.deleteDialog.err = err
						return
					}
					a.deleteDialog.open = false
					a.deleteDialog.err = nil
				}
			})
		})
	})
}

func (a *app) chat(c *ui.Context) {
	curSession, curSessionState, ok := a.selectedSessionState()
	if !ok {
		ui.Column(c).Grow(1).FillHeight().Children(func() {
			ui.Text(c, "暂无会话")
		})
		return
	}

	chatMsgs := curSessionState.messages

	ui.Column(c).Grow(1).FillHeight().Children(func() {
		ui.Text(c, curSession.Title) // 标题栏

		if curSessionState.err != nil {
			ui.Text(c, curSessionState.err.Error())
		}
		a.scrollChat(c, curSession.Id, chatMsgs) // 对话记录栏
		if curSessionState.confirm != nil {
			a.renderConfirm(c, curSessionState) // 工具调用确认
		}
		ui.TextArea(c.Key(inputKey(curSession.Id)), &curSessionState.inputText) // 输入框
		ui.Row(c).Children(func() {
			a.sessionOptsPopover(c, curSessionState)          // 会话选项
			a.submitButton(c, curSession.Id, curSessionState) // 发送按钮
		})
	})
}

func (a *app) scrollChat(c *ui.Context, sessionId int64, chatMsgs []*chatMessage) {
	ui.Scroll(c.Key(scrollKey(sessionId))).Grow(1).Children(func() {
		for _, chatMsg := range chatMsgs {
			a.renderMessage(c, chatMsg)
		}
	})
}

func (a *app) renderMessage(c *ui.Context, msg *chatMessage) {
	ui.Column(c.Key(msg)).Children(func() {
		switch msg.Role {
		case "user":
			ui.Text(c, fmt.Sprintf("[用户] %s\n", msg.Content))
		case "assistant":
			if msg.ReasoningContent != "" { //思考内容
				ui.Collapsible(
					c,
					"思考",
					&msg.reasoningOpen,
					func() {
						ui.Text(c, fmt.Sprintf("[思考内容] %s\n", msg.ReasoningContent))
					},
				)
			}
			if msg.Content != "" { // 正文内容
				ui.Text(c, fmt.Sprintf("[回复] %s\n", msg.Content))
			}
			for _, call := range msg.ToolCalls { // 工具调用
				ui.Text(c, fmt.Sprintf("[工具调用] %s %s\n", call.Function.Name, call.Function.Args))
			}
		case "tool": // 工具结果
			ui.Text(c, fmt.Sprintf("[工具结果] %s\n", msg.Content))
		default:
			ui.Text(c, fmt.Sprintf("%s\n", msg.Content))
		}
	})
}

func (a *app) sessionOptsPopover(c *ui.Context, state *sessionState) {
	optsButton := ui.Button(c, "选项")
	if optsButton.Clicked() {
		a.optsOpen = !a.optsOpen
	}

	ui.Popover(c, optsButton, &a.optsOpen, func() {
		ui.Column(c).Children(func() {
			ui.Text(c, fmt.Sprintf("模型: %s", a.cfg.DefaultModel))

			ui.Checkbox(c, &state.opts.Thinking, "思考")
			if state.opts.Thinking {
				effortIdx := slices.Index(reasoningEfforts, state.opts.ReasoningEffort)
				if effortIdx < 0 { // 未设强度时默认 low
					effortIdx = 0
					state.opts.ReasoningEffort = reasoningEfforts[0]
				}
				if ui.Segmented(c, &effortIdx, reasoningEfforts...).Changed() {
					state.opts.ReasoningEffort = reasoningEfforts[effortIdx]
				}
			}

			if len(a.defaultOpts.Tools) > 0 {
				toolsOn := len(state.opts.Tools) > 0
				if ui.Checkbox(c, &toolsOn, "允许工具调用").Changed() {
					state.setToolsEnabled(toolsOn, a.defaultOpts.Tools)
				}
			}
		})
	})
}

func (a *app) submitButton(c *ui.Context, sessionId int64, state *sessionState) {
	if state.sessionRunning {
		if ui.Button(c, "停止").Clicked() && state.cancel != nil {
			state.cancel()
			state.cancel = nil
		}
		return
	}
	if ui.Button(c, "提交").Clicked() {
		if strings.TrimSpace(state.inputText) == "" {
			return
		}
		a.submit(sessionId, state.inputText)
	}
}

func (a *app) lazyCreateSession(input string) (int64, error) {
	title := makeSessionTitle(input)
	id, err := storage.CreateSession(a.ctx, a.db, title)
	if err != nil {
		return 0, fmt.Errorf("新建会话失败: %w", err)
	}

	curSession := a.sessionState(0)
	delete(a.sessionStates, 0)
	curSession.historyLoaded = true
	curSession.err = nil
	a.sessionStates[id] = curSession

	a.sessions.add(storage.Session{Id: id, Title: title})
	a.sessions.selectedId = id
	return id, nil
}

func (a *app) renderConfirm(c *ui.Context, state *sessionState) {
	if state.confirm == nil {
		return
	}
	confirm := state.confirm
	ui.Column(c).Children(func() {
		ui.Text(c, fmt.Sprintf("请求执行命令 (目录: %s)", confirm.dir))
		ui.Text(c, confirm.command)
		ui.Row(c).Children(func() {
			if ui.Button(c, "批准").Clicked() {
				state.resolveConfirm(true)
			}
			if ui.Button(c, "拒绝").Clicked() {
				state.resolveConfirm(false)
			}
		})
	})
}

// 加载指定 session
func (a *app) selectSession(selectedId int64) error {
	if selectedId == 0 {
		a.sessions.selectedId = 0
		return nil
	}
	a.sessions.selectedId = selectedId
	state := a.sessionState(selectedId)

	if !state.historyLoaded { // 未加载指定 sessionId 的历史时
		msgs, err := agent.LoadHistory(a.ctx, a.db, selectedId)
		if err != nil {
			state.err = fmt.Errorf("加载会话历史失败: %w", err)
			return err
		}

		chatMsgs := make([]*chatMessage, 0, len(msgs)) // 填充messages
		for _, msg := range msgs {
			chatMsgs = append(chatMsgs, &chatMessage{Message: msg})
		}

		state.messages = chatMsgs
		state.historyLoaded = true
		state.err = nil
	}

	return nil
}

// 重命名指定会话
func (a *app) renameSession(sessionId int64, title string) error {
	cleaned := strings.Join(strings.Fields(title), " ")
	if cleaned == "" {
		return errors.New("会话标题不能为空")
	}

	if err := storage.UpdateSessionTitle(a.ctx, a.db, sessionId, cleaned); err != nil {
		return fmt.Errorf("重命名会话失败: %w", err)
	}

	a.sessions.rename(sessionId, cleaned)
	a.err = nil
	return nil
}

// 删除指定会话
// 若正在运行则先取消，再清理数据库与内存状态
func (a *app) deleteSession(sessionId int64) error {
	if state, ok := a.sessionStates[sessionId]; ok {
		if state.cancel != nil {
			state.cancel()
			state.cancel = nil
		}
		state.resolveConfirm(false)
	}

	if err := storage.DeleteSession(a.ctx, a.db, sessionId); err != nil {
		a.err = fmt.Errorf("删除会话失败: %w", err)
		return a.err
	}

	prevSelectedId := a.sessions.selectedId
	delete(a.sessionStates, sessionId)
	nextId, _ := a.sessions.remove(sessionId)
	a.err = nil

	if nextId != prevSelectedId {
		return a.selectSession(nextId)
	}
	return nil
}

func (a *app) submit(submitId int64, submitInputText string) {
	if submitId == 0 {
		newId, err := a.lazyCreateSession(submitInputText)
		if err != nil {
			a.sessionState(0).err = err
			return
		}
		submitId = newId
	}
	submitState := a.sessionState(submitId)
	if submitState.sessionRunning {
		return
	}

	submitLoop := submitState.loop // 取得指定 session 的 loop
	if submitLoop == nil {         // loop 为空，新建
		submitLoop = agent.NewAgentLoop(a.cfg, a.db, submitState.opts)
		if err := submitLoop.BindSession(a.ctx, submitId); err != nil { // 绑定 loop 至 session
			submitState.err = err
			return
		}
		submitState.loop = submitLoop
	} else {
		submitLoop.SetOpts(submitState.opts) // 同步最新会话选项给已有 loop
	}

	inputFunc := func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		return submitInputText, nil
	}
	outputFunc := func(ctx context.Context, chunk string, role agent.OutputRole) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		a.win.Update(func() { // 更新窗口
			submitState.appendStreamChunk(role, chunk)
		})

		return nil
	}

	confirmFunc := func(ctx context.Context, dir, command string) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}

		confirm := &confirmToolCall{
			dir:     dir,
			command: command,
			reply:   make(chan bool, 1),
		}
		a.win.Update(func() {
			submitState.confirm = confirm
		})

		select {
		case allow := <-confirm.reply:
			return allow, nil
		case <-ctx.Done():
			a.win.Update(func() {
				if submitState.confirm == confirm { // 避免误清后续请求
					submitState.confirm = nil
				}
			})
			return false, ctx.Err()
		}
	}

	turnCtx, cancel := context.WithCancel(a.ctx)
	submitState.cancel = cancel
	submitState.sessionRunning = true
	submitState.err = nil
	submitState.inputText = ""
	submitState.curReply = nil
	submitState.messages = append(submitState.messages, &chatMessage{
		Message: apis.Message{Role: "user", Content: submitInputText},
	}) // 追加窗口对话历史

	go func() { // 后台 session 运行
		defer cancel()
		err := submitLoop.RunTurn(turnCtx, inputFunc, outputFunc, confirmFunc)

		a.win.Update(func() {
			submitState.sessionRunning = false
			submitState.cancel = nil
			submitState.curReply = nil
			submitState.confirm = nil

			if err != nil && !errors.Is(err, context.Canceled) {
				submitState.err = err
			}
		})
	}()
}

func Run(ctx context.Context, db *sql.DB, cfg config.Config, opts apis.StreamOptions) error {
	sessions, err := storage.ListSessions(ctx, db)
	if err != nil {
		return err
	}

	appCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a := &app{
		ctx:         appCtx,
		cancel:      cancel,
		db:          db,
		cfg:         cfg,
		defaultOpts: opts,

		sessions: sessionList{
			list: sessions,
		},
		sessionStates: make(map[int64]*sessionState),
	}

	if len(sessions) > 0 { // 默认加载第一条
		if err := a.selectSession(a.sessions.list[0].Id); err != nil {
			return err
		}
	}

	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:   "SAISON",
			Width:   960,
			Height:  640,
			Content: ui.View(a.view),
		})
		a.win.OnClosed(func() {
			a.cancel()
		})
	})

	return mygo.App.Run()
}
