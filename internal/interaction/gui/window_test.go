package gui

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

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
				{Id: 1, Title: "会话1"},
				{Id: 2, Title: "会话2"},
			},
			selectedId: 1,
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
	a.sessions.selectedId = 2
	tt.Frame()
	s2 := a.sessionState(2)
	if !s2.opts.Thinking || s2.opts.ReasoningEffort != "low" || len(s2.opts.Tools) != 1 {
		t.Fatalf("会话2 选项不应受会话1 影响: %+v", s2.opts)
	}
}

func TestAppRenameSession(t *testing.T) {
	db, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	id, err := storage.CreateSession(t.Context(), db, "原标题")
	if err != nil {
		t.Fatal(err)
	}

	a := &app{
		ctx: t.Context(),
		db:  db,
		sessions: sessionList{
			list:       []storage.Session{{Id: id, Title: "原标题"}},
			selectedId: id,
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
	gotDB, err := storage.QuerySession(t.Context(), db, id)
	if err != nil || gotDB != "新的 标题" {
		t.Fatalf("数据库标题 = %q, err = %v", gotDB, err)
	}

	// 不存在的会话返回错误
	if err := a.renameSession(id+99, "无效"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("重命名不存在会话返回 = %v，预期包装 sql.ErrNoRows", err)
	}
}

func TestAppDeleteSession(t *testing.T) {
	db, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	id1, err := storage.CreateSession(t.Context(), db, "会话1")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := storage.CreateSession(t.Context(), db, "会话2")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateMessage(t.Context(), db, storage.Message{
		SessionId: id1,
		Role:      "user",
		Content:   "会话1历史消息",
	}); err != nil {
		t.Fatal(err)
	}

	canceled := false
	confirm := &confirmToolCall{dir: "/tmp", command: "ls", reply: make(chan bool, 1)}
	a := &app{
		ctx: t.Context(),
		db:  db,
		sessions: sessionList{
			list: []storage.Session{
				{Id: id2, Title: "会话2"},
				{Id: id1, Title: "会话1"},
			},
			selectedId: id2,
		},
		sessionStates: map[int64]*sessionState{
			id2: {
				sessionRunning: true,
				cancel:         func() { canceled = true },
				confirm:        confirm,
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
	if _, err := storage.QuerySession(t.Context(), db, id2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("删除后查询会话2 = %v，预期 sql.ErrNoRows", err)
	}
	if a.sessions.selectedId != id1 {
		t.Fatalf("回退选中 = %d，预期 %d", a.sessions.selectedId, id1)
	}
	s1 := a.sessionState(id1)
	if !s1.historyLoaded || len(s1.messages) != 1 || s1.messages[0].Content != "会话1历史消息" {
		t.Fatalf("回退到会话1后未自动加载历史: loaded=%v, msgs=%+v", s1.historyLoaded, s1.messages)
	}

	// 删除最后一个会话后回退到新对话草稿态(0)
	if err := a.deleteSession(id1); err != nil {
		t.Fatal(err)
	}
	if a.sessions.selectedId != 0 || len(a.sessions.list) != 0 {
		t.Fatalf("清空全部会话后 selectedId=%d, list=%+v", a.sessions.selectedId, a.sessions.list)
	}
}

func TestSidebarSessionMenuAndModals(t *testing.T) {
	db, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	id1, err := storage.CreateSession(t.Context(), db, "会话1")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := storage.CreateSession(t.Context(), db, "会话2")
	if err != nil {
		t.Fatal(err)
	}

	a := &app{
		ctx: t.Context(),
		db:  db,
		sessions: sessionList{
			list: []storage.Session{
				{Id: id2, Title: "会话2"},
				{Id: id1, Title: "会话1"},
			},
			selectedId: id2,
		},
		sessionStates: make(map[int64]*sessionState),
	}

	tt := ui.NewTester(a.view, 960, 640)
	menuLabel := fmt.Sprintf("更多操作 %d", id2)

	// 打开侧边栏右侧菜单 -> 点击重命名 -> 弹出重命名框
	tt.Click(menuLabel)
	tt.Click("重命名")
	if !a.renameDialog.open || !tt.HasText("重命名会话") {
		t.Fatalf("重命名弹框未打开，当前文本: %q", tt.Texts())
	}

	// 空标题保存应报错且保持弹框打开
	a.renameDialog.title = "   "
	tt.Frame()
	tt.Click("保存")
	if !a.renameDialog.open || !tt.HasText("会话标题不能为空") {
		t.Fatalf("空标题保存应提示错误并保持打开，open=%v, err=%v", a.renameDialog.open, a.renameDialog.err)
	}

	// 填入新标题并保存
	a.renameDialog.title = "已改名会话"
	tt.Frame()
	tt.Click("保存")
	if a.renameDialog.open {
		t.Fatal("保存成功后重命名弹框应关闭")
	}
	if a.sessions.list[0].Title != "已改名会话" {
		t.Fatalf("重命名后列表标题 = %q，预期 已改名会话", a.sessions.list[0].Title)
	}

	// 打开删除确认弹框 -> 先点取消不应删除
	tt.Click(menuLabel)
	tt.Click("删除")
	if !a.deleteDialog.open || !tt.HasText("确定删除“已改名会话”吗？") {
		t.Fatalf("删除确认弹框未显示预期提示，当前文本: %q", tt.Texts())
	}
	tt.Click("取消")
	if a.deleteDialog.open || len(a.sessions.list) != 2 {
		t.Fatalf("取消删除后应关闭弹框且保留会话，open=%v, len=%d", a.deleteDialog.open, len(a.sessions.list))
	}

	// 再次打开删除确认弹框 -> 确认删除 -> 删库并回退到会话1
	tt.Click(menuLabel)
	tt.Click("删除")
	tt.Click("确认删除")
	if a.deleteDialog.open {
		t.Fatal("确认删除后弹框应关闭")
	}
	if len(a.sessions.list) != 1 || a.sessions.selectedId != id1 {
		t.Fatalf("删除后状态不符: selectedId=%d, list=%+v", a.sessions.selectedId, a.sessions.list)
	}
	if _, err := storage.QuerySession(t.Context(), db, id2); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("删除后数据库仍能查到会话 %d: %v", id2, err)
	}
}

func TestSubmitRejectsEmptyAPIKey(t *testing.T) {
	db, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	a := &app{
		ctx:           t.Context(),
		db:            db,
		cfg:           config.Config{APIKey: "   "},
		sessionStates: make(map[int64]*sessionState),
	}
	draft := a.sessionState(0)
	draft.inputText = "你好"

	a.submit(0, draft.inputText)

	if draft.err == nil || !strings.Contains(draft.err.Error(), "APIKey") {
		t.Fatalf("空 APIKey 提交应报错提示配置 APIKey，实际 err = %v", draft.err)
	}
	if draft.sessionRunning || len(draft.messages) != 0 || draft.inputText != "你好" {
		t.Fatalf("拦截后不应修改草稿状态: running=%v, msgs=%d, input=%q", draft.sessionRunning, len(draft.messages), draft.inputText)
	}
	sessions, err := storage.ListSessions(t.Context(), db)
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
	if !a.settingsDialog.open || a.settingsDialog.showKey || !tt.HasText("应用设置") {
		t.Fatalf("设置弹框初始状态不符: %+v", a.settingsDialog)
	}

	// 非法 Endpoint 应报错且不改内存配置、不关闭弹框
	a.settingsDialog.cfg.Endpoint = "not-a-url"
	tt.Frame()
	tt.Click("保存")
	if !a.settingsDialog.open || a.settingsDialog.err == nil {
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
	if a.settingsDialog.open || a.settingsDialog.err != nil {
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
