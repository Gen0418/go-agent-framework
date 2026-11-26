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

func TestConfirmOperateByUser(t *testing.T) {
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
			terminal := NewCli(strings.NewReader(tc.input), &out)
			ok, err := terminal.ConfirmOperateByUser(t.Context(), dir, command)
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

func TestPrintHistoryToolOutput(t *testing.T) {
	const args = `{"command":"pwd"}`
	for _, result := range []string{"/workspace", "/workspace\n"} {
		var out bytes.Buffer
		terminal := NewCli(strings.NewReader(""), &out)
		terminal.PrintHistory(t.Context(), []apis.Message{
			{Role: "assistant", ToolCalls: []apis.ToolCall{{Id: "a", Function: apis.ToolCallFunction{Name: "bash", Args: args}}}},
			{Role: "tool", ToolCallId: "a", Content: result},
		})

		want := "\n[工具调用] bash " + args + "\n\x1b[36m[工具输出] \x1b[0m/workspace\n"
		if out.String() != want {
			t.Errorf("结果 %q 的历史输出 = %q，预期 %q", result, out.String(), want)
		}
	}
}

func TestSelectSession(t *testing.T) {
	for _, tc := range []struct {
		input string
		id    int64
	}{
		{" new ", 0}, {" 8 ", 8}, {"0", -1}, {"-2", -1}, {"abc", -1},
	} {
		t.Run(tc.input, func(t *testing.T) {
			terminal := NewCli(strings.NewReader(tc.input+"\n"), io.Discard)
			id, err := terminal.SelectSession(t.Context())
			if id != tc.id || (tc.id >= 0 && err != nil) {
				t.Fatalf("输入 %q 得到 ID %d，错误 %v，预期 ID %d", tc.input, id, err, tc.id)
			}
		})
	}
	terminal := NewCli(strings.NewReader(""), io.Discard)
	if _, err := terminal.SelectSession(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("预期输入结束，收到 %v", err)
	}
}

func TestWriteOutputReasoningToContentNewline(t *testing.T) {
	var out bytes.Buffer
	terminal := NewCli(strings.NewReader(""), &out)

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
