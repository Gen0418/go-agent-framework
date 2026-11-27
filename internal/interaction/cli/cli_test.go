package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
)

func TestConfirm(t *testing.T) {
	for _, tc := range []struct {
		name, input     string
		approved, retry bool
		err             error
	}{
		{"同意", "y\n", true, false, nil},
		{"重试后同意", "不确定\n YES \n", true, true, nil},
		{"拒绝", "N\n", false, false, nil},
		{"完整拒绝", "no\n", false, false, nil},
		{"输入结束", "", false, false, io.EOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			const dir, command = "工作 目录", "cat 'test file'"
			terminal := New(strings.NewReader(tc.input), &out)
			ok, err := terminal.Confirm(t.Context(), dir, command)
			if ok != tc.approved || !errors.Is(err, tc.err) {
				t.Fatalf("确认 = %v，错误 = %v", ok, err)
			}

			prompt := out.String()
			if !strings.Contains(prompt, dir) || !strings.Contains(prompt, command) || strings.Contains(prompt, "请输入") != tc.retry {
				t.Fatalf("确认提示 = %q", prompt)
			}
		})
	}
}

type errWriter struct {
	err error
}

func (w errWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestPrintHistoryToolOutput(t *testing.T) {
	const args = `{"command":"pwd"}`
	for _, result := range []string{"/workspace", "/workspace\n"} {
		var out bytes.Buffer
		terminal := New(strings.NewReader(""), &out)
		if err := terminal.PrintHistory(t.Context(), []apis.Message{
			{Role: "assistant", ToolCalls: []apis.ToolCall{{ID: "a", Function: apis.ToolCallFunction{Name: "bash", Args: args}}}},
			{Role: "tool", ToolCallID: "a", Content: result},
		}); err != nil {
			t.Fatalf("PrintHistory 意外失败: %v", err)
		}

		want := "\n[工具调用] bash " + args + "\n\x1b[36m[工具输出] \x1b[0m/workspace\n"
		if out.String() != want {
			t.Errorf("结果 %q 的历史输出 = %q，预期 %q", result, out.String(), want)
		}
	}

	wantErr := errors.New("输出管道已关闭")
	failing := New(strings.NewReader(""), errWriter{err: wantErr})
	if err := failing.PrintHistory(t.Context(), []apis.Message{
		{Role: "assistant", ReasoningContent: "先想"},
	}); !errors.Is(err, wantErr) {
		t.Fatalf("写入失败应返回错误 %v，实际 %v", wantErr, err)
	}
	if failing.inReasoning {
		t.Fatal("PrintHistory 出错返回后应重置 inReasoning 状态")
	}
}

func TestSelectSession(t *testing.T) {
	for _, tc := range []struct {
		input string
		id    int64
		isNew bool
		err   error
	}{
		{" new ", 0, true, nil},
		{" 8 ", 8, false, nil},
		{"0", 0, false, ErrInvalidSession},
		{"-2", 0, false, ErrInvalidSession},
		{"abc", 0, false, ErrInvalidSession},
	} {
		t.Run(tc.input, func(t *testing.T) {
			var out bytes.Buffer
			terminal := New(strings.NewReader(tc.input+"\n"), &out)
			id, isNew, err := terminal.SelectSession(t.Context())
			if id != tc.id || isNew != tc.isNew || !errors.Is(err, tc.err) {
				t.Fatalf("输入 %q 得到 (id=%d, isNew=%v, err=%v)，预期 (id=%d, isNew=%v, err=%v)",
					tc.input, id, isNew, err, tc.id, tc.isNew, tc.err)
			}
			if !strings.Contains(out.String(), "sessionID") {
				t.Fatalf("提示文案应写入输出流 c.out，实际 = %q", out.String())
			}
		})
	}

	var out bytes.Buffer
	terminal := New(strings.NewReader(""), &out)
	if _, _, err := terminal.SelectSession(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("预期输入结束，收到 %v", err)
	}

	wantErr := errors.New("提示写入失败")
	failing := New(strings.NewReader("new\n"), errWriter{err: wantErr})
	if _, _, err := failing.SelectSession(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("输出失败应返回 %v，实际 %v", wantErr, err)
	}
}

func TestWriteOutputReasoningToContentNewline(t *testing.T) {
	var out bytes.Buffer
	terminal := New(strings.NewReader(""), &out)

	events := []agent.OutputEvent{
		{Kind: agent.OutputReasoning, Text: "先想"},
		{Kind: agent.OutputReasoning, Text: "再想"},
		{Kind: agent.OutputContent, Text: "正"},
		{Kind: agent.OutputContent, Text: "文"},
	}
	for _, ev := range events {
		if err := terminal.WriteOutput(t.Context(), ev); err != nil {
			t.Fatal(err)
		}
	}

	want := ansiReasoning + "先想" + ansiReset + ansiReasoning + "再想" + ansiReset + "\n正文"
	if out.String() != want {
		t.Fatalf("输出 = %q，预期 %q", out.String(), want)
	}
}

func TestPrintHistoryImages(t *testing.T) {
	var out bytes.Buffer
	terminal := New(strings.NewReader(""), &out)
	err := terminal.PrintHistory(t.Context(), []apis.Message{
		{
			Role:    "user",
			Content: "看看报错",
			Images: []apis.Image{
				{Path: "attachments/pic/1/a.png", MIME: "image/png", Name: "error.png"},
				{Path: "attachments/pic/1/b.png", MIME: "image/png"},
			},
		},
	})
	if err != nil {
		t.Fatalf("PrintHistory 失败: %v", err)
	}
	want := "\n[图片: error.png]\n[图片]\n看看报错"
	if out.String() != want {
		t.Fatalf("带图历史输出 = %q，预期 %q", out.String(), want)
	}
}
