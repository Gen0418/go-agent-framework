package gui

import (
	"testing"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/storage"
)

func TestMakeSessionTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空白文本回退默认标题", " \n\t ", "新对话"},
		{"合并换行和多余空白", "  第一行\n\n  第二行\t第三列 ", "第一行 第二行 第三列"},
		{"恰好20个中文字符不截断", "一二三四五六七八九十一二三四五六七八九十", "一二三四五六七八九十一二三四五六七八九十"},
		{"超过20个中文字符按rune截断", "一二三四五六七八九十一二三四五六七八九十多余", "一二三四五六七八九十一二三四五六七八九十..."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := makeSessionTitle(tc.in); got != tc.want {
				t.Fatalf("makeSessionTitle(%q) = %q，预期 %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSessionListAddAndSelected(t *testing.T) {
	list := sessionList{
		list: []storage.Session{{ID: 1, Title: "旧会话"}},
	}
	list.add(storage.Session{ID: 2, Title: "新会话"})

	if len(list.list) != 2 || list.list[0].ID != 2 || list.list[1].ID != 1 {
		t.Fatalf("add 后列表顺序 = %+v", list.list)
	}

	list.selectedID = 2
	got, ok := list.selected()
	if !ok || got.Title != "新会话" {
		t.Fatalf("selected() = (%+v, %v)，预期选中新会话", got, ok)
	}

	list.selectedID = 99
	if _, ok := list.selected(); ok {
		t.Fatal("不存在的 selectedID 不应匹配")
	}
}

func TestSessionListRenameAndRemove(t *testing.T) {
	list := sessionList{
		list: []storage.Session{
			{ID: 3, Title: "会话3"},
			{ID: 2, Title: "会话2"},
			{ID: 1, Title: "会话1"},
		},
		selectedID: 2,
	}

	// 重命名已存在和不存在的会话
	if !list.rename(2, "已改名") || list.list[1].Title != "已改名" {
		t.Fatalf("rename 后列表 = %+v", list.list)
	}
	if list.rename(99, "无效") {
		t.Fatal("重命名不存在的会话应返回 false")
	}

	// 删除非当前选中会话，保持 selectedID 不变
	nextID, ok := list.remove(3)
	if !ok || nextID != 2 || list.selectedID != 2 || len(list.list) != 2 {
		t.Fatalf("删除非选中项后 nextID=%d, ok=%v, list=%+v", nextID, ok, list.list)
	}

	// 删除不存在的会话
	if nextID, ok := list.remove(99); ok || nextID != 2 {
		t.Fatalf("删除不存在会话返回 nextID=%d, ok=%v", nextID, ok)
	}

	// 删除当前选中项（非末尾）时落到原位置的下一个会话
	nextID, ok = list.remove(2)
	if !ok || nextID != 1 || list.selectedID != 1 {
		t.Fatalf("删除当前选中项后 nextID=%d, ok=%v, selectedID=%d", nextID, ok, list.selectedID)
	}

	// 删除最后一个会话时回退到 0（新对话草稿态）
	nextID, ok = list.remove(1)
	if !ok || nextID != 0 || list.selectedID != 0 || len(list.list) != 0 {
		t.Fatalf("清空列表后 nextID=%d, ok=%v, list=%+v", nextID, ok, list.list)
	}

	// 删除末尾选中项时回退到前一项
	tailList := sessionList{
		list: []storage.Session{
			{ID: 10, Title: "A"},
			{ID: 20, Title: "B"},
		},
		selectedID: 20,
	}
	nextID, ok = tailList.remove(20)
	if !ok || nextID != 10 || tailList.selectedID != 10 {
		t.Fatalf("删除末尾选中项后 nextID=%d, ok=%v, selectedID=%d", nextID, ok, tailList.selectedID)
	}
}

func TestSessionStateAppendStreamEvent(t *testing.T) {
	s := &sessionState{}
	call := apis.ToolCall{
		ID:       "call-1",
		Type:     "function",
		Function: apis.ToolCallFunction{Name: "bash", Args: `{"command":"pwd"}`},
	}

	s.appendStreamEvent(agent.OutputEvent{Kind: agent.OutputReasoning, Text: "思考1"})
	s.appendStreamEvent(agent.OutputEvent{Kind: agent.OutputReasoning, Text: "思考2"})
	s.appendStreamEvent(agent.OutputEvent{Kind: agent.OutputContent, Text: "正文1"})
	s.appendStreamEvent(agent.OutputEvent{Kind: agent.OutputToolCall, ToolCall: &call})
	s.appendStreamEvent(agent.OutputEvent{Kind: agent.OutputToolResult, Text: "工具结果", ToolCallID: "call-1"})
	s.appendStreamEvent(agent.OutputEvent{Kind: agent.OutputContent, Text: "总结回复"})

	if len(s.messages) != 3 {
		t.Fatalf("消息数量 = %d，预期 3", len(s.messages))
	}
	if s.messages[0].Role != "assistant" || s.messages[0].ReasoningContent != "思考1思考2" ||
		s.messages[0].Content != "正文1" || len(s.messages[0].ToolCalls) != 1 || s.messages[0].ToolCalls[0] != call {
		t.Fatalf("第 1 条消息 = %+v", s.messages[0].Message)
	}
	if s.messages[1].Role != "tool" || s.messages[1].Content != "工具结果" || s.messages[1].ToolCallID != "call-1" {
		t.Fatalf("第 2 条消息 = %+v", s.messages[1].Message)
	}
	if s.messages[2].Role != "assistant" || s.messages[2].Content != "总结回复" {
		t.Fatalf("第 3 条消息 = %+v", s.messages[2].Message)
	}
}

func TestSessionStateSyncHistoryPreservesReasoningOpen(t *testing.T) {
	s := &sessionState{
		messages: []*chatMessage{
			{Message: apis.Message{Role: "user", Content: "问题"}},
			{Message: apis.Message{Role: "assistant", ReasoningContent: "思考", Content: "半截未落库"}, reasoningOpen: true},
			{Message: apis.Message{Role: "assistant", Content: "残留脏消息"}},
		},
	}

	s.syncHistory([]apis.Message{
		{Role: "user", Content: "问题"},
		{Role: "assistant", ReasoningContent: "思考", Content: "最终落库"},
	})

	if s.curReply != nil {
		t.Fatal("syncHistory 后 curReply 应置空")
	}
	if len(s.messages) != 2 {
		t.Fatalf("同步后消息数 = %d，预期 2", len(s.messages))
	}
	if s.messages[1].Content != "最终落库" || !s.messages[1].reasoningOpen {
		t.Fatalf("同步后第 2 条消息 = %+v，预期保留 reasoningOpen=true 且内容对齐", s.messages[1])
	}
}

func TestSessionStateResolveConfirm(t *testing.T) {
	for _, allow := range []bool{true, false} {
		req := &confirmToolCall{dir: "/tmp", command: "ls", reply: make(chan bool, 1)}
		s := &sessionState{confirm: req}

		s.resolveConfirm(allow)
		if s.confirm != nil {
			t.Fatal("resolveConfirm 后 confirm 应置空")
		}
		if got := <-req.reply; got != allow {
			t.Fatalf("通道返回值 = %v，预期 %v", got, allow)
		}

		// 重复调用或 confirm 为空时不应阻塞或 panic
		s.resolveConfirm(!allow)
	}
}

func TestSessionStateOptsIsolation(t *testing.T) {
	a := &app{
		defaultOpts: apis.StreamOptions{
			Thinking:        true,
			ReasoningEffort: "low",
			Tools:           []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}},
		},
		sessionStates: make(map[int64]*sessionState),
	}

	s1 := a.sessionState(1)
	s2 := a.sessionState(2)

	s1.opts.Thinking = false
	s1.opts.ReasoningEffort = "max"
	s1.opts.Tools[0].Function.Name = "mutated"

	if !a.defaultOpts.Thinking || a.defaultOpts.ReasoningEffort != "low" || a.defaultOpts.Tools[0].Function.Name != "bash" {
		t.Fatalf("修改会话 1 选项污染了 defaultOpts: %+v", a.defaultOpts)
	}
	if !s2.opts.Thinking || s2.opts.ReasoningEffort != "low" || s2.opts.Tools[0].Function.Name != "bash" {
		t.Fatalf("修改会话 1 选项污染了会话 2: %+v", s2.opts)
	}
}

func TestSessionStateToolGroupToggles(t *testing.T) {
	s := &sessionState{}
	local := []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}}
	web := []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "web_search"}}}

	// 空工具分组开启时保持为空
	s.setToolGroupEnabled(true, nil)
	if len(s.opts.Tools) != 0 || s.hasAnyTool(local) || s.hasAnyTool(web) {
		t.Fatalf("空分组不应启用任何工具: %+v", s.opts.Tools)
	}

	// 同时开启本地命令与联网搜索，且各自互不干扰
	s.setToolGroupEnabled(true, local)
	s.setToolGroupEnabled(true, web)
	if !s.hasAnyTool(local) || !s.hasAnyTool(web) || len(s.opts.Tools) != 2 {
		t.Fatalf("同时开启两组工具后 Tools = %+v", s.opts.Tools)
	}
	// 修改会话切片不应污染默认定义
	s.opts.Tools[0].Function.Name = "changed"
	if local[0].Function.Name != "bash" {
		t.Fatalf("修改会话工具切片污染了 local 默认定义: %+v", local)
	}
	s.opts.Tools[0].Function.Name = "bash"

	// 仅关闭本地命令，保留联网搜索
	s.setToolGroupEnabled(false, local)
	if s.hasAnyTool(local) || !s.hasAnyTool(web) || len(s.opts.Tools) != 1 || s.opts.Tools[0].Function.Name != "web_search" {
		t.Fatalf("关闭本地命令后应保留联网搜索，实际 = %+v", s.opts.Tools)
	}

	// 关闭联网搜索后清空，重新开启本地命令恢复 bash
	s.setToolGroupEnabled(false, web)
	if len(s.opts.Tools) != 0 {
		t.Fatalf("两组均关闭后 Tools 应为空，实际 = %+v", s.opts.Tools)
	}
	s.setToolGroupEnabled(true, local)
	if !s.hasAnyTool(local) || s.hasAnyTool(web) || len(s.opts.Tools) != 1 || s.opts.Tools[0].Function.Name != "bash" {
		t.Fatalf("重新开启本地命令后未恢复 bash: %+v", s.opts.Tools)
	}
}

func TestSessionStatePendingImages(t *testing.T) {
	s := &sessionState{}
	s.addPendingImage(pendingImage{id: 1, name: "a.png", mime: "image/png", data: []byte{1, 2, 3}})
	s.addPendingImage(pendingImage{id: 2, name: "b.jpg", mime: "image/jpeg", data: []byte{4, 5}})

	if s.pendingImageBytes() != 5 || len(s.pendingImages) != 2 {
		t.Fatalf("pendingImages 状态异常: count=%d bytes=%d", len(s.pendingImages), s.pendingImageBytes())
	}
	if s.removePendingImage(99) {
		t.Fatal("移除不存在的图片 ID 应返回 false")
	}
	if !s.removePendingImage(1) || len(s.pendingImages) != 1 || s.pendingImages[0].id != 2 || s.pendingImageBytes() != 2 {
		t.Fatalf("移除图片 1 后状态异常: %+v", s.pendingImages)
	}
}
