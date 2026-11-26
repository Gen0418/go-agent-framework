package gui

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo/ui"
)

func TestSessionOptsPopover(t *testing.T) {
	a := &app{
		cfg: config.Config{DefaultModel: "deepseek-chat"},
		defaultOpts: apis.StreamOptions{
			Thinking:        true,
			ReasoningEffort: "low",
			Tools:           []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}},
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

	// 点击“选项”展开浮层
	tt.Click("选项")
	if !tt.HasText("模型: deepseek-chat") {
		t.Fatalf("展开选项浮层后未显示当前模型，当前文本: %q", tt.Texts())
	}

	// 切换思考强度为 high
	tt.Click("high")
	s1 := a.sessionState(1)
	if s1.opts.ReasoningEffort != "high" {
		t.Fatalf("切换强度后 ReasoningEffort = %q，预期 high", s1.opts.ReasoningEffort)
	}

	// 关闭思考后隐藏强度选项，但保留原强度值
	tt.Click("思考")
	if s1.opts.Thinking || s1.opts.ReasoningEffort != "high" {
		t.Fatalf("关闭思考后 opts = %+v，预期 Thinking=false 且保留 high", s1.opts)
	}
	if tt.HasText("high") {
		t.Fatal("关闭思考后不应再显示思考强度选项")
	}

	// 关闭并重新开启工具调用
	tt.Click("允许工具调用")
	if len(s1.opts.Tools) != 0 {
		t.Fatalf("关闭工具后 Tools 应为空，实际 = %+v", s1.opts.Tools)
	}
	tt.Click("允许工具调用")
	if len(s1.opts.Tools) != 1 || s1.opts.Tools[0].Function.Name != "bash" {
		t.Fatalf("重新开启工具后 Tools = %+v，预期恢复 bash", s1.opts.Tools)
	}

	// 切换到会话2，选项不受会话1影响
	a.sessions.selectedID = 2
	tt.Frame()
	s2 := a.sessionState(2)
	if !s2.opts.Thinking || s2.opts.ReasoningEffort != "low" || len(s2.opts.Tools) != 1 {
		t.Fatalf("会话2 选项不应受会话1 影响: %+v", s2.opts)
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

	draft := ""
	opts := apis.StreamOptions{
		Thinking:        true,
		ReasoningEffort: "low",
		Tools:           []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}},
	}
	var pop optsPopover
	running := false
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
				inputText:   &draft,
				model:       "deepseek-chat",
				opts:        &opts,
				hasTools:    true,
				optsPopover: &pop,
				running:     running,
				onSubmit: func(text string) {
					submitted = append(submitted, text)
					draft = ""
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
	if len(submitted) != 0 || draft != "第一行\n第二行" {
		t.Fatalf("Shift+Enter 不应提交: submitted=%q, draft=%q", submitted, draft)
	}

	tt.Key(0, ui.KeyEnter)
	if len(submitted) != 1 || submitted[0] != "第一行\n第二行" || draft != "" {
		t.Fatalf("Enter 应提交并清空草稿: submitted=%q, draft=%q", submitted, draft)
	}

	// 运行态切换为“停止”按钮并响应点击
	running = true
	tt.Frame()
	tt.Click("停止")
	if !stopped {
		t.Fatal("运行态点击停止应触发 onStop")
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
	tt.Click("切换工具参数展开")
	if !tt.HasText(`{"command":`) || !tt.HasText("收起") {
		t.Fatalf("展开长工具参数后应显示参数与收起按钮，当前文本: %q", tt.Texts())
	}

	// 切换确认卡命令区的展开/收起，且“批准”按钮在最小窗口下依然可点
	tt.Click("切换待确认命令展开")
	if !confirm.expanded {
		t.Fatal("点击展开后 confirm.expanded 应为 true")
	}
	tt.Click("批准")
	if allow := <-confirm.reply; !allow {
		t.Fatal("长命令展开状态下点击批准应正常返回 true")
	}
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
