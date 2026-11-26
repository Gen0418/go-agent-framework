package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/storage"
)

const (
	doneEvent       = "data: [DONE]\n\n"
	stopEvent       = "data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	callFinishEvent = "data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n"
	replyStream     = "data: " + `{"choices":[{"delta":{"reasoning_content":"思考"}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"content":"答复"}}]}` + "\n\n" + stopEvent + doneEvent
	callStream = "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"bash","arguments":"{\"command\":\"cat fixture.txt\"}"}},{"index":1,"id":"b","type":"function","function":{"name":"bash","arguments":"{\"command\":\"> denied.txt\"}"}}]}}]}`
)

type exchange struct {
	messages []apis.Message
	stream   string
}

func newTestLoop(t *testing.T, exchanges ...exchange) *AgentLoop {
	t.Helper()
	pending := make(chan exchange, len(exchanges))
	for _, step := range exchanges {
		pending <- step
	}
	close(pending)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		step, ok := <-pending
		if !ok {
			t.Error("收到额外的模型请求")
			http.Error(w, "额外请求", http.StatusBadRequest)
			return
		}
		var request struct {
			Messages []apis.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "请求格式错误", http.StatusBadRequest)
			return
		}
		if !reflect.DeepEqual(request.Messages, step.messages) {
			t.Errorf("请求历史 = %+v，预期 %+v", request.Messages, step.messages)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, step.stream)
	}))
	t.Cleanup(func() {
		server.Close()
		if len(pending) != 0 {
			t.Errorf("还有 %d 次预期请求未发出", len(pending))
		}
	})

	db, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	loop := NewAgentLoop(config.Config{Endpoint: server.URL}, db, apis.StreamOptions{})
	if err := loop.NewSession(t.Context(), "测试会话"); err != nil {
		t.Fatal(err)
	}
	return loop
}

func assertHistory(t *testing.T, loop *AgentLoop, want []apis.Message) {
	t.Helper()
	if !reflect.DeepEqual(loop.msgs, want) {
		t.Errorf("内存历史 = %+v，预期 %+v", loop.msgs, want)
	}
	rows, err := storage.QueryMessages(t.Context(), loop.db, loop.SessionId)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		t.Fatalf("数据库中有 %d 条消息，预期 %d 条", len(rows), len(want))
	}
	for i, msg := range want {
		calls := msg.ToolCalls
		if calls == nil {
			calls = []apis.ToolCall{}
		}
		var storedCalls []apis.ToolCall
		row := rows[i]
		if err := json.Unmarshal([]byte(row.ToolCalls), &storedCalls); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(storedCalls, calls) || row.ToolCallId != msg.ToolCallId ||
			row.Role != msg.Role || row.Content != msg.Content || row.ReasoningContent != msg.ReasoningContent {
			t.Errorf("数据库第 %d 条消息 = %+v，预期 %+v", i+1, row, msg)
		}
	}
}

func inputOnce(t *testing.T, text string) InputFunc {
	used := false
	return func(context.Context) (string, error) {
		if used {
			t.Fatal("一轮中重复读取用户输入")
		}
		used = true
		return text, nil
	}
}

func TestRunTurnToolResultsAndNextTurn(t *testing.T) {
	user := apis.Message{Role: "user", Content: "调用工具"}
	call := apis.ToolCall{Id: "a", Type: "function", Function: apis.ToolCallFunction{Name: "bash", Args: `{"command":"cat fixture.txt"}`}}
	secondCall := call
	secondCall.Id = "b"
	secondCall.Function.Args = `{"command":"> denied.txt"}`
	request := apis.Message{Role: "assistant", ToolCalls: []apis.ToolCall{call, secondCall}}
	const result = "文件内容"
	t.Chdir(t.TempDir())
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("fixture.txt", []byte(result), 0600); err != nil {
		t.Fatal(err)
	}
	var confirmed []string
	confirm := func(_ context.Context, gotDir, command string) (bool, error) {
		if gotDir != dir {
			t.Fatalf("确认目录 = %q，预期 %q", gotDir, dir)
		}
		confirmed = append(confirmed, command)
		return command == "cat fixture.txt", nil
	}
	history := []apis.Message{
		user, request,
		{Role: "tool", ToolCallId: "a", Content: result},
		{Role: "tool", ToolCallId: "b", Content: "用户拒绝执行"},
		{Role: "assistant", ReasoningContent: "思考", Content: "答复"},
		{Role: "user", Content: "继续"},
		{Role: "assistant", ReasoningContent: "思考", Content: "答复"},
	}
	loop := newTestLoop(t,
		exchange{history[:1], callStream + "\n\n" + callFinishEvent + doneEvent},
		exchange{history[:4], replyStream},
		exchange{history[:6], replyStream},
	)
	var results []string
	output := func(_ context.Context, text string, role OutputRole) error {
		if role == IsToolContent {
			results = append(results, text)
		}
		return nil
	}
	if err := loop.RunTurn(t.Context(), inputOnce(t, user.Content), output, confirm); err != nil {
		t.Fatal(err)
	}
	assertHistory(t, loop, history[:5])
	if !reflect.DeepEqual(results, []string{result, "用户拒绝执行"}) {
		t.Fatalf("实时工具输出 = %q", results)
	}
	if !reflect.DeepEqual(confirmed, []string{"cat fixture.txt", "> denied.txt"}) {
		t.Fatalf("确认的命令 = %v", confirmed)
	}
	if _, err := os.Stat("denied.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒绝后检查文件，收到 %v", err)
	}

	if err := loop.RunTurn(t.Context(), inputOnce(t, "继续"), discardOutput, unexpectedConfirm(t)); err != nil {
		t.Fatal(err)
	}
	assertHistory(t, loop, history)
}

func TestRunTurnReplyBoundaries(t *testing.T) {
	user := apis.Message{Role: "user", Content: "测试"}
	tests := []struct {
		name, stream string
		wantError    bool
		wantEOF      bool
	}{
		{"空回复", stopEvent + doneEvent, false, false},
		{"调用列表为空", callFinishEvent + doneEvent, true, false},
		{"工具调用结束后断流", callStream + "\n\n" + callFinishEvent, true, true},
		{"结束原因之后断流", strings.TrimSuffix(replyStream, doneEvent), true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := []apis.Message{user}
			loop := newTestLoop(t, exchange{want, tc.stream})
			err := loop.RunTurn(t.Context(), inputOnce(t, user.Content), discardOutput, unexpectedConfirm(t))
			if (err != nil) != tc.wantError || errors.Is(err, io.ErrUnexpectedEOF) != tc.wantEOF {
				t.Fatalf("返回错误 = %v", err)
			}
			assertHistory(t, loop, want)
		})
	}
}

func TestRunTurnInputEOF(t *testing.T) {
	loop := newTestLoop(t)
	err := loop.RunTurn(t.Context(), func(context.Context) (string, error) { return "", io.EOF }, discardOutput, unexpectedConfirm(t))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("预期输入结束，收到 %v", err)
	}
	assertHistory(t, loop, []apis.Message{})
}

func TestRunTurnToolWriteFailure(t *testing.T) {
	user := apis.Message{Role: "user", Content: "调用工具"}
	loop := newTestLoop(t, exchange{[]apis.Message{user}, callStream + "\n\n" + callFinishEvent + doneEvent})
	_, err := loop.db.ExecContext(t.Context(), `CREATE TRIGGER reject_tool BEFORE INSERT ON messages
		WHEN NEW.role = 'tool' BEGIN SELECT RAISE(ABORT, '工具结果写入失败'); END`)
	if err != nil {
		t.Fatal(err)
	}
	err = loop.RunTurn(t.Context(), inputOnce(t, user.Content), discardOutput, func(context.Context, string, string) (bool, error) { return false, nil })
	if err == nil || !strings.Contains(err.Error(), "工具结果写入失败") {
		t.Fatalf("预期工具结果写入错误，收到 %v", err)
	}
	if len(loop.msgs) != 2 || loop.msgs[1].Role != "assistant" {
		t.Fatalf("写入失败后的历史 = %+v", loop.msgs)
	}
	assertHistory(t, loop, []apis.Message{user, loop.msgs[1]})
}

func discardOutput(context.Context, string, OutputRole) error {
	return nil
}

func TestRunTurnOutput(t *testing.T) {
	user := apis.Message{Role: "user", Content: "测试输出"}
	loop := newTestLoop(t, exchange{[]apis.Message{user}, replyStream})
	type fragment struct {
		text string
		role OutputRole
	}
	var got []fragment
	err := loop.RunTurn(t.Context(), inputOnce(t, user.Content), func(_ context.Context, text string, role OutputRole) error {
		got = append(got, fragment{text, role})
		return nil
	}, unexpectedConfirm(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []fragment{{"思考", IsReasoningContent}, {"\n答复", IsContent}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("输出片段 = %+v，预期 %+v", got, want)
	}
}

func TestRunTurnOutputFailure(t *testing.T) {
	for _, failAt := range []string{"思考", "答复"} {
		t.Run(failAt, func(t *testing.T) {
			user := apis.Message{Role: "user", Content: "测试输出失败"}
			loop := newTestLoop(t, exchange{[]apis.Message{user}, replyStream})
			wantErr := errors.New("输出中断")
			failed := false
			err := loop.RunTurn(t.Context(), inputOnce(t, user.Content), func(_ context.Context, text string, _ OutputRole) error {
				if failed {
					t.Error("输出失败后仍收到片段")
				}
				if strings.TrimPrefix(text, "\n") == failAt {
					failed = true
					return wantErr
				}
				return nil
			}, unexpectedConfirm(t))
			if !errors.Is(err, wantErr) {
				t.Fatalf("预期输出中断，收到 %v", err)
			}
			assertHistory(t, loop, []apis.Message{user})
		})
	}
}

func TestNewSessionStartsSeparateHistory(t *testing.T) {
	first := apis.Message{Role: "user", Content: "第一段对话"}
	second := apis.Message{Role: "user", Content: "第二段对话"}
	loop := newTestLoop(t, exchange{[]apis.Message{first}, replyStream}, exchange{[]apis.Message{second}, replyStream})
	if err := loop.RunTurn(t.Context(), inputOnce(t, first.Content), discardOutput, unexpectedConfirm(t)); err != nil {
		t.Fatal(err)
	}
	firstID := loop.SessionId
	if err := loop.NewSession(t.Context(), "新会话"); err != nil {
		t.Fatal(err)
	}
	if loop.SessionId == firstID {
		t.Fatal("新会话复用了旧会话 ID")
	}
	if err := loop.RunTurn(t.Context(), inputOnce(t, second.Content), discardOutput, unexpectedConfirm(t)); err != nil {
		t.Fatal(err)
	}
	assertHistory(t, loop, []apis.Message{second, {Role: "assistant", Content: "答复", ReasoningContent: "思考"}})
	rows, err := storage.QueryMessages(t.Context(), loop.db, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Content != first.Content || rows[1].Content != "答复" {
		t.Fatalf("旧会话历史 = %+v", rows)
	}
}

func unexpectedConfirm(t *testing.T) ConfirmFunc {
	return func(context.Context, string, string) (bool, error) {
		t.Fatal("意外请求执行工具")
		return false, nil
	}
}

func TestToolCallInvalidArguments(t *testing.T) {
	for _, tc := range []struct{ name, args, want string }{
		{"残缺对象", `{"command":`, "必须是 JSON 对象"},
		{"数组", `[]`, "必须是 JSON 对象"},
		{"类型错误", `{"command":12}`, "参数解析错误"},
		{"命令带有未知字段", `{"command":"pwd","cwd":"/tmp"}`, `unknown field "cwd"`},
		{"缺少命令", `{}`, "命令串为空"},
		{"空白命令", `{"command":"  "}`, "命令串为空"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := apis.ToolCall{Id: "a", Function: apis.ToolCallFunction{Name: "bash", Args: tc.args}}
			result, err := new(AgentLoop).runToolCall(t.Context(), call, t.TempDir(), unexpectedConfirm(t))
			if err != nil || !strings.Contains(result, tc.want) {
				t.Fatalf("工具结果 = %q，错误 = %v", result, err)
			}
		})
	}
}

func TestToolCallCommandFailure(t *testing.T) {
	call := apis.ToolCall{Function: apis.ToolCallFunction{Name: "bash", Args: `{"command":"echo diagnostic >&2; exit 7"}`}}
	result, err := new(AgentLoop).runToolCall(t.Context(), call, t.TempDir(), func(context.Context, string, string) (bool, error) { return true, nil })
	if err != nil || !strings.Contains(result, "exit status 7") || !strings.Contains(result, "diagnostic") {
		t.Fatalf("工具结果 = %q，错误 = %v", result, err)
	}
}

func TestRunTurnConfirmInterrupted(t *testing.T) {
	for _, want := range []error{io.EOF, context.Canceled} {
		t.Run(want.Error(), func(t *testing.T) {
			user := apis.Message{Role: "user", Content: "调用工具"}
			nextUser := apis.Message{Role: "user", Content: "继续聊"}
			assistantCalls := apis.Message{
				Role: "assistant",
				ToolCalls: []apis.ToolCall{
					{Id: "a", Type: "function", Function: apis.ToolCallFunction{Name: "bash", Args: `{"command":"cat fixture.txt"}`}},
					{Id: "b", Type: "function", Function: apis.ToolCallFunction{Name: "bash", Args: `{"command":"> denied.txt"}`}},
				},
			}
			canceledTools := []apis.Message{
				{Role: "tool", ToolCallId: "a", Content: "已取消"},
				{Role: "tool", ToolCallId: "b", Content: "已取消"},
			}
			historyAfterCancel := append([]apis.Message{user, assistantCalls}, canceledTools...)

			loop := newTestLoop(
				t,
				exchange{[]apis.Message{user}, callStream + "\n\n" + callFinishEvent + doneEvent},
				exchange{append(historyAfterCancel, nextUser), replyStream},
			)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var toolOutputs []string
			err := loop.RunTurn(ctx, inputOnce(t, user.Content), func(_ context.Context, chunk string, role OutputRole) error {
				if role == IsToolContent {
					toolOutputs = append(toolOutputs, chunk)
				}
				return nil
			}, func(context.Context, string, string) (bool, error) {
				if want == context.Canceled {
					cancel()
					return true, nil
				}
				return false, io.EOF
			})
			if !errors.Is(err, want) {
				t.Fatalf("返回错误 = %v，预期 %v", err, want)
			}
			assertHistory(t, loop, historyAfterCancel)
			if !reflect.DeepEqual(toolOutputs, []string{"已取消", "已取消"}) {
				t.Fatalf("取消后的工具输出回调 = %+v，预期两条已取消", toolOutputs)
			}

			// 重新绑定会话并继续下一轮，确认持久化历史合法可续聊
			reloaded := NewAgentLoop(loop.cfg, loop.db, loop.opts)
			if err := reloaded.BindSession(t.Context(), loop.SessionId); err != nil {
				t.Fatal(err)
			}
			if err := reloaded.RunTurn(t.Context(), inputOnce(t, nextUser.Content), discardOutput, unexpectedConfirm(t)); err != nil {
				t.Fatalf("中断后重绑续聊失败: %v", err)
			}
		})
	}
}

func TestRunTurnStreamCanceled(t *testing.T) {
	t.Run("取消时保留已输出的部分思考与正文且不带工具调用", func(t *testing.T) {
		user := apis.Message{Role: "user", Content: "写一段长回复"}
		partialStream := "data: " + `{"choices":[{"delta":{"reasoning_content":"先想一下"}}]}` + "\n\n" +
			"data: " + `{"choices":[{"delta":{"content":"前半句"}}]}` + "\n\n" +
			"data: " + `{"choices":[{"delta":{"content":"后半句"}}]}` + "\n\n" + stopEvent + doneEvent

		loop := newTestLoop(t, exchange{[]apis.Message{user}, partialStream})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		err := loop.RunTurn(ctx, inputOnce(t, user.Content), func(_ context.Context, text string, role OutputRole) error {
			if role == IsContent && strings.TrimPrefix(text, "\n") == "前半句" {
				cancel()
				return nil
			}
			return ctx.Err()
		}, unexpectedConfirm(t))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("返回错误 = %v，预期 context.Canceled", err)
		}
		assertHistory(t, loop, []apis.Message{
			user,
			{Role: "assistant", ReasoningContent: "先想一下", Content: "前半句"},
		})
	})

	t.Run("尚未收到输出即取消不写入空助手消息", func(t *testing.T) {
		user := apis.Message{Role: "user", Content: "立刻停止"}
		loop := newTestLoop(t, exchange{[]apis.Message{user}, replyStream})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		err := loop.RunTurn(ctx, inputOnce(t, user.Content), func(_ context.Context, _ string, _ OutputRole) error {
			cancel()
			return ctx.Err()
		}, unexpectedConfirm(t))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("返回错误 = %v，预期 context.Canceled", err)
		}
		assertHistory(t, loop, []apis.Message{user})
	})
}

func TestBindSessionContinuesStoredConversation(t *testing.T) {
	ctx := t.Context()
	call := apis.ToolCall{Id: "call-1", Type: "function", Function: apis.ToolCallFunction{Name: "bash", Args: `{"command":"pwd"}`}}
	history := []apis.Message{
		{Role: "user", Content: "查看目录"},
		{Role: "assistant", ReasoningContent: "需要读取工作目录", ToolCalls: []apis.ToolCall{call}},
		{Role: "tool", ToolCallId: call.Id, Content: "/workspace"},
		{Role: "assistant", Content: "目录是 /workspace"},
	}
	next := apis.Message{Role: "user", Content: "刚才的目录是什么？"}
	request := append(history, next)
	loop := newTestLoop(t, exchange{request, replyStream})
	targetID := loop.SessionId
	for _, msg := range history {
		if err := loop.appendMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}

	if err := loop.NewSession(ctx, "另一个会话"); err != nil {
		t.Fatal(err)
	}
	otherID := loop.SessionId
	if err := loop.appendMessage(ctx, apis.Message{Role: "user", Content: "独立的话题"}); err != nil {
		t.Fatal(err)
	}
	before, err := storage.QueryMessages(ctx, loop.db, otherID)
	if err != nil {
		t.Fatal(err)
	}

	if err := loop.BindSession(ctx, targetID); err != nil {
		t.Fatal(err)
	}
	if loop.SessionId != targetID {
		t.Fatalf("绑定的会话 = %d，预期 %d", loop.SessionId, targetID)
	}
	if err := loop.RunTurn(ctx, inputOnce(t, next.Content), discardOutput, unexpectedConfirm(t)); err != nil {
		t.Fatal(err)
	}

	rows, err := storage.QueryMessages(ctx, loop.db, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(request)+1 || rows[len(history)].Content != next.Content || rows[len(request)].Content != "答复" {
		t.Fatalf("续聊后的目标会话 = %+v", rows)
	}
	after, err := storage.QueryMessages(ctx, loop.db, otherID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("其他会话的记录发生变化：%+v", after)
	}
}

func TestBindSessionFailureKeepsCurrentSession(t *testing.T) {
	ctx := t.Context()
	loop := newTestLoop(t)
	currentID := loop.SessionId
	msg := apis.Message{Role: "user", Content: "保留这段历史"}
	if err := loop.appendMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}

	brokenID, err := storage.CreateSession(ctx, loop.db, "历史损坏")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []storage.Message{
		{SessionId: brokenID, Role: "user", Content: "有效的第一条"},
		{SessionId: brokenID, Role: "assistant", ToolCalls: "["},
	} {
		if err := storage.CreateMessage(ctx, loop.db, row); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name string
		id   int64
	}{
		{"会话不存在", brokenID + 1},
		{"工具调用损坏", brokenID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := loop.BindSession(ctx, tc.id); err == nil {
				t.Fatal("预期绑定失败")
			}
			if loop.SessionId != currentID {
				t.Fatalf("绑定失败后会话 ID 变成了 %d", loop.SessionId)
			}
			assertHistory(t, loop, []apis.Message{msg})
		})
	}
}

func TestBindEmptySession(t *testing.T) {
	ctx := t.Context()
	loop := newTestLoop(t)
	if err := loop.appendMessage(ctx, apis.Message{Role: "user", Content: "旧消息"}); err != nil {
		t.Fatal(err)
	}
	emptyID, err := storage.CreateSession(ctx, loop.db, "空会话")
	if err != nil {
		t.Fatal(err)
	}

	if err := loop.BindSession(ctx, emptyID); err != nil {
		t.Fatal(err)
	}
	if loop.SessionId != emptyID || len(loop.msgs) != 0 {
		t.Fatalf("绑定空会话后 ID = %d，历史 = %+v", loop.SessionId, loop.msgs)
	}
}

func TestSetOptsAppliesToNextTurn(t *testing.T) {
	type capturedReq struct {
		Thinking *struct {
			Type string `json:"type"`
		} `json:"thinking"`
		ReasoningEffort string      `json:"reasoning_effort"`
		Tools           []apis.Tool `json:"tools"`
	}

	var gotReqs []capturedReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req capturedReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			http.Error(w, "请求格式错误", http.StatusBadRequest)
			return
		}
		gotReqs = append(gotReqs, req)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, replyStream)
	}))
	t.Cleanup(server.Close)

	db, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	tools := []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}}
	loop := NewAgentLoop(config.Config{Endpoint: server.URL}, db, apis.StreamOptions{
		Thinking:        true,
		ReasoningEffort: "high",
		Tools:           tools,
	})
	if err := loop.NewSession(t.Context(), "选项切换测试"); err != nil {
		t.Fatal(err)
	}

	// 外部修改原切片不应影响 AgentLoop 已保存的选项
	tools[0].Function.Name = "mutated"
	if err := loop.RunTurn(t.Context(), inputOnce(t, "第一轮"), discardOutput, unexpectedConfirm(t)); err != nil {
		t.Fatal(err)
	}

	// 关闭思考与工具后，第二轮应生效
	loop.SetOpts(apis.StreamOptions{
		Thinking:        false,
		ReasoningEffort: "max",
		Tools:           nil,
	})
	if err := loop.RunTurn(t.Context(), inputOnce(t, "第二轮"), discardOutput, unexpectedConfirm(t)); err != nil {
		t.Fatal(err)
	}

	// 重新开启思考与工具，并再次验证外部切片隔离
	nextTools := []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}}
	loop.SetOpts(apis.StreamOptions{
		Thinking:        true,
		ReasoningEffort: "low",
		Tools:           nextTools,
	})
	nextTools[0].Function.Name = "mutated-again"
	if err := loop.RunTurn(t.Context(), inputOnce(t, "第三轮"), discardOutput, unexpectedConfirm(t)); err != nil {
		t.Fatal(err)
	}

	if len(gotReqs) != 3 {
		t.Fatalf("收到 %d 次请求，预期 3 次", len(gotReqs))
	}
	if gotReqs[0].Thinking == nil || gotReqs[0].Thinking.Type != "enabled" ||
		gotReqs[0].ReasoningEffort != "high" || len(gotReqs[0].Tools) != 1 || gotReqs[0].Tools[0].Function.Name != "bash" {
		t.Fatalf("第一轮请求选项不符: %+v", gotReqs[0])
	}
	if gotReqs[1].Thinking == nil || gotReqs[1].Thinking.Type != "disabled" ||
		gotReqs[1].ReasoningEffort != "" || len(gotReqs[1].Tools) != 0 {
		t.Fatalf("第二轮请求选项不符: %+v", gotReqs[1])
	}
	if gotReqs[2].Thinking == nil || gotReqs[2].Thinking.Type != "enabled" ||
		gotReqs[2].ReasoningEffort != "low" || len(gotReqs[2].Tools) != 1 || gotReqs[2].Tools[0].Function.Name != "bash" {
		t.Fatalf("第三轮请求选项不符: %+v", gotReqs[2])
	}
}

func TestSetConfigAppliesToNextTurn(t *testing.T) {
	type captured struct {
		auth  string
		model string
	}

	makeServer := func(out *[]captured) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				http.Error(w, "请求格式错误", http.StatusBadRequest)
				return
			}
			*out = append(*out, captured{
				auth:  r.Header.Get("Authorization"),
				model: body.Model,
			})
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, replyStream)
		}))
		t.Cleanup(server.Close)
		return server
	}

	var firstGot, secondGot []captured
	server1 := makeServer(&firstGot)
	server2 := makeServer(&secondGot)

	db, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	loop := NewAgentLoop(config.Config{
		APIKey:       "key-1",
		Endpoint:     server1.URL,
		DefaultModel: "model-1",
	}, db, apis.StreamOptions{})
	if err := loop.NewSession(t.Context(), "配置切换测试"); err != nil {
		t.Fatal(err)
	}

	if err := loop.RunTurn(t.Context(), inputOnce(t, "第一轮"), discardOutput, unexpectedConfirm(t)); err != nil {
		t.Fatal(err)
	}

	// 切换配置后，第二轮应请求新的 Endpoint、APIKey 和 DefaultModel
	loop.SetConfig(config.Config{
		APIKey:       "key-2",
		Endpoint:     server2.URL,
		DefaultModel: "model-2",
	})
	if err := loop.RunTurn(t.Context(), inputOnce(t, "第二轮"), discardOutput, unexpectedConfirm(t)); err != nil {
		t.Fatal(err)
	}

	if len(firstGot) != 1 || firstGot[0].auth != "Bearer key-1" || firstGot[0].model != "model-1" {
		t.Fatalf("第一轮配置未生效: %+v", firstGot)
	}
	if len(secondGot) != 1 || secondGot[0].auth != "Bearer key-2" || secondGot[0].model != "model-2" {
		t.Fatalf("第二轮新配置未生效: %+v", secondGot)
	}
}
