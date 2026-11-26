package gui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// GUI应用主体
type app struct {
	// 应用级依赖
	ctx         context.Context
	cancel      context.CancelFunc
	store       *storage.Store
	win         *mygo.Window
	dispatch    func(fn func())    // 可选 UI 线程调度器，为空时默认走 a.win.Update
	cfg         config.Config      // 应用级配置项(APIKey/Endpoint/DefaultModel)
	cfgPath     string             // 本地配置文件路径
	defaultOpts apis.StreamOptions // 默认会话设置(Thinking/ReasoningEffort/Tools)

	// 会话级信息
	sessions         sessionList
	sessionStates    map[int64]*sessionState // sessionID -> sessionState
	chatLists        map[int64]*ui.ListState // sessionID -> 消息列表滚动状态（与纯状态解耦）
	md               markdown.Options        // Markdown 渲染选项（所有会话共享，默认按词淡入）
	sidebarCollapsed bool                    // 侧边栏是否收起（零值默认展开）
	optsPopover      optsPopover             // 会话选项浮层组件
	renameDialog     renameDialog            // 重命名弹框状态
	deleteDialog     deleteDialog            // 删除确认弹框状态
	settingsDialog   settingsDialog          // 应用设置弹框状态

	// 应用级错误
	err error
}

func (a *app) chatList(id int64) *ui.ListState {
	if a.chatLists == nil {
		a.chatLists = make(map[int64]*ui.ListState)
	}
	if l, ok := a.chatLists[id]; ok {
		return l
	}
	l := &ui.ListState{}
	a.chatLists[id] = l
	return l
}

func (a *app) sessionState(id int64) *sessionState {
	if s, ok := a.sessionStates[id]; ok {
		return s
	}
	opts := a.defaultOpts
	opts.Tools = slices.Clone(opts.Tools) // 独立拷贝 Tools，避免跨会话共享切片
	s := &sessionState{opts: opts}
	a.sessionStates[id] = s
	return s
}

func (a *app) selectedSessionState() (storage.Session, *sessionState, bool) {
	if a.sessions.selectedID == 0 {
		return storage.Session{ID: 0, Title: "新对话"}, a.sessionState(0), true
	}
	s, ok := a.sessions.selected()
	if !ok {
		return storage.Session{}, nil, false
	}
	return s, a.sessionState(s.ID), true
}

// 应用根视图
// 包含可折叠侧边栏、聊天主区以及重命名/删除/设置三个全局弹框
func (a *app) view(c *ui.Context) {
	applyTheme(c)

	ui.Row(c).Fill().Children(func() {
		if !a.sidebarCollapsed {
			items := make([]sidebarItem, 0, len(a.sessions.list))
			for _, s := range a.sessions.list {
				item := sidebarItem{session: s}
				if st, ok := a.sessionStates[s.ID]; ok {
					item.running = st.running
					item.pendingConfirm = st.confirm != nil
				}
				items = append(items, item)
			}
			renderSidebar(c, sidebarProps{
				items:      items,
				selectedID: a.sessions.selectedID,
				onNew: func() {
					a.sessions.selectedID = 0
					a.err = nil
				},
				onCollapse: func() {
					a.sidebarCollapsed = true
				},
				onSelect: func(id int64) {
					_ = a.selectSession(id)
				},
				onRename: a.renameDialog.open,
				onDelete: a.deleteDialog.open,
				onSettings: func() {
					a.settingsDialog.open(a.cfg)
				},
			})
		}
		a.renderActiveChat(c)
	})
	a.renameDialog.view(c, a.renameSession)
	a.deleteDialog.view(c, a.deleteSession)
	a.settingsDialog.view(c, a.saveConfig)
}

func (a *app) renderActiveChat(c *ui.Context) {
	curSession, curSessionState, ok := a.selectedSessionState()
	if !ok {
		ui.Column(c).Grow(1).FillHeight().Children(func() {
			ui.Text(c, "暂无会话").TextColor(c.Theme().TextMuted).Margin(ui.Auto)
		})
		return
	}

	if a.md.Theme == nil {
		a.md.Theme = &markdown.Theme{
			FontSize:   14,
			LinkWeight: 500,
		}
	}

	renderChat(c, chatProps{
		sessionID:        curSession.ID,
		title:            curSession.Title,
		model:            a.cfg.DefaultModel,
		running:          curSessionState.running,
		sidebarCollapsed: a.sidebarCollapsed,
		onExpandSidebar: func() {
			a.sidebarCollapsed = false
		},
		err: curSessionState.err,
		onDismissErr: func() {
			curSessionState.err = nil
		},
		messages:  curSessionState.messages,
		curReply:  curSessionState.curReply,
		listState: a.chatList(curSession.ID),
		md:        &a.md,
		confirm:   curSessionState.confirm,
		onConfirm: curSessionState.resolveConfirm,
		composer: composerProps{
			sessionID:   curSession.ID,
			inputText:   &curSessionState.inputText,
			model:       a.cfg.DefaultModel,
			opts:        &curSessionState.opts,
			hasTools:    len(a.defaultOpts.Tools) > 0,
			optsPopover: &a.optsPopover,
			running:     curSessionState.running,
			onToggleTools: func(enabled bool) {
				curSessionState.setToolsEnabled(enabled, a.defaultOpts.Tools)
			},
			onSubmit: func(text string) {
				a.submit(curSession.ID, text)
			},
			onStop: func() {
				if curSessionState.cancel != nil {
					curSessionState.cancel()
					curSessionState.cancel = nil
				}
			},
		},
	})
}

// 首次发送时将新对话草稿落库为正式会话
// 同步迁移 sessionStates[0] 到真实 sessionID
func (a *app) lazyCreateSession(input string) (int64, error) {
	title := makeSessionTitle(input)
	id, err := a.store.CreateSession(a.ctx, title)
	if err != nil {
		return 0, fmt.Errorf("新建会话失败: %w", err)
	}

	curSession := a.sessionState(0)
	delete(a.sessionStates, 0)
	curSession.historyLoaded = true
	curSession.err = nil
	a.sessionStates[id] = curSession
	if list, ok := a.chatLists[0]; ok {
		delete(a.chatLists, 0)
		a.chatLists[id] = list
	}

	a.sessions.add(storage.Session{ID: id, Title: title})
	a.sessions.selectedID = id
	return id, nil
}

// 加载指定 session
// 仅在首次切换且尚未加载历史时读库
func (a *app) selectSession(selectedID int64) error {
	if selectedID == 0 {
		a.sessions.selectedID = 0
		return nil
	}
	a.sessions.selectedID = selectedID
	state := a.sessionState(selectedID)

	if !state.historyLoaded { // 未加载指定 sessionID 的历史时
		msgs, err := a.store.LoadHistory(a.ctx, selectedID)
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
func (a *app) renameSession(sessionID int64, title string) error {
	cleaned := strings.Join(strings.Fields(title), " ")
	if cleaned == "" {
		return errors.New("会话标题不能为空")
	}

	if err := a.store.UpdateSessionTitle(a.ctx, sessionID, cleaned); err != nil {
		return fmt.Errorf("重命名会话失败: %w", err)
	}

	a.sessions.rename(sessionID, cleaned)
	a.err = nil
	return nil
}

// 删除指定会话
// 若正在运行则先取消，再清理数据库与内存状态
func (a *app) deleteSession(sessionID int64) error {
	if state, ok := a.sessionStates[sessionID]; ok {
		if state.cancel != nil {
			state.cancel()
			state.cancel = nil
		}
		state.resolveConfirm(false)
	}

	if err := a.store.DeleteSession(a.ctx, sessionID); err != nil {
		a.err = fmt.Errorf("删除会话失败: %w", err)
		return a.err
	}

	prevSelectedID := a.sessions.selectedID
	delete(a.sessionStates, sessionID)
	delete(a.chatLists, sessionID)
	nextID, _ := a.sessions.remove(sessionID)
	a.err = nil

	if nextID != prevSelectedID {
		return a.selectSession(nextID)
	}
	return nil
}

// 校验并保存应用级配置
func (a *app) saveConfig(cfg config.Config) error {
	normalized := cfg.WithDefaults()
	parsed, err := url.Parse(normalized.Endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("Endpoint 必须是有效的 http 或 https 地址")
	}

	if err := config.Save(a.cfgPath, normalized); err != nil {
		return err
	}
	a.cfg = normalized
	return nil
}

func (a *app) updateUI(fn func()) {
	if a.dispatch != nil {
		a.dispatch(fn)
		return
	}
	if a.win != nil {
		a.win.Update(fn)
	}
}

// 提交本轮用户输入并在后台异步推进 Loop
// 所有跨 goroutine 的界面状态更新均通过 a.updateUI 回到主线程执行
func (a *app) submit(submitID int64, submitInputText string) {
	if strings.TrimSpace(a.cfg.APIKey) == "" {
		a.sessionState(submitID).err = errors.New("请先在设置中配置 APIKey")
		return
	}

	if submitID == 0 {
		newID, err := a.lazyCreateSession(submitInputText)
		if err != nil {
			a.sessionState(0).err = err
			return
		}
		submitID = newID
	}
	submitState := a.sessionState(submitID)
	if submitState.running {
		return
	}
	if !submitState.historyLoaded {
		if err := a.selectSession(submitID); err != nil {
			return
		}
	}

	submitLoop := submitState.loop // 取得指定 session 的 loop
	if submitLoop == nil {         // loop 为空，新建
		submitLoop = agent.NewLoop(a.store, nil)
		if err := submitLoop.BindSession(a.ctx, submitID); err != nil { // 绑定 loop 至 session
			submitState.err = err
			return
		}
		submitState.loop = submitLoop
	}

	turnClient := &apis.Client{
		Endpoint: a.cfg.Endpoint,
		APIKey:   a.cfg.APIKey,
		Model:    a.cfg.DefaultModel,
	}
	turnOpts := submitState.opts
	turnOpts.Tools = slices.Clone(turnOpts.Tools)

	outputFunc := func(ctx context.Context, event agent.OutputEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		a.updateUI(func() { // 更新窗口
			submitState.appendStreamEvent(event)
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
		a.updateUI(func() {
			submitState.confirm = confirm
		})

		select {
		case allow := <-confirm.reply:
			return allow, nil
		case <-ctx.Done():
			a.updateUI(func() {
				if submitState.confirm == confirm { // 避免误清后续请求
					submitState.confirm = nil
				}
			})
			return false, ctx.Err()
		}
	}

	turnCtx, cancel := context.WithCancel(a.ctx)
	submitState.cancel = cancel
	submitState.running = true
	submitState.err = nil
	submitState.inputText = ""
	submitState.curReply = nil
	submitState.messages = append(submitState.messages, &chatMessage{
		Message: apis.Message{Role: "user", Content: submitInputText},
	}) // 追加窗口对话历史

	go func() { // 后台 session 运行
		defer cancel()
		err := submitLoop.RunTurn(turnCtx, agent.TurnRequest{
			Input:   submitInputText,
			Client:  turnClient,
			Opts:    turnOpts,
			Output:  outputFunc,
			Confirm: confirmFunc,
		})
		history := slices.Clone(submitLoop.History())

		a.updateUI(func() {
			submitState.syncHistory(history)
			submitState.running = false
			submitState.cancel = nil
			submitState.confirm = nil

			if err != nil && !errors.Is(err, context.Canceled) {
				submitState.err = err
			}
		})
	}()
}

func Run(ctx context.Context, store *storage.Store, cfg config.Config, cfgPath string, opts apis.StreamOptions) error {
	sessions, err := store.ListSessions(ctx)
	if err != nil {
		return err
	}

	appCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a := &app{
		ctx:         appCtx,
		cancel:      cancel,
		store:       store,
		cfg:         cfg,
		cfgPath:     cfgPath,
		defaultOpts: opts,

		sessions: sessionList{
			list: sessions,
		},
		sessionStates: make(map[int64]*sessionState),
	}

	if len(sessions) > 0 { // 默认加载第一条
		if err := a.selectSession(a.sessions.list[0].ID); err != nil {
			return err
		}
	}

	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "SAISON",
			Width:     1040,
			Height:    700,
			MinWidth:  680,
			MinHeight: 480,
			Content:   ui.View(a.view),
		})
		a.win.OnClosed(func() {
			a.cancel()
		})
	})

	return mygo.App.Run()
}
