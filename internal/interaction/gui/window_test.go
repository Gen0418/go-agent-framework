package gui

import (
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
