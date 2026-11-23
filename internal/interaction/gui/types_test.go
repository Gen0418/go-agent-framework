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
		list: []storage.Session{{Id: 1, Title: "旧会话"}},
	}
	list.add(storage.Session{Id: 2, Title: "新会话"})

	if len(list.list) != 2 || list.list[0].Id != 2 || list.list[1].Id != 1 {
		t.Fatalf("add 后列表顺序 = %+v", list.list)
	}

	list.selectedId = 2
	got, ok := list.selected()
	if !ok || got.Title != "新会话" {
		t.Fatalf("selected() = (%+v, %v)，预期选中新会话", got, ok)
	}

	list.selectedId = 99
	if _, ok := list.selected(); ok {
		t.Fatal("不存在的 selectedId 不应匹配")
	}
}

func TestSessionStateAppendStreamChunk(t *testing.T) {
	s := &sessionState{}

	s.appendStreamChunk(agent.IsReasoningContent, "思考1")
	s.appendStreamChunk(agent.IsReasoningContent, "思考2")
	s.appendStreamChunk(agent.IsContent, "正文1")
	s.appendStreamChunk(agent.IsToolContent, "工具结果")
	s.appendStreamChunk(agent.IsContent, "总结回复")

	if len(s.messages) != 3 {
		t.Fatalf("消息数量 = %d，预期 3", len(s.messages))
	}
	if s.messages[0].Role != "assistant" || s.messages[0].ReasoningContent != "思考1思考2" || s.messages[0].Content != "正文1" {
		t.Fatalf("第 1 条消息 = %+v", s.messages[0].Message)
	}
	if s.messages[1].Role != "tool" || s.messages[1].Content != "工具结果" {
		t.Fatalf("第 2 条消息 = %+v", s.messages[1].Message)
	}
	if s.messages[2].Role != "assistant" || s.messages[2].Content != "总结回复" {
		t.Fatalf("第 3 条消息 = %+v", s.messages[2].Message)
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

func TestSessionStateSetToolsEnabled(t *testing.T) {
	s := &sessionState{}
	defaults := []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}}

	// 无默认工具时开启仍保持为空
	s.setToolsEnabled(true, nil)
	if len(s.opts.Tools) != 0 {
		t.Fatal("默认工具为空时不应启用工具")
	}

	// 开启工具从 defaults 拷贝，关闭后置空，再次开启能恢复
	s.setToolsEnabled(true, defaults)
	if len(s.opts.Tools) != 1 || s.opts.Tools[0].Function.Name != "bash" {
		t.Fatalf("开启工具后 Tools = %+v", s.opts.Tools)
	}
	s.opts.Tools[0].Function.Name = "changed"
	if defaults[0].Function.Name != "bash" {
		t.Fatalf("修改会话工具切片污染了 defaults: %+v", defaults)
	}

	s.setToolsEnabled(false, defaults)
	if len(s.opts.Tools) != 0 {
		t.Fatalf("关闭工具后 Tools 应为空，实际 = %+v", s.opts.Tools)
	}
	s.setToolsEnabled(true, defaults)
	if len(s.opts.Tools) != 1 || s.opts.Tools[0].Function.Name != "bash" {
		t.Fatalf("重新开启工具后未恢复默认定义: %+v", s.opts.Tools)
	}
}
