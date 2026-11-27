package gui

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

func TestSessionOptsPopover(t *testing.T) {
	localTools := []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}}
	webTools := []apis.Tool{
		{Type: "function", Function: apis.FunctionDef{Name: "web_search"}},
		{Type: "function", Function: apis.FunctionDef{Name: "web_fetch"}},
	}

	a := &app{
		cfg:        config.Config{DefaultModel: "deepseek-chat"},
		localTools: localTools,
		webTools:   webTools,
		defaultOpts: apis.StreamOptions{
			Thinking:        true,
			ReasoningEffort: "low",
			Tools:           append(append([]apis.Tool(nil), localTools...), webTools...),
		},
		sessions: sessionList{
			list: []storage.Session{
				{ID: 1, Title: "会话1"},
				{ID: 2, Title: "会话2"},
			},
			selectedID: 1,
		},
		sessionStates: make(map[int64]*sessionState),
	}

	tt := ui.NewTester(a.view, 960, 640)
	if !tt.HasText("思考强度 low") || tt.HasText("联网搜索") {
		t.Fatalf("输入区底部摘要应仅展示思考强度，不展示联网搜索，当前文本: %q", tt.Texts())
	}

	// 点击“选项”展开浮层
	mustClick(t, tt, "选项")
	if !tt.HasText("模型: deepseek-chat") {
		t.Fatalf("展开选项浮层后未显示当前模型，当前文本: %q", tt.Texts())
	}

	// 切换思考强度为 high
	mustClick(t, tt, "high")
	s1 := a.sessionState(1)
	if s1.opts.ReasoningEffort != "high" {
		t.Fatalf("切换强度后 ReasoningEffort = %q，预期 high", s1.opts.ReasoningEffort)
	}

	// 关闭思考后隐藏强度选项，但保留原强度值
	mustClick(t, tt, "思考")
	if s1.opts.Thinking || s1.opts.ReasoningEffort != "high" {
		t.Fatalf("关闭思考后 opts = %+v，预期 Thinking=false 且保留 high", s1.opts)
	}
	if tt.HasText("high") {
		t.Fatal("关闭思考后不应再显示思考强度选项")
	}

	// 独立关闭本地命令：保留整组联网工具（web_search + web_fetch）
	mustClick(t, tt, "允许本地命令")
	if len(s1.opts.Tools) != 2 || s1.opts.Tools[0].Function.Name != "web_search" || s1.opts.Tools[1].Function.Name != "web_fetch" {
		t.Fatalf("关闭本地命令后应保留联网工具组，实际 = %+v", s1.opts.Tools)
	}

	// 独立关闭联网搜索：Tools 清空
	mustClick(t, tt, "允许联网搜索")
	if len(s1.opts.Tools) != 0 {
		t.Fatalf("同时关闭两组工具后 Tools 应为空，实际 = %+v", s1.opts.Tools)
	}

	// 单独重新开启本地命令：仅恢复 bash
	mustClick(t, tt, "允许本地命令")
	if len(s1.opts.Tools) != 1 || s1.opts.Tools[0].Function.Name != "bash" {
		t.Fatalf("重新开启本地命令后 Tools = %+v，预期仅恢复 bash", s1.opts.Tools)
	}

	// 文档解析默认关闭图表识别（纯文本模式），开启后仅影响当前会话
	if s1.docRichLayout {
		t.Fatal("新会话默认应关闭识别文档图表")
	}
	mustClick(t, tt, "识别文档图表")
	if !s1.docRichLayout {
		t.Fatal("点击勾选识别文档图表后应开启图表识别模式")
	}

	// 切换到会话2，选项不受会话1影响
	a.sessions.selectedID = 2
	tt.Frame()
	s2 := a.sessionState(2)
	if !s2.opts.Thinking || s2.opts.ReasoningEffort != "low" || len(s2.opts.Tools) != 3 || s2.docRichLayout {
		t.Fatalf("会话2 选项不应受会话1 影响: opts=%+v docRichLayout=%v", s2.opts, s2.docRichLayout)
	}
}

func TestInitDefaultToolsAndLiveClient(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	a := &app{
		cfgPath: cfgPath,
		cfg: config.Config{
			APIKey:       "sk-initial",
			Endpoint:     "https://api.deepseek.com/chat/completions",
			DefaultModel: "deepseek-flash",
		},
		sessionStates: make(map[int64]*sessionState),
	}
	a.initDefaultTools()

	if a.registry == nil || len(a.localTools) == 0 ||
		len(a.webTools) != 2 || a.webTools[0].Function.Name != "web_search" || a.webTools[1].Function.Name != "web_fetch" {
		t.Fatalf("工具分组初始化异常: local=%+v web=%+v", a.localTools, a.webTools)
	}
	if cli := a.currentClient(); cli == nil || cli.APIKey != "sk-initial" || cli.Endpoint != "https://api.deepseek.com/chat/completions" {
		t.Fatalf("初始 API 客户端异常: %+v", cli)
	}

	// 运行期在设置中更新 APIKey 与 Endpoint 后，客户端快照应即时刷新
	err := a.saveConfig(config.Config{
		APIKey:       "sk-updated",
		Endpoint:     "https://proxy.example.com/ds/v1/chat/completions",
		DefaultModel: "deepseek-pro",
	})
	if err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	if cli := a.currentClient(); cli == nil || cli.APIKey != "sk-updated" ||
		cli.Endpoint != "https://proxy.example.com/ds/v1/chat/completions" || cli.Model != "deepseek-pro" {
		t.Fatalf("保存设置后 API 客户端未刷新: %+v", cli)
	}
}

func TestAppRenameSession(t *testing.T) {
	store, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	id, err := store.CreateSession(t.Context(), "原标题")
	if err != nil {
		t.Fatal(err)
	}

	a := &app{
		ctx:   t.Context(),
		store: store,
		sessions: sessionList{
			list:       []storage.Session{{ID: id, Title: "原标题"}},
			selectedID: id,
		},
		sessionStates: make(map[int64]*sessionState),
	}

	// 空白标题应被拒绝且不改数据库
	if err := a.renameSession(id, " \n\t "); err == nil {
		t.Fatal("空白标题重命名应返回错误")
	}

	// 正常改名会合并多余空白并同步库和内存
	if err := a.renameSession(id, "  新的\n  标题 "); err != nil {
		t.Fatal(err)
	}
	if a.sessions.list[0].Title != "新的 标题" {
		t.Fatalf("内存标题 = %q，预期 新的 标题", a.sessions.list[0].Title)
	}
	gotDB, err := store.QuerySession(t.Context(), id)
	if err != nil || gotDB != "新的 标题" {
		t.Fatalf("数据库标题 = %q, err = %v", gotDB, err)
	}

	// 不存在的会话返回错误
	if err := a.renameSession(id+99, "无效"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("重命名不存在会话返回 = %v，预期包装 sql.ErrNoRows", err)
	}
}

func TestAppDeleteSession(t *testing.T) {
	store, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	id1, err := store.CreateSession(t.Context(), "会话1")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := store.CreateSession(t.Context(), "会话2")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessage(t.Context(), id1, apis.Message{
		Role:    "user",
		Content: "会话1历史消息",
	}); err != nil {
		t.Fatal(err)
	}

	canceled := false
	confirm := &confirmToolCall{dir: "/tmp", command: "ls", reply: make(chan bool, 1)}
	a := &app{
		ctx:   t.Context(),
		store: store,
		sessions: sessionList{
			list: []storage.Session{
				{ID: id2, Title: "会话2"},
				{ID: id1, Title: "会话1"},
			},
			selectedID: id2,
		},
		sessionStates: map[int64]*sessionState{
			id2: {
				running: true,
				cancel:  func() { canceled = true },
				confirm: confirm,
			},
		},
	}

	// 删除运行中且当前选中的会话2：先取消并释放确认，再回退并加载会话1历史
	if err := a.deleteSession(id2); err != nil {
		t.Fatal(err)
	}
	if !canceled {
		t.Fatal("删除运行中会话前应先调用 cancel")
	}
	if allow := <-confirm.reply; allow {
		t.Fatal("删除待确认会话应以 false 释放确认通道")
	}
	if _, exists := a.sessionStates[id2]; exists {
		t.Fatal("删除后应清理 sessionStates[id2]")
	}
	if _, err := store.QuerySession(t.Context(), id2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("删除后查询会话2 = %v，预期 sql.ErrNoRows", err)
	}
	if a.sessions.selectedID != id1 {
		t.Fatalf("回退选中 = %d，预期 %d", a.sessions.selectedID, id1)
	}
	s1 := a.sessionState(id1)
	if !s1.historyLoaded || len(s1.messages) != 1 || s1.messages[0].Content != "会话1历史消息" {
		t.Fatalf("回退到会话1后未自动加载历史: loaded=%v, msgs=%+v", s1.historyLoaded, s1.messages)
	}

	// 删除最后一个会话后回退到新对话草稿态(0)
	if err := a.deleteSession(id1); err != nil {
		t.Fatal(err)
	}
	if a.sessions.selectedID != 0 || len(a.sessions.list) != 0 {
		t.Fatalf("清空全部会话后 selectedID=%d, list=%+v", a.sessions.selectedID, a.sessions.list)
	}
}

func TestSidebarSessionMenuAndModals(t *testing.T) {
	store, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	id1, err := store.CreateSession(t.Context(), "会话1")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := store.CreateSession(t.Context(), "会话2")
	if err != nil {
		t.Fatal(err)
	}

	a := &app{
		ctx:   t.Context(),
		store: store,
		sessions: sessionList{
			list: []storage.Session{
				{ID: id2, Title: "会话2"},
				{ID: id1, Title: "会话1"},
			},
			selectedID: id2,
		},
		sessionStates: make(map[int64]*sessionState),
	}

	tt := ui.NewTester(a.view, 960, 640)
	menuLabel := fmt.Sprintf("更多操作 %d", id2)

	// 打开侧边栏右侧菜单 -> 点击重命名 -> 弹出重命名框
	tt.Click(menuLabel)
	tt.Click("重命名")
	if !a.renameDialog.visible || !tt.HasText("重命名会话") {
		t.Fatalf("重命名弹框未打开，当前文本: %q", tt.Texts())
	}

	// 空标题保存应报错且保持弹框打开
	a.renameDialog.title = "   "
	tt.Frame()
	tt.Click("保存")
	if !a.renameDialog.visible || !tt.HasText("会话标题不能为空") {
		t.Fatalf("空标题保存应提示错误并保持打开，visible=%v, err=%v", a.renameDialog.visible, a.renameDialog.err)
	}

	// 填入新标题并保存
	a.renameDialog.title = "已改名会话"
	tt.Frame()
	tt.Click("保存")
	if a.renameDialog.visible {
		t.Fatal("保存成功后重命名弹框应关闭")
	}
	if a.sessions.list[0].Title != "已改名会话" {
		t.Fatalf("重命名后列表标题 = %q，预期 已改名会话", a.sessions.list[0].Title)
	}

	// 打开删除确认弹框 -> 先点取消不应删除
	tt.Click(menuLabel)
	tt.Click("删除")
	if !a.deleteDialog.visible || !tt.HasText("确定删除“已改名会话”吗？") {
		t.Fatalf("删除确认弹框未显示预期提示，当前文本: %q", tt.Texts())
	}
	tt.Click("取消")
	if a.deleteDialog.visible || len(a.sessions.list) != 2 {
		t.Fatalf("取消删除后应关闭弹框且保留会话，visible=%v, len=%d", a.deleteDialog.visible, len(a.sessions.list))
	}

	// 再次打开删除确认弹框 -> 确认删除 -> 删库并回退到会话1
	tt.Click(menuLabel)
	tt.Click("删除")
	tt.Click("确认删除")
	if a.deleteDialog.visible {
		t.Fatal("确认删除后弹框应关闭")
	}
	if len(a.sessions.list) != 1 || a.sessions.selectedID != id1 {
		t.Fatalf("删除后状态不符: selectedID=%d, list=%+v", a.sessions.selectedID, a.sessions.list)
	}
	if _, err := store.QuerySession(t.Context(), id2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("删除后数据库仍能查到会话 %d: %v", id2, err)
	}
}

func TestSubmitRejectsEmptyAPIKey(t *testing.T) {
	store, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	a := &app{
		ctx:           t.Context(),
		store:         store,
		cfg:           config.Config{APIKey: "   "},
		sessionStates: make(map[int64]*sessionState),
	}
	draft := a.sessionState(0)
	draft.inputText = "你好"

	a.submit(0, draft.inputText)

	if draft.err == nil || !strings.Contains(draft.err.Error(), "APIKey") {
		t.Fatalf("空 APIKey 提交应报错提示配置 APIKey，实际 err = %v", draft.err)
	}
	if draft.running || len(draft.messages) != 0 || draft.inputText != "你好" {
		t.Fatalf("拦截后不应修改草稿状态: running=%v, msgs=%d, input=%q", draft.running, len(draft.messages), draft.inputText)
	}
	sessions, err := store.ListSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("空 APIKey 拦截后不应建库会话，实际 = %+v", sessions)
	}
}

func TestSettingsModalAndSaveConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_ENDPOINT", "")

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	a := &app{
		cfg: config.Config{
			APIKey:       "sk-old",
			Endpoint:     "https://old.example.com/v1",
			DefaultModel: "old-model",
		},
		cfgPath:       cfgPath,
		sessionStates: make(map[int64]*sessionState),
	}

	tt := ui.NewTester(a.view, 960, 640)

	// 点击侧边栏底部“设置”打开弹框，默认隐藏明文 Key
	tt.Click("设置")
	if !a.settingsDialog.visible || a.settingsDialog.showKey || !tt.HasText("应用设置") {
		t.Fatalf("设置弹框初始状态不符: %+v", a.settingsDialog)
	}

	// 非法 Endpoint 应报错且不改内存配置、不关闭弹框
	a.settingsDialog.cfg.Endpoint = "not-a-url"
	tt.Frame()
	tt.Click("保存")
	if !a.settingsDialog.visible || a.settingsDialog.err == nil {
		t.Fatalf("非法 Endpoint 应报错并保持打开: %+v", a.settingsDialog)
	}
	if a.cfg.Endpoint != "https://old.example.com/v1" {
		t.Fatalf("保存失败后不应修改内存配置: %+v", a.cfg)
	}

	// 填入有效配置并保存：落盘、更新内存、弹框关闭
	a.settingsDialog.cfg = config.Config{
		APIKey:       "sk-new",
		Endpoint:     "https://new.example.com/v1",
		DefaultModel: "deepseek-reasoner",
	}
	tt.Frame()
	tt.Click("保存")
	if a.settingsDialog.visible || a.settingsDialog.err != nil {
		t.Fatalf("保存成功后应关闭弹框并清错: %+v", a.settingsDialog)
	}
	if a.cfg.APIKey != "sk-new" || a.cfg.Endpoint != "https://new.example.com/v1" || a.cfg.DefaultModel != "deepseek-reasoner" {
		t.Fatalf("保存后内存配置不符: %+v", a.cfg)
	}

	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("重新加载配置文件失败: %v", err)
	}
	if loaded != a.cfg {
		t.Fatalf("落盘配置 = %+v，预期 %+v", loaded, a.cfg)
	}

	// 展开选项浮层时应立刻显示更新后的默认模型
	tt.Click("选项")
	if !tt.HasText("模型: deepseek-reasoner") {
		t.Fatalf("选项浮层未同步显示新模型，当前文本: %q", tt.Texts())
	}
}

func TestSidebarCollapseAndChatHeader(t *testing.T) {
	a := &app{
		cfg: config.Config{DefaultModel: "deepseek-chat"},
		sessions: sessionList{
			list: []storage.Session{
				{ID: 1, Title: "会话1"},
				{ID: 2, Title: "会话2"},
			},
			selectedID: 1,
		},
		sessionStates: map[int64]*sessionState{
			1: {err: errors.New("网络超时")},
			2: {
				running: true,
				confirm: &confirmToolCall{dir: "/tmp", command: "ls", reply: make(chan bool, 1)},
			},
		},
	}

	tt := ui.NewTester(a.view, 1040, 700)

	// 默认展开侧边栏，显示品牌副标题、待确认状态标识、顶栏模型名、空态提示与错误横幅
	if !tt.HasText("Ready to use, free to brew.") {
		t.Fatalf("侧边栏未显示品牌副标题，当前文本: %q", tt.Texts())
	}
	if !tt.HasText("待确认") {
		t.Fatalf("侧边栏未显示待确认标识，当前文本: %q", tt.Texts())
	}
	if !tt.HasText("deepseek-chat") || !tt.HasText("输入消息开始新对话，或从左侧切换已有会话") {
		t.Fatalf("聊天区顶栏或空态文案缺失，当前文本: %q", tt.Texts())
	}
	if !tt.HasText("网络超时") {
		t.Fatalf("未展示错误横幅，当前文本: %q", tt.Texts())
	}

	// 关闭错误横幅应清空当前会话错误
	tt.Click("关闭错误提示")
	if a.sessionState(1).err != nil || tt.HasText("网络超时") {
		t.Fatalf("关闭错误提示后错误未清空: err=%v", a.sessionState(1).err)
	}

	// 收起侧边栏后隐藏侧边栏，并在聊天顶栏出现展开按钮
	tt.Click("收起侧边栏")
	if !a.sidebarCollapsed || tt.HasText("新建会话") {
		t.Fatalf("收起侧边栏后状态不符: collapsed=%v, texts=%q", a.sidebarCollapsed, tt.Texts())
	}

	// 点击展开侧边栏恢复显示
	tt.Click("展开侧边栏")
	if a.sidebarCollapsed || !tt.HasText("待确认") {
		t.Fatalf("展开侧边栏后未恢复显示: collapsed=%v, texts=%q", a.sidebarCollapsed, tt.Texts())
	}
}

func TestChatMessageRenderingAndReasoningToggle(t *testing.T) {
	a := &app{
		cfg: config.Config{DefaultModel: "deepseek-chat"},
		sessions: sessionList{
			list:       []storage.Session{{ID: 1, Title: "消息渲染测试"}},
			selectedID: 1,
		},
		sessionStates: map[int64]*sessionState{
			1: {
				historyLoaded: true,
				messages: []*chatMessage{
					{Message: apis.Message{Role: "user", Content: "请列出当前目录"}},
					{Message: apis.Message{
						Role:             "assistant",
						ReasoningContent: "先调用 bash 查看目录",
						Content:          "这是 **Markdown** 回复",
						ToolCalls: []apis.ToolCall{{
							ID:       "c1",
							Type:     "function",
							Function: apis.ToolCallFunction{Name: "bash", Args: `{"command":"ls"}`},
						}},
					}},
					{Message: apis.Message{Role: "tool", ToolCallID: "c1", Content: "README.md\ngo.mod\n"}},
				},
			},
		},
	}

	tt := ui.NewTester(a.view, 1040, 700)

	// 不再出现 CLI 风格前缀
	for _, legacy := range []string{"[用户]", "[回复]", "[思考内容]", "[工具调用]", "[工具结果]"} {
		if tt.HasText(legacy) {
			t.Fatalf("界面仍残留旧 CLI 前缀 %q，当前文本: %q", legacy, tt.Texts())
		}
	}

	// 用户消息、Markdown 正文、工具调用与输出均应渲染
	for _, want := range []string{"请列出当前目录", "Markdown", "工具调用", "bash", "工具输出", "README.md"} {
		if !tt.HasText(want) {
			t.Fatalf("消息列表缺少预期内容 %q，当前文本: %q", want, tt.Texts())
		}
	}

	// 思考内容默认折叠，点击“思考过程”展开后再点击可收起
	if tt.HasText("先调用 bash 查看目录") {
		t.Fatal("思考内容默认应折叠隐藏")
	}
	tt.Click("思考过程")
	if !tt.HasText("先调用 bash 查看目录") {
		t.Fatalf("点击展开思考过程后未显示思考内容，当前文本: %q", tt.Texts())
	}
	tt.Click("思考过程")
	if a.sessionState(1).messages[1].reasoningOpen {
		t.Fatal("再次点击思考过程应关闭展开态")
	}
}

func TestComposerKeyboardAndConfirmCard(t *testing.T) {
	var submitted []string
	stopped := false
	decided := []bool{}

	state := &sessionState{
		opts: apis.StreamOptions{
			Thinking:        true,
			ReasoningEffort: "low",
			Tools:           []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}},
		},
	}
	var pop optsPopover
	confirm := &confirmToolCall{dir: "/workspace", command: "ls -la"}

	view := func(c *ui.Context) {
		applyTheme(c)
		ui.Column(c).Fill().Padding(16).Gap(12).Children(func() {
			if confirm != nil {
				renderConfirmCard(c, confirm, func(allow bool) {
					decided = append(decided, allow)
					confirm = nil
				})
			}
			renderComposer(c, composerProps{
				sessionID:   1,
				model:       "deepseek-chat",
				state:       state,
				optsPopover: &pop,
				toolToggles: []optionToggle{{label: "允许本地命令", checked: true}},
				onSubmit: func(text string) {
					submitted = append(submitted, text)
					state.inputText = ""
				},
				onStop: func() {
					stopped = true
				},
			})
		})
	}

	tt := ui.NewTester(view, 800, 500)

	// 确认卡片展示目录和命令，点击批准触发回调
	if !tt.HasText("等待确认") || !tt.HasText("ls -la") {
		t.Fatalf("确认卡片未渲染预期内容: %q", tt.Texts())
	}
	tt.Click("批准")
	if len(decided) != 1 || !decided[0] {
		t.Fatalf("点击批准后结果 = %v，预期 [true]", decided)
	}

	// 输入区底部仅展示思考强度摘要
	if !tt.HasText("思考强度 low") {
		t.Fatalf("未展示思考强度摘要，当前文本: %q", tt.Texts())
	}

	// 聚焦输入框：Shift+Enter 换行不发送，Enter 直接发送并清空草稿
	tt.Click("消息输入框")
	tt.Type("第一行")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("第二行")
	if len(submitted) != 0 || state.inputText != "第一行\n第二行" {
		t.Fatalf("Shift+Enter 不应提交: submitted=%q, draft=%q", submitted, state.inputText)
	}

	tt.Key(0, ui.KeyEnter)
	if len(submitted) != 1 || submitted[0] != "第一行\n第二行" || state.inputText != "" {
		t.Fatalf("Enter 应提交并清空草稿: submitted=%q, draft=%q", submitted, state.inputText)
	}

	// 运行态切换为“停止”按钮并响应点击
	state.running = true
	tt.Frame()
	tt.Click("停止")
	if !stopped {
		t.Fatal("运行态点击停止应触发 onStop")
	}
}

func mustClick(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("点击 %q 失败（元素可能被遮挡或挤出视野）: %v", label, err)
	}
}

func TestLongToolCallAndConfirmStayScrollableAndVisible(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("echo line-%02d", i+1)
	}
	longCmd := strings.Join(lines, "\n")
	longArgs := fmt.Sprintf(`{"command":%q}`, longCmd)

	confirm := &confirmToolCall{dir: "/workspace", command: longCmd, reply: make(chan bool, 1)}
	a := &app{
		cfg: config.Config{DefaultModel: "deepseek-chat"},
		sessions: sessionList{
			list:       []storage.Session{{ID: 1, Title: "长命令测试"}},
			selectedID: 1,
		},
		sessionStates: map[int64]*sessionState{
			1: {
				historyLoaded: true,
				running:       true,
				confirm:       confirm,
				messages: []*chatMessage{
					{Message: apis.Message{
						Role: "assistant",
						ToolCalls: []apis.ToolCall{{
							ID:       "long-call",
							Type:     "function",
							Function: apis.ToolCallFunction{Name: "powershell", Args: longArgs},
						}},
					}},
				},
			},
		},
	}

	// 按最小窗口尺寸（680×480）渲染，验证长命令不会把“批准/拒绝”挤出视野
	tt := ui.NewTester(a.view, 680, 480)

	// 消息列表中的长工具参数默认折叠（不包含完整 JSON 参数文本），确认卡展示行数摘要
	if tt.HasText(`{"command":`) {
		t.Fatal("长工具调用参数默认应折叠隐藏")
	}
	if !tt.HasText("共 60 行") {
		t.Fatalf("长命令确认卡应显示行数摘要，当前文本: %q", tt.Texts())
	}

	// 展开消息中的长工具参数
	mustClick(t, tt, "切换工具参数展开")
	if !tt.HasText(`{"command":`) || !tt.HasText("收起") {
		t.Fatalf("展开长工具参数后应显示参数与收起按钮，当前文本: %q", tt.Texts())
	}

	// 切换确认卡命令区的展开/收起，且“批准”按钮在最小窗口下依然可点
	mustClick(t, tt, "切换待确认命令展开")
	if !confirm.expanded {
		t.Fatal("点击展开后 confirm.expanded 应为 true")
	}
	mustClick(t, tt, "批准")
	if allow := <-confirm.reply; !allow {
		t.Fatal("长命令展开状态下点击批准应正常返回 true")
	}
}

func TestOverflowProtectionForModalsErrorAndToolResult(t *testing.T) {
	t.Run("超长错误横幅、展开长确认卡与超长标题在最小窗口下不挤出关键按钮，且长工具输出可折叠", func(t *testing.T) {
		errLines := make([]string, 30)
		for i := range errLines {
			errLines[i] = fmt.Sprintf("error-detail-line-%02d", i+1)
		}
		cmdLines := make([]string, 40)
		for i := range cmdLines {
			cmdLines[i] = fmt.Sprintf("echo confirm-line-%02d", i+1)
		}
		outLines := make([]string, 30)
		for i := range outLines {
			outLines[i] = fmt.Sprintf("out-line-%02d", i+1)
		}

		confirm := &confirmToolCall{
			dir:     strings.Repeat("C:/very_long_segment/", 8),
			command: strings.Join(cmdLines, "\n"),
			reply:   make(chan bool, 1),
		}
		a := &app{
			cfg: config.Config{DefaultModel: "deepseek-chat"},
			sessions: sessionList{
				list:       []storage.Session{{ID: 1, Title: strings.Repeat("SuperLongSessionTitleWithoutSpaces", 3)}},
				selectedID: 1,
			},
			sessionStates: map[int64]*sessionState{
				1: {
					historyLoaded: true,
					running:       true,
					err:           errors.New(strings.Join(errLines, "\n")),
					confirm:       confirm,
					messages: []*chatMessage{
						{Message: apis.Message{Role: "tool", ToolCallID: "t1", Content: strings.Join(outLines, "\n")}},
					},
				},
			},
		}

		tt := ui.NewTester(a.view, 680, 480)

		// 即使 30 行错误横幅 + 40 行展开确认卡 + 超长路径同时出现，关键操作按钮依然都在视野内可点
		mustClick(t, tt, "切换待确认命令展开")
		if !confirm.expanded {
			t.Fatal("点击展开后 confirm.expanded 应为 true")
		}
		mustClick(t, tt, "批准")
		if allow := <-confirm.reply; !allow {
			t.Fatal("错误横幅叠加展开长确认卡时批准按钮应保持可点")
		}
		mustClick(t, tt, "关闭错误提示")
		if a.sessionState(1).err != nil {
			t.Fatal("超长错误横幅应能正常关闭")
		}

		// 关闭横幅与确认卡后，验证长工具输出默认展开在滚动区，且支持手动收起/再展开
		if !tt.HasText("共 30 行") || !tt.HasText("out-line-01") {
			t.Fatalf("长工具输出默认应展示摘要与内容: %q", tt.Texts())
		}
		mustClick(t, tt, "切换工具输出展开")
		if tt.HasText("out-line-01") {
			t.Fatal("点击收起后应隐藏长工具输出正文")
		}
		mustClick(t, tt, "切换工具输出展开")
		if !tt.HasText("out-line-01") {
			t.Fatal("再次点击展开后应恢复长工具输出正文")
		}

		// 超长无空格会话标题不应把右侧菜单按钮挤出侧边栏
		mustClick(t, tt, "更多操作 1")
		if !tt.HasText("重命名") {
			t.Fatalf("超长无空格会话标题不应把右侧菜单挤出视野: %q", tt.Texts())
		}
	})

	t.Run("矮窗口下设置弹框叠加长报错仍可点击底部保存与取消", func(t *testing.T) {
		longErr := errors.New(strings.Repeat("保存配置失败：磁盘写入权限不足或路径非法。\n", 15))
		a := &app{
			cfg:           config.Config{APIKey: "sk-1", Endpoint: "https://api.example.com", DefaultModel: "m"},
			sessionStates: make(map[int64]*sessionState),
		}
		a.settingsDialog.open(a.cfg)
		a.settingsDialog.err = longErr

		// 在 680×480 最小窗口下，设置弹框带 15 行错误仍应保证底部取消按钮可见可点
		tt := ui.NewTester(a.view, 680, 480)
		mustClick(t, tt, "取消")
		if a.settingsDialog.visible {
			t.Fatal("矮窗口+长报错场景下取消按钮应保持可见并可关闭弹框")
		}
	})
}

func TestSubmitBlocksWhenHistoryLoadFails(t *testing.T) {
	store, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	// 构造一个不存在的 sessionID，触发加载历史失败
	const missingID int64 = 999
	a := &app{
		ctx:           t.Context(),
		store:         store,
		cfg:           config.Config{APIKey: "sk-test", Endpoint: "https://example.invalid", DefaultModel: "m"},
		sessionStates: make(map[int64]*sessionState),
	}
	state := a.sessionState(missingID)
	state.inputText = "不应该发出去"

	a.submit(missingID, state.inputText)

	if state.err == nil || !strings.Contains(state.err.Error(), "加载会话历史失败") {
		t.Fatalf("历史未加载且重载失败时应报错，实际 err = %v", state.err)
	}
	if state.running || len(state.messages) != 0 || state.inputText != "不应该发出去" {
		t.Fatalf("拦截后不应变更会话状态: running=%v, msgs=%d, input=%q", state.running, len(state.messages), state.inputText)
	}
}

func TestMultiSessionConfirmSwitchAndPersistence(t *testing.T) {
	// 构造 mock 模型服务：根据请求内容区分会话 A（工具调用）和会话 B（普通对话）
	const (
		doneEvent = "data: [DONE]\n\n"
		stopEvent = "data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"
		callEvent = "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo ok\"}"}}]}}]}` + "\n\n" +
			"data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" + doneEvent
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []apis.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		last := req.Messages[len(req.Messages)-1]
		switch {
		case last.Role == "user" && last.Content == "执行命令A":
			io.WriteString(w, callEvent)
		case last.Role == "tool":
			io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"命令执行完成"}}]}`+"\n\n"+stopEvent+doneEvent)
		default:
			io.WriteString(w, "data: "+`{"choices":[{"delta":{"reasoning_content":"想一下B"}}]}`+"\n\n"+
				"data: "+`{"choices":[{"delta":{"content":"回复B"}}]}`+"\n\n"+stopEvent+doneEvent)
		}
	}))
	t.Cleanup(srv.Close)

	store, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	idA, err := store.CreateSession(t.Context(), "会话A")
	if err != nil {
		t.Fatal(err)
	}
	idB, err := store.CreateSession(t.Context(), "会话B")
	if err != nil {
		t.Fatal(err)
	}

	// 模拟单线程 UI 事件循环，保证所有 updateUI 串行执行且与测试主线程同步
	uiCh := make(chan func(), 64)
	a := &app{
		ctx:      t.Context(),
		store:    store,
		dispatch: func(fn func()) { uiCh <- fn },
		cfg: config.Config{
			APIKey:       "sk-test",
			Endpoint:     srv.URL,
			DefaultModel: "test-model",
		},
		sessions: sessionList{
			list: []storage.Session{
				{ID: idA, Title: "会话A"},
				{ID: idB, Title: "会话B"},
			},
			selectedID: idA,
		},
		sessionStates: make(map[int64]*sessionState),
	}

	waitUntil := func(cond func() bool, desc string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for !cond() {
			select {
			case fn := <-uiCh:
				fn()
			case <-deadline:
				t.Fatalf("等待超时: %s", desc)
			}
		}
	}

	// 1. 会话 A 发起工具调用并进入待确认状态
	if err := a.selectSession(idA); err != nil {
		t.Fatal(err)
	}
	stateA := a.sessionState(idA)
	a.submit(idA, "执行命令A")
	waitUntil(func() bool { return stateA.confirm != nil }, "会话 A 出现工具确认")

	// 2. 切到会话 B 正常发消息并等待完成，期间会话 A 保持待确认状态
	if err := a.selectSession(idB); err != nil {
		t.Fatal(err)
	}
	stateB := a.sessionState(idB)
	a.submit(idB, "你好B")
	waitUntil(func() bool { return !stateB.running }, "会话 B 完成生成")
	if stateB.err != nil {
		t.Fatalf("会话 B 出错: %v", stateB.err)
	}
	if stateA.confirm == nil || !stateA.running {
		t.Fatal("切到会话 B 后不应丢失会话 A 的待确认或运行状态")
	}

	// 3. 切回会话 A 批准执行并等待整轮完成
	if err := a.selectSession(idA); err != nil {
		t.Fatal(err)
	}
	stateA.resolveConfirm(true)
	waitUntil(func() bool { return !stateA.running }, "会话 A 完成工具调用与最终回复")
	if stateA.err != nil {
		t.Fatalf("会话 A 出错: %v", stateA.err)
	}

	// 4. 验证两个会话的实时界面消息与重启后从数据库重载的历史完全一致
	for _, tc := range []struct {
		name  string
		id    int64
		state *sessionState
	}{
		{"会话A", idA, stateA},
		{"会话B", idB, stateB},
	} {
		reloaded, err := store.LoadHistory(t.Context(), tc.id)
		if err != nil {
			t.Fatalf("%s 重载历史失败: %v", tc.name, err)
		}
		got := make([]apis.Message, len(tc.state.messages))
		for i, m := range tc.state.messages {
			got[i] = m.Message
		}
		if !reflect.DeepEqual(got, reloaded) {
			t.Fatalf("%s 界面消息与数据库历史不一致:\n界面=%+v\n库内=%+v", tc.name, got, reloaded)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func makeGUITestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 40, G: 120, B: 220, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeGUITestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestGUIImagePickerPasteDropAndRemove(t *testing.T) {
	dir := t.TempDir()
	pngBytes := makeGUITestPNG(t)
	jpgBytes := makeGUITestJPEG(t)

	shotPath := filepath.Join(dir, "shot.png")
	dropPath := filepath.Join(dir, "photo.jpg")
	badPath := filepath.Join(dir, "archive.bin")
	for path, data := range map[string][]byte{
		shotPath: pngBytes,
		dropPath: jpgBytes,
		badPath:  {0x00, 0x01, 0x02, 0xff, 0xfe},
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	uiCh := make(chan func(), 32)
	deletedCh := make(chan string, 8)
	var uploadSeq int
	var mu sync.Mutex
	a := &app{
		ctx:      t.Context(),
		dispatch: func(fn func()) { uiCh <- fn },
		httpClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodDelete {
					deletedCh <- filepath.Base(r.URL.Path)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"deleted":true}`)),
					}, nil
				}
				mu.Lock()
				uploadSeq++
				id := fmt.Sprintf("file-api-%d", uploadSeq)
				mu.Unlock()
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":%q,"bytes":128,"expires_at":%d}`, id, time.Now().Add(24*time.Hour).Unix()))),
				}, nil
			}),
		},
		cfg: config.Config{
			APIKey:       "sk-test",
			Endpoint:     "https://api.deepseek.com/chat/completions",
			DefaultModel: "deepseek-flash",
		},
		sessions: sessionList{
			list:       []storage.Session{{ID: 1, Title: "附件输入测试"}},
			selectedID: 1,
		},
		sessionStates: map[int64]*sessionState{
			1: {historyLoaded: true},
		},
		pickFiles: func(cb func([]string, error)) {
			cb([]string{shotPath, badPath}, nil)
		},
	}

	drainUploads := func(wantReady int) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			ready := 0
			for _, pf := range a.sessionState(1).pendingFiles {
				if pf.status == fileReady {
					ready++
				}
			}
			if ready >= wantReady {
				return
			}
			select {
			case fn := <-uiCh:
				fn()
			case <-deadline:
				t.Fatalf("等待附件预上传就绪超时，want=%d got=%d", wantReady, ready)
			}
		}
	}

	tt := ui.NewTester(a.view, 960, 640)
	state := a.sessionState(1)

	// 1. 点击“+ 附件”选择混合有效图片与无效二进制文件：合法文件进入预上传队列，无效二进制报错
	mustClick(t, tt, "添加附件")
	if len(state.pendingFiles) != 1 || state.pendingFiles[0].name != "shot.png" || !state.pendingFiles[0].isImage {
		t.Fatalf("选附件后 pendingFiles = %+v，预期仅包含 shot.png", state.pendingFiles)
	}
	if state.err == nil || !strings.Contains(state.err.Error(), "archive.bin") {
		t.Fatalf("不支持的二进制文件应报错提示 archive.bin，实际 err = %v", state.err)
	}
	drainUploads(1)
	shotFileID := state.pendingFiles[0].fileID

	// 2. 聚焦输入框并 Cmd/Ctrl+V 粘贴截图 PNG：继续走剪贴板 Base64 草稿（pendingImages），不走预上传
	mustClick(t, tt, "消息输入框")
	tt.SetClipboardData(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, pngBytes))))
	tt.Key(ui.Cmd, ui.KeyV)
	if len(state.pendingImages) != 1 || state.pendingImages[0].name != "粘贴图片.png" {
		t.Fatalf("粘贴截图后 pendingImages = %+v，预期新增粘贴图片.png", state.pendingImages)
	}

	// 3. 普通文本粘贴仍正常进入输入框，不误触发图片附件
	tt.SetClipboard("帮我看看这些附件")
	tt.Key(ui.Cmd, ui.KeyV)
	if state.inputText != "帮我看看这些附件" || len(state.pendingImages) != 1 {
		t.Fatalf("普通文本粘贴异常: input=%q, images=%d", state.inputText, len(state.pendingImages))
	}

	// 4. 拖拽 JPEG 文件到聊天区：进入 Files API 预上传草稿队列
	if !tt.DropFiles(600, 260, dropPath) {
		t.Fatal("聊天区应接受拖入文件")
	}
	if len(state.pendingFiles) != 2 || state.pendingFiles[1].name != "photo.jpg" {
		t.Fatalf("拖入文件后 pendingFiles = %+v，预期新增 photo.jpg", state.pendingFiles)
	}
	drainUploads(2)
	tt.Frame()

	// 5. 移除已预上传的 shot.png：从草稿移除并异步清理云端文件；移除粘贴图片不影响文件草稿
	mustClick(t, tt, "移除附件 shot.png")
	if len(state.pendingFiles) != 1 || state.pendingFiles[0].name != "photo.jpg" {
		t.Fatalf("移除 shot.png 后应仅剩 photo.jpg，实际 = %+v", state.pendingFiles)
	}
	select {
	case delID := <-deletedCh:
		if delID != shotFileID {
			t.Fatalf("移除附件触发删除的云端 file_id = %q，预期 %q", delID, shotFileID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("移除已就绪附件后未触发云端 DELETE 清理")
	}

	mustClick(t, tt, "移除图片 粘贴图片.png")
	if len(state.pendingImages) != 0 {
		t.Fatalf("移除粘贴图片后 pendingImages 应为空，实际 = %d", len(state.pendingImages))
	}
}

func TestGUIImageOnlySubmitAndHistoryThumbnails(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent.db")
	store, err := storage.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	var capturedBody []byte
	var openedPaths []string
	uiCh := make(chan func(), 32)
	a := &app{
		ctx:      t.Context(),
		store:    store,
		dispatch: func(fn func()) { uiCh <- fn },
		httpClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				capturedBody, _ = io.ReadAll(r.Body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body: io.NopCloser(strings.NewReader(
						"data: " + `{"choices":[{"delta":{"content":"收到纯图消息"}}]}` + "\n\n" +
							"data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
							"data: [DONE]\n\n",
					)),
				}, nil
			}),
		},
		openPath: func(path string) error {
			openedPaths = append(openedPaths, path)
			return nil
		},
		cfg: config.Config{
			APIKey:       "sk-test",
			Endpoint:     "https://api.deepseek.com/chat/completions",
			DefaultModel: "deepseek-flash",
		},
		sessionStates: make(map[int64]*sessionState),
	}

	pngBytes := makeGUITestPNG(t)
	a.attachImageBytes(0, "clip.png", pngBytes)

	tt := ui.NewTester(a.view, 960, 640)

	// 1. 不输入任何文字，直接点击“发送”提交纯图片草稿
	mustClick(t, tt, "发送")
	if a.sessions.selectedID == 0 || len(a.sessions.list) != 1 || a.sessions.list[0].Title != "图片对话" {
		t.Fatalf("纯图懒创建会话异常: selected=%d list=%+v", a.sessions.selectedID, a.sessions.list)
	}
	sid := a.sessions.selectedID
	state := a.sessionState(sid)
	if len(state.pendingImages) != 0 {
		t.Fatalf("发送后待发图片草稿应清空，实际 = %d", len(state.pendingImages))
	}

	deadline := time.After(5 * time.Second)
	for state.running {
		select {
		case fn := <-uiCh:
			fn()
		case <-deadline:
			t.Fatal("等待纯图对话生成完成超时")
		}
	}
	if state.err != nil {
		t.Fatalf("纯图发送流程出错: %v", state.err)
	}

	// 2. 验证请求体按多模态 image_url 编码，且文件确实落盘到 attachments/pic/<sid>/
	bodyStr := string(capturedBody)
	if !strings.Contains(bodyStr, `"type":"image_url"`) || !strings.Contains(bodyStr, `"data:image/png;base64,`) {
		t.Fatalf("请求体未包含多模态图片编码: %s", bodyStr)
	}
	if len(state.messages) != 2 || len(state.messages[0].Images) != 1 {
		t.Fatalf("完成后的会话消息异常: %+v", state.messages)
	}
	savedPath := state.messages[0].Images[0].Path
	wantDir := filepath.Join(dir, "attachments", "pic", fmt.Sprint(sid))
	if !strings.HasPrefix(savedPath, wantDir+string(filepath.Separator)) {
		t.Fatalf("图片未落盘到 attachments/pic/%d: %q", sid, savedPath)
	}

	// 3. 历史气泡渲染图片缩略图，点击触发预览回调
	tt.Frame()
	mustClick(t, tt, "查看图片 clip.png")
	if len(openedPaths) != 1 || openedPaths[0] != savedPath {
		t.Fatalf("点击历史缩略图未打开预期路径: got=%q want=%q", openedPaths, savedPath)
	}

	// 4. 磁盘文件被外部移除后，清缓存重绘应优雅展示“图片不可用”而不报错崩溃
	if err := os.Remove(savedPath); err != nil {
		t.Fatal(err)
	}
	a.thumbs.evict(pathThumbKey(savedPath, historyThumbMaxSide))
	tt.Frame()
	if !tt.HasText("图片不可用") {
		t.Fatalf("缺失图片文件时应展示兜底占位，当前文本: %q", tt.Texts())
	}
}

func TestGUIFilePreUploadRetryToggleSubmitAndDelete(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent.db")
	cfgPath := filepath.Join(dir, "config.json")
	store, err := storage.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	pdfPath := filepath.Join(dir, "report.pdf")
	notesPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.7 mock report content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notesPath, []byte("hello plain text attachment\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type uploadRecord struct {
		auth       string
		purpose    string
		filename   string
		anchor     string
		seconds    string
		fieldOrder []string
	}

	var mu sync.Mutex
	var uploads []uploadRecord
	var chatBody []byte
	var openedPaths []string
	deletedCh := make(chan struct{ auth, id string }, 16)
	firstUploadStarted := make(chan struct{})
	releaseFirstUpload := make(chan struct{})
	uploadCount := 0

	uiCh := make(chan func(), 64)
	a := &app{
		ctx:      t.Context(),
		store:    store,
		cfgPath:  cfgPath,
		dispatch: func(fn func()) { uiCh <- fn },
		httpClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/files"):
					_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
					if err != nil {
						return nil, err
					}
					mr := multipart.NewReader(r.Body, params["boundary"])
					rec := uploadRecord{auth: r.Header.Get("Authorization")}
					for {
						part, err := mr.NextPart()
						if err == io.EOF {
							break
						}
						if err != nil {
							return nil, err
						}
						rec.fieldOrder = append(rec.fieldOrder, part.FormName())
						val, _ := io.ReadAll(part)
						switch part.FormName() {
						case "purpose":
							rec.purpose = string(val)
						case "expires_after[anchor]":
							rec.anchor = string(val)
						case "expires_after[seconds]":
							rec.seconds = string(val)
						case "file":
							rec.filename = part.FileName()
						}
					}
					mu.Lock()
					uploadCount++
					n := uploadCount
					uploads = append(uploads, rec)
					mu.Unlock()

					if n == 1 {
						close(firstUploadStarted)
						<-releaseFirstUpload
						return &http.Response{
							StatusCode: http.StatusInternalServerError,
							Header:     http.Header{"Content-Type": []string{"application/json"}},
							Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"临时解析故障"}}`)),
						}, nil
					}
					id := fmt.Sprintf("file-api-%d", n)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
							`{"id":%q,"bytes":%d,"expires_at":%d}`,
							id, 28, time.Now().Add(30*24*time.Hour).Unix(),
						))),
					}, nil

				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/files/"):
					deletedCh <- struct{ auth, id string }{
						auth: r.Header.Get("Authorization"),
						id:   filepath.Base(r.URL.Path),
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"deleted":true}`)),
					}, nil

				default:
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					chatBody = body
					mu.Unlock()
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body: io.NopCloser(strings.NewReader(
							"data: " + `{"choices":[{"delta":{"content":"已阅读报告内容"}}]}` + "\n\n" +
								"data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
								"data: [DONE]\n\n",
						)),
					}, nil
				}
			}),
		},
		openPath: func(path string) error {
			openedPaths = append(openedPaths, path)
			return nil
		},
		cfg: config.Config{
			APIKey:       "sk-key-1",
			Endpoint:     "https://api.deepseek.com/chat/completions",
			DefaultModel: "deepseek-flash",
		},
		sessionStates: make(map[int64]*sessionState),
	}

	waitUI := func(cond func() bool, desc string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for !cond() {
			select {
			case fn := <-uiCh:
				fn()
			case <-deadline:
				t.Fatalf("等待超时: %s", desc)
			}
		}
	}

	tt := ui.NewTester(a.view, 1040, 700)

	// 1. 拖入 report.pdf：后台 goroutine 以默认 text_recognition 开始预上传，期间禁止发送
	if !tt.DropFiles(600, 300, pdfPath) {
		t.Fatal("聊天区应接受拖入 PDF")
	}
	<-firstUploadStarted
	tt.Frame()
	draft := a.sessionState(0)
	if len(draft.pendingFiles) != 1 || draft.pendingFiles[0].status != fileUploading {
		t.Fatalf("预上传中状态异常: %+v", draft.pendingFiles)
	}
	if !tt.HasText("上传解析中…") {
		t.Fatalf("上传中应显示进度文案，当前文本: %q", tt.Texts())
	}
	// 上传未完成时点击“发送”或直接调用 submit 均被阻止
	_ = tt.Click("发送")
	a.submit(0, "")
	if a.sessions.selectedID != 0 {
		t.Fatal("附件上传中不应允许提交")
	}

	// 2. 放行首次请求返回 500 失败 -> 展示“重试”，勾选“识别文档图表”开启后点击重试 -> 使用 rich_layout_recognition 成功
	close(releaseFirstUpload)
	waitUI(func() bool { return draft.pendingFiles[0].status == fileFailed }, "首次上传失败")
	tt.Frame()
	if !tt.HasText("重试") {
		t.Fatalf("上传失败后应展示重试按钮，当前文本: %q", tt.Texts())
	}

	mustClick(t, tt, "选项")
	mustClick(t, tt, "识别文档图表")
	mustClick(t, tt, "选项")
	mustClick(t, tt, "重试上传 report.pdf")
	waitUI(func() bool { return draft.pendingFiles[0].status == fileReady }, "重试后上传就绪")
	tt.Frame()
	if !tt.HasText("含图表识别") {
		t.Fatalf("开启识别文档图表并重试后应显示含图表识别，当前文本: %q", tt.Texts())
	}

	mu.Lock()
	if len(uploads) != 2 {
		mu.Unlock()
		t.Fatalf("预期 2 次上传请求，实际 = %d", len(uploads))
	}
	if uploads[0].purpose != apis.FilePurposeTextRecognition ||
		uploads[1].purpose != apis.FilePurposeRichLayoutRecognition ||
		uploads[1].anchor != "created_at" || uploads[1].seconds != "2592000" {
		mu.Unlock()
		t.Fatalf("上传参数不符合预期: %+v", uploads)
	}
	mu.Unlock()

	// 3. 仅携带就绪文件点击“发送”（不输入文本）：懒建会话、备份至 attachments/file/<sid>/、请求携带 file_id
	mustClick(t, tt, "发送")
	sid := a.sessions.selectedID
	if sid == 0 || len(a.sessions.list) != 1 || a.sessions.list[0].Title != "report.pdf" {
		t.Fatalf("纯文件懒创建会话标题异常: sid=%d list=%+v", sid, a.sessions.list)
	}
	state := a.sessionState(sid)
	waitUI(func() bool { return !state.running }, "纯文件对话完成")
	if state.err != nil {
		t.Fatalf("纯文件提交出错: %v", state.err)
	}

	mu.Lock()
	chatBodyStr := string(chatBody)
	mu.Unlock()
	if !strings.Contains(chatBodyStr, `"type":"file"`) || !strings.Contains(chatBodyStr, `"file_id":"file-api-2"`) {
		t.Fatalf("聊天请求体未包含 file_id 块: %s", chatBodyStr)
	}
	if len(state.messages) != 2 || len(state.messages[0].Files) != 1 {
		t.Fatalf("完成后历史消息附件异常: %+v", state.messages)
	}
	savedFile := state.messages[0].Files[0]
	wantDir := filepath.Join(dir, "attachments", "file", fmt.Sprint(sid))
	if !strings.HasPrefix(savedFile.Path, wantDir+string(filepath.Separator)) {
		t.Fatalf("附件原件未备份到 attachments/file/%d: %q", sid, savedFile.Path)
	}

	// 4. 点击历史消息中的文件卡片，调用系统打开本地备份路径
	tt.Frame()
	mustClick(t, tt, "打开文件 report.pdf")
	if len(openedPaths) != 1 || openedPaths[0] != savedFile.Path {
		t.Fatalf("点击历史文件卡片打开路径不符: got=%q want=%q", openedPaths, savedFile.Path)
	}

	// 5. 再拖入草稿 notes.txt 后在设置中更换 APIKey：旧云端 file_id 被删除，并用新 Key 自动重传
	if !tt.DropFiles(600, 300, notesPath) {
		t.Fatal("聊天区应接受拖入 notes.txt")
	}
	waitUI(func() bool {
		return len(state.pendingFiles) == 1 && state.pendingFiles[0].status == fileReady
	}, "notes.txt 首次预上传就绪")
	oldDraftID := state.pendingFiles[0].fileID

	if err := a.saveConfig(config.Config{
		APIKey:       "sk-key-2",
		Endpoint:     "https://api.deepseek.com/chat/completions",
		DefaultModel: "deepseek-flash",
	}); err != nil {
		t.Fatalf("切换 APIKey 失败: %v", err)
	}
	waitUI(func() bool {
		return len(state.pendingFiles) == 1 &&
			state.pendingFiles[0].status == fileReady &&
			state.pendingFiles[0].fileID != oldDraftID
	}, "切换 APIKey 后 notes.txt 自动重传就绪")
	newDraftID := state.pendingFiles[0].fileID

	select {
	case del := <-deletedCh:
		if del.id != oldDraftID || del.auth != "Bearer sk-key-1" {
			t.Fatalf("切换 APIKey 删除旧云端文件参数异常: %+v，预期 id=%q oldKey", del, oldDraftID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("切换 APIKey 后未删除旧 Key 下的云端草稿文件")
	}

	// 6. 删除会话：同时异步清理历史 file-api-2 与未发草稿 newDraftID，且清理本地 attachments/file/<sid>/
	if err := a.deleteSession(sid); err != nil {
		t.Fatalf("删除会话失败: %v", err)
	}
	deletedSet := make(map[string]bool)
	deadline := time.After(3 * time.Second)
	for len(deletedSet) < 2 {
		select {
		case del := <-deletedCh:
			deletedSet[del.id] = true
		case <-deadline:
			t.Fatalf("删除会话后未收齐云端文件清理请求，已收到: %+v", deletedSet)
		}
	}
	if !deletedSet["file-api-2"] || !deletedSet[newDraftID] {
		t.Fatalf("删除会话清理的云端 file_id 不完整: %+v，预期含 file-api-2 与 %s", deletedSet, newDraftID)
	}
	if _, err := os.Stat(wantDir); !os.IsNotExist(err) {
		t.Fatalf("删除会话后本地附件目录应已移除，stat err = %v", err)
	}
}
