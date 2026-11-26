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

func newTestLoop(t *testing.T, exchanges ...exchange) (*AgentLoop, *apis.Client) {
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

	store, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	loop := NewAgentLoop(store, nil)
	if err := loop.NewSession(t.Context(), "测试会话"); err != nil {
		t.Fatal(err)
	}
	return loop, &apis.Client{Endpoint: server.URL}
}

func assertHistory(t *testing.T, loop *AgentLoop, want []apis.Message) {
	t.Helper()
	if !reflect.DeepEqual(loop.msgs, want) {
		t.Errorf("内存历史 = %+v，预期 %+v", loop.msgs, want)
	}
	stored, err := loop.store.LoadHistory(t.Context(), loop.SessionId())
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 && len(stored) == 0 {
		return
	}
	if !reflect.DeepEqual(stored, want) {
		t.Errorf("数据库历史 = %+v，预期 %+v", stored, want)
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
	loop, client := newTestLoop(t,
		exchange{history[:1], callStream + "\n\n" + callFinishEvent + doneEvent},
		exchange{history[:4], replyStream},
		exchange{history[:6], replyStream},
	)
	var gotCalls []apis.ToolCall
	var results []string
	output := func(_ context.Context, event OutputEvent) error {
		switch event.Kind {
		case OutputToolCall:
			if event.ToolCall != nil {
				gotCalls = append(gotCalls, *event.ToolCall)
			}
		case OutputToolResult:
			results = append(results, event.Text)
		}
		return nil
	}
	if err := loop.RunTurn(t.Context(), TurnRequest{
		Input:   user.Content,
		Client:  client,
		Output:  output,
		Confirm: confirm,
	}); err != nil {
		t.Fatal(err)
	}
	assertHistory(t, loop, history[:5])
	if !reflect.DeepEqual(gotCalls, []apis.ToolCall{call, secondCall}) {
		t.Fatalf("实时工具调用 = %+v，预期 %+v", gotCalls, []apis.ToolCall{call, secondCall})
	}
	if !reflect.DeepEqual(results, []string{result, "用户拒绝执行"}) {
		t.Fatalf("实时工具输出 = %q", results)
	}
	if !reflect.DeepEqual(confirmed, []string{"cat fixture.txt", "> denied.txt"}) {
		t.Fatalf("确认的命令 = %v", confirmed)
	}
	if _, err := os.Stat("denied.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒绝后检查文件，收到 %v", err)
	}

	if err := loop.RunTurn(t.Context(), TurnRequest{
		Input:   "继续",
		Client:  client,
		Output:  discardOutput,
		Confirm: unexpectedConfirm(t),
	}); err != nil {
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
			loop, client := newTestLoop(t, exchange{want, tc.stream})
			err := loop.RunTurn(t.Context(), TurnRequest{
				Input:   user.Content,
				Client:  client,
				Output:  discardOutput,
				Confirm: unexpectedConfirm(t),
			})
			if (err != nil) != tc.wantError || errors.Is(err, io.ErrUnexpectedEOF) != tc.wantEOF {
				t.Fatalf("返回错误 = %v", err)
			}
			assertHistory(t, loop, want)
		})
	}
}

type rejectToolStore struct {
	Store
	err error
}

func (s rejectToolStore) AppendMessage(ctx context.Context, sessionId int64, msg apis.Message) error {
	if msg.Role == "tool" {
		return s.err
	}
	return s.Store.AppendMessage(ctx, sessionId, msg)
}

func TestRunTurnToolWriteFailure(t *testing.T) {
	user := apis.Message{Role: "user", Content: "调用工具"}
	loop, client := newTestLoop(t, exchange{[]apis.Message{user}, callStream + "\n\n" + callFinishEvent + doneEvent})
	wantErr := errors.New("工具结果写入失败")
	loop.store = rejectToolStore{Store: loop.store, err: wantErr}

	err := loop.RunTurn(t.Context(), TurnRequest{
		Input:   user.Content,
		Client:  client,
		Output:  discardOutput,
		Confirm: func(context.Context, string, string) (bool, error) { return false, nil },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("预期工具结果写入错误，收到 %v", err)
	}
	if len(loop.msgs) != 2 || loop.msgs[1].Role != "assistant" {
		t.Fatalf("写入失败后的历史 = %+v", loop.msgs)
	}
	assertHistory(t, loop, []apis.Message{user, loop.msgs[1]})
}

func discardOutput(context.Context, OutputEvent) error {
	return nil
}

func TestRunTurnOutput(t *testing.T) {
	user := apis.Message{Role: "user", Content: "测试输出"}
	loop, client := newTestLoop(t, exchange{[]apis.Message{user}, replyStream})
	var got []OutputEvent
	err := loop.RunTurn(t.Context(), TurnRequest{
		Input:  user.Content,
		Client: client,
		Output: func(_ context.Context, event OutputEvent) error {
			got = append(got, event)
			return nil
		},
		Confirm: unexpectedConfirm(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []OutputEvent{
		{Kind: OutputReasoning, Text: "思考"},
		{Kind: OutputContent, Text: "答复"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("输出事件 = %+v，预期 %+v", got, want)
	}
}

func TestRunTurnOutputFailure(t *testing.T) {
	for _, failAt := range []string{"思考", "答复"} {
		t.Run(failAt, func(t *testing.T) {
			user := apis.Message{Role: "user", Content: "测试输出失败"}
			loop, client := newTestLoop(t, exchange{[]apis.Message{user}, replyStream})
			wantErr := errors.New("输出中断")
			failed := false
			err := loop.RunTurn(t.Context(), TurnRequest{
				Input:  user.Content,
				Client: client,
				Output: func(_ context.Context, event OutputEvent) error {
					if failed {
						t.Error("输出失败后仍收到片段")
					}
					if event.Text == failAt {
						failed = true
						return wantErr
					}
					return nil
				},
				Confirm: unexpectedConfirm(t),
			})
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
	loop, client := newTestLoop(t, exchange{[]apis.Message{first}, replyStream}, exchange{[]apis.Message{second}, replyStream})
	if err := loop.RunTurn(t.Context(), TurnRequest{
		Input:   first.Content,
		Client:  client,
		Output:  discardOutput,
		Confirm: unexpectedConfirm(t),
	}); err != nil {
		t.Fatal(err)
	}
	firstID := loop.SessionId()
	if err := loop.NewSession(t.Context(), "新会话"); err != nil {
		t.Fatal(err)
	}
	if loop.SessionId() == firstID {
		t.Fatal("新会话复用了旧会话 ID")
	}
	if err := loop.RunTurn(t.Context(), TurnRequest{
		Input:   second.Content,
		Client:  client,
		Output:  discardOutput,
		Confirm: unexpectedConfirm(t),
	}); err != nil {
		t.Fatal(err)
	}
	assertHistory(t, loop, []apis.Message{second, {Role: "assistant", Content: "答复", ReasoningContent: "思考"}})
	rows, err := loop.store.LoadHistory(t.Context(), firstID)
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

			loop, client := newTestLoop(
				t,
				exchange{[]apis.Message{user}, callStream + "\n\n" + callFinishEvent + doneEvent},
				exchange{append(historyAfterCancel, nextUser), replyStream},
			)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var toolOutputs []string
			err := loop.RunTurn(ctx, TurnRequest{
				Input:  user.Content,
				Client: client,
				Output: func(_ context.Context, event OutputEvent) error {
					if event.Kind == OutputToolResult {
						toolOutputs = append(toolOutputs, event.Text)
					}
					return nil
				},
				Confirm: func(context.Context, string, string) (bool, error) {
					if want == context.Canceled {
						cancel()
						return true, nil
					}
					return false, io.EOF
				},
			})
			if !errors.Is(err, want) {
				t.Fatalf("返回错误 = %v，预期 %v", err, want)
			}
			assertHistory(t, loop, historyAfterCancel)
			if !reflect.DeepEqual(toolOutputs, []string{"已取消", "已取消"}) {
				t.Fatalf("取消后的工具输出回调 = %+v，预期两条已取消", toolOutputs)
			}

			// 重新绑定会话并继续下一轮，确认持久化历史合法可续聊
			reloaded := NewAgentLoop(loop.store, nil)
			if err := reloaded.BindSession(t.Context(), loop.SessionId()); err != nil {
				t.Fatal(err)
			}
			if err := reloaded.RunTurn(t.Context(), TurnRequest{
				Input:   nextUser.Content,
				Client:  client,
				Output:  discardOutput,
				Confirm: unexpectedConfirm(t),
			}); err != nil {
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

		loop, client := newTestLoop(t, exchange{[]apis.Message{user}, partialStream})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		err := loop.RunTurn(ctx, TurnRequest{
			Input:  user.Content,
			Client: client,
			Output: func(_ context.Context, event OutputEvent) error {
				if event.Kind == OutputContent && event.Text == "前半句" {
					cancel()
					return nil
				}
				return ctx.Err()
			},
			Confirm: unexpectedConfirm(t),
		})
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
		loop, client := newTestLoop(t, exchange{[]apis.Message{user}, replyStream})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		err := loop.RunTurn(ctx, TurnRequest{
			Input:  user.Content,
			Client: client,
			Output: func(_ context.Context, _ OutputEvent) error {
				cancel()
				return ctx.Err()
			},
			Confirm: unexpectedConfirm(t),
		})
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
	loop, client := newTestLoop(t, exchange{request, replyStream})
	targetID := loop.SessionId()
	for _, msg := range history {
		if err := loop.appendMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}

	if err := loop.NewSession(ctx, "另一个会话"); err != nil {
		t.Fatal(err)
	}
	otherID := loop.SessionId()
	if err := loop.appendMessage(ctx, apis.Message{Role: "user", Content: "独立的话题"}); err != nil {
		t.Fatal(err)
	}
	before, err := loop.store.LoadHistory(ctx, otherID)
	if err != nil {
		t.Fatal(err)
	}

	if err := loop.BindSession(ctx, targetID); err != nil {
		t.Fatal(err)
	}
	if loop.SessionId() != targetID {
		t.Fatalf("绑定的会话 = %d，预期 %d", loop.SessionId(), targetID)
	}
	if err := loop.RunTurn(ctx, TurnRequest{
		Input:   next.Content,
		Client:  client,
		Output:  discardOutput,
		Confirm: unexpectedConfirm(t),
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := loop.store.LoadHistory(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(request)+1 || rows[len(history)].Content != next.Content || rows[len(request)].Content != "答复" {
		t.Fatalf("续聊后的目标会话 = %+v", rows)
	}
	after, err := loop.store.LoadHistory(ctx, otherID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("其他会话的记录发生变化：%+v", after)
	}
}

type failingLoadStore struct {
	Store
	failId int64
	err    error
}

func (s failingLoadStore) LoadHistory(ctx context.Context, sessionId int64) ([]apis.Message, error) {
	if sessionId == s.failId {
		return nil, s.err
	}
	return s.Store.LoadHistory(ctx, sessionId)
}

func TestBindSessionFailureKeepsCurrentSession(t *testing.T) {
	ctx := t.Context()
	loop, _ := newTestLoop(t)
	currentID := loop.SessionId()
	msg := apis.Message{Role: "user", Content: "保留这段历史"}
	if err := loop.appendMessage(ctx, msg); err != nil {
		t.Fatal(err)
	}

	brokenID, err := loop.store.CreateSession(ctx, "历史损坏")
	if err != nil {
		t.Fatal(err)
	}
	loop.store = failingLoadStore{
		Store:  loop.store,
		failId: brokenID,
		err:    errors.New("历史记录损坏"),
	}

	for _, tc := range []struct {
		name string
		id   int64
	}{
		{"会话不存在", brokenID + 1},
		{"读取历史失败", brokenID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := loop.BindSession(ctx, tc.id); err == nil {
				t.Fatal("预期绑定失败")
			}
			if loop.SessionId() != currentID {
				t.Fatalf("绑定失败后会话 ID 变成了 %d", loop.SessionId())
			}
			assertHistory(t, loop, []apis.Message{msg})
		})
	}
}

func TestBindEmptySession(t *testing.T) {
	ctx := t.Context()
	loop, _ := newTestLoop(t)
	if err := loop.appendMessage(ctx, apis.Message{Role: "user", Content: "旧消息"}); err != nil {
		t.Fatal(err)
	}
	emptyID, err := loop.store.CreateSession(ctx, "空会话")
	if err != nil {
		t.Fatal(err)
	}

	if err := loop.BindSession(ctx, emptyID); err != nil {
		t.Fatal(err)
	}
	if loop.SessionId() != emptyID || len(loop.msgs) != 0 {
		t.Fatalf("绑定空会话后 ID = %d，历史 = %+v", loop.SessionId(), loop.msgs)
	}
}

func TestRunTurnPerTurnOptsAndClient(t *testing.T) {
	type capturedReq struct {
		auth     string
		Model    string `json:"model"`
		Thinking *struct {
			Type string `json:"type"`
		} `json:"thinking"`
		ReasoningEffort string      `json:"reasoning_effort"`
		Tools           []apis.Tool `json:"tools"`
	}

	makeServer := func(out *[]capturedReq) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req capturedReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				http.Error(w, "请求格式错误", http.StatusBadRequest)
				return
			}
			req.auth = r.Header.Get("Authorization")
			*out = append(*out, req)
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, replyStream)
		}))
		t.Cleanup(server.Close)
		return server
	}

	var firstReqs, secondReqs []capturedReq
	server1 := makeServer(&firstReqs)
	server2 := makeServer(&secondReqs)

	store, err := storage.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	loop := NewAgentLoop(store, nil)
	if err := loop.NewSession(t.Context(), "单轮参数测试"); err != nil {
		t.Fatal(err)
	}

	// 第一轮：server1 + key-1 + 开启思考与工具
	if err := loop.RunTurn(t.Context(), TurnRequest{
		Input:  "第一轮",
		Client: &apis.Client{Endpoint: server1.URL, APIKey: "key-1", Model: "model-1"},
		Opts: apis.StreamOptions{
			Thinking:        true,
			ReasoningEffort: "high",
			Tools:           []apis.Tool{{Type: "function", Function: apis.FunctionDef{Name: "bash"}}},
		},
		Confirm: unexpectedConfirm(t),
	}); err != nil {
		t.Fatal(err)
	}

	// 第二轮：切到 server2 + key-2 + 关闭思考与工具
	if err := loop.RunTurn(t.Context(), TurnRequest{
		Input:  "第二轮",
		Client: &apis.Client{Endpoint: server2.URL, APIKey: "key-2", Model: "model-2"},
		Opts: apis.StreamOptions{
			Thinking:        false,
			ReasoningEffort: "max",
			Tools:           nil,
		},
		Confirm: unexpectedConfirm(t),
	}); err != nil {
		t.Fatal(err)
	}

	if len(firstReqs) != 1 || firstReqs[0].auth != "Bearer key-1" || firstReqs[0].Model != "model-1" ||
		firstReqs[0].Thinking == nil || firstReqs[0].Thinking.Type != "enabled" ||
		firstReqs[0].ReasoningEffort != "high" || len(firstReqs[0].Tools) != 1 {
		t.Fatalf("第一轮请求不符: %+v", firstReqs)
	}
	if len(secondReqs) != 1 || secondReqs[0].auth != "Bearer key-2" || secondReqs[0].Model != "model-2" ||
		secondReqs[0].Thinking == nil || secondReqs[0].Thinking.Type != "disabled" ||
		secondReqs[0].ReasoningEffort != "" || len(secondReqs[0].Tools) != 0 {
		t.Fatalf("第二轮请求不符: %+v", secondReqs)
	}
}
