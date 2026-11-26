package apis

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

// ---------- 测试辅助 ----------

// sseLine 构造一行 SSE data。三个增量字段均可选，空串不出现在 JSON 中，
// 用于模拟 DeepSeek 各种形态的 chunk（首块/思考块/混合块/末块）。
func sseLine(reasoning, content, finish string) string {
	delta := map[string]string{}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}
	if content != "" {
		delta["content"] = content
	}
	choice := map[string]any{"delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	b, _ := json.Marshal(map[string]any{"choices": []any{choice}})
	return "data: " + string(b) + "\n\n"
}

// chunkLine 拼一行只有 content 增量的 SSE data。
func chunkLine(content string) string {
	return sseLine("", content, "")
}

// testClient 构造指向指定端点的测试客户端，key/model 用固定占位值。
func testClient(endpoint string) *Client {
	return &Client{
		Endpoint: endpoint,
		APIKey:   "test-key",
		Model:    "deepseek-flash",
	}
}

// drain 消费 handleStream，只聚合 EventContent 的文本，返回聚合结果与终止错误。
func drain(body io.Reader) (string, error) {
	var sb strings.Builder
	err := handleStream(body, func(ev StreamEvent, err error) bool {
		if err != nil {
			// handleStream 的契约是不通过 yield 报错，这里出现即为违约
			panic(fmt.Sprintf("handleStream 不该用 yield 传错误: %v", err))
		}
		if ev.Kind == EventContent {
			sb.WriteString(ev.Text)
		}
		return true
	})
	return sb.String(), err
}

// drainEvents 消费 handleStream，返回完整事件序列，用于断言 Kind 顺序与携带字段。
func drainEvents(body io.Reader) ([]StreamEvent, error) {
	var evs []StreamEvent
	err := handleStream(body, func(ev StreamEvent, err error) bool {
		if err != nil {
			panic(fmt.Sprintf("handleStream 不该用 yield 传错误: %v", err))
		}
		evs = append(evs, ev)
		return true
	})
	return evs, err
}

func equalStreamEvent(got, want StreamEvent) bool {
	return got.Kind == want.Kind &&
		got.Text == want.Text &&
		got.FinishReason == want.FinishReason &&
		slices.Equal(got.ToolCalls, want.ToolCalls)
}

// ---------- 层 3：parseStreamChunk（纯函数）----------

// String 决定测试失败信息与日志里 %v 的可读性，必须精确。
func TestEventKindString(t *testing.T) {
	tests := []struct {
		kind EventKind
		want string
	}{
		{EventReasoning, "reasoning"},
		{EventContent, "content"},
		{EventFinish, "finish"},
		{EventDone, "done"},
		{EventKind(99), "unknown"}, // 越界值兜底，不能 panic
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("EventKind(%d).String() = %q, want %q", int(tt.kind), got, tt.want)
		}
	}
}

func TestParseStreamChunk(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantContent   string
		wantReasoning string
		wantFinish    string
		wantChoices   int
		wantErr       bool
	}{
		{
			name:        "正常增量块",
			raw:         `{"choices":[{"delta":{"content":"你好"},"index":0}]}`,
			wantContent: "你好",
			wantChoices: 1,
		},
		{
			name:        "首块只带 role，content 为空",
			raw:         `{"choices":[{"delta":{"role":"assistant","content":""},"index":0}]}`,
			wantContent: "",
			wantChoices: 1,
		},
		{
			name:          "思考块带 reasoning_content",
			raw:           `{"choices":[{"delta":{"reasoning_content":"让我想想"},"index":0}]}`,
			wantReasoning: "让我想想",
			wantChoices:   1,
		},
		{
			name:          "混合块同时带思考与回答",
			raw:           `{"choices":[{"delta":{"reasoning_content":"想","content":"答"},"index":0}]}`,
			wantContent:   "答",
			wantReasoning: "想",
			wantChoices:   1,
		},
		{
			name:        "末块带 finish_reason",
			raw:         `{"choices":[{"delta":{},"finish_reason":"stop","index":0}]}`,
			wantFinish:  "stop",
			wantChoices: 1,
		},
		{
			name:        "choices 为空数组",
			raw:         `{"choices":[]}`,
			wantChoices: 0,
		},
		{
			// DeepSeek 的真实响应带大量本结构体未声明的字段，必须被静默忽略
			name:        "含未声明字段（id/created/usage/logprobs）",
			raw:         `{"id":"x","created":1,"model":"deepseek-flash","usage":{"total_tokens":18},"choices":[{"delta":{"content":"a"},"logprobs":null,"index":0}],"system_fingerprint":"f"}`,
			wantContent: "a",
			wantChoices: 1,
		},
		{
			name:    "畸形 JSON 返回错误",
			raw:     `{"choices":`,
			wantErr: true,
		},
		{
			name:    "空字符串返回错误",
			raw:     ``,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunk, err := parseStreamChunk(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got := len(chunk.Choices); got != tt.wantChoices {
				t.Fatalf("len(Choices) = %d, want %d", got, tt.wantChoices)
			}
			if tt.wantChoices == 0 {
				return
			}
			if got := chunk.Choices[0].Delta.Content; got != tt.wantContent {
				t.Errorf("Content = %q, want %q", got, tt.wantContent)
			}
			if got := chunk.Choices[0].Delta.ReasoningContent; got != tt.wantReasoning {
				t.Errorf("ReasoningContent = %q, want %q", got, tt.wantReasoning)
			}
			if got := chunk.Choices[0].FinishReason; got != tt.wantFinish {
				t.Errorf("FinishReason = %q, want %q", got, tt.wantFinish)
			}
		})
	}
}

// ---------- 层 2：handleStream ----------

func TestHandleStream(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{
			name: "正常流：跳过 role 块与空 content 块，[DONE] 终止",
			body: `data: {"choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
				chunkLine("你好") +
				chunkLine("，") +
				chunkLine("世界") +
				`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n",
			want: "你好，世界",
		},
		{
			name: "无结束标记时仍返回已收到的正文",
			body: chunkLine("abc"),
			want: "abc",
		},
		{
			name: "空输入",
			body: "",
			want: "",
		},
		{
			name: "跳过空行、心跳注释行与非 data 字段",
			body: "\n" +
				": keep-alive\n" +
				"\n" +
				"event: message\n" +
				"id: 42\n" +
				chunkLine("ok") +
				"\n" +
				"data: [DONE]\n\n",
			want: "ok",
		},
		{
			name: "CRLF 行尾",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"crlf\"}}]}\r\n" +
				"\r\n" +
				"data: [DONE]\r\n\r\n",
			want: "crlf",
		},
		{
			name: "data: 后无空格也能解析",
			body: "data:{\"choices\":[{\"delta\":{\"content\":\"nospace\"}}]}\n\n" +
				"data: [DONE]\n\n",
			want: "nospace",
		},
		{
			name: "choices 为空数组的块被跳过",
			body: "data: {\"choices\":[]}\n\n" +
				chunkLine("a") +
				"data: [DONE]\n\n",
			want: "a",
		},
		{
			name: "content 含转义与多字节字符",
			body: chunkLine("引号\"反斜杠\\换行\n emoji 👋") +
				"data: [DONE]\n\n",
			want: "引号\"反斜杠\\换行\n emoji 👋",
		},
		{
			name: "[DONE] 之后的数据不再被处理",
			body: chunkLine("before") +
				"data: [DONE]\n\n" +
				chunkLine("after"),
			want: "before",
		},
		{
			name:    "畸形 JSON 报错而非静默跳过",
			body:    "data: {\"choices\":\n\n",
			wantErr: true,
		},
		{
			name:    "[DONE] 不是合法 JSON，但必须先被哨兵拦截",
			body:    "data: [DONE]\n\n",
			want:    "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := drain(strings.NewReader(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// 验证思考、正文、停止原因和流结束标记的事件顺序
func TestHandleStreamEvents(t *testing.T) {
	t.Run("思考流与回答流交替，事件按块序产出", func(t *testing.T) {
		body := sseLine("思考1", "", "") +
			sseLine("思考2", "", "") +
			sseLine("", "回答1", "") +
			sseLine("", "", "stop") +
			"data: [DONE]\n\n"
		evs, err := drainEvents(strings.NewReader(body))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		want := []struct {
			kind EventKind
			text string
		}{
			{EventReasoning, "思考1"},
			{EventReasoning, "思考2"},
			{EventContent, "回答1"},
			{EventFinish, ""},
			{EventDone, ""},
		}
		if len(evs) != len(want) {
			t.Fatalf("事件数 = %d, want %d: %+v", len(evs), len(want), evs)
		}
		for i, w := range want {
			if evs[i].Kind != w.kind {
				t.Fatalf("evs[%d].Kind = %v, want %v（全序列: %v）", i, evs[i].Kind, w.kind, evs)
			}
			if evs[i].Text != w.text {
				t.Errorf("evs[%d].Text = %q, want %q", i, evs[i].Text, w.text)
			}
		}
	})

	t.Run("同一 chunk 同时带思考与回答，先产出 reasoning 再产出 content", func(t *testing.T) {
		evs, err := drainEvents(strings.NewReader(sseLine("先想", "后答", "") + "data: [DONE]\n\n"))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(evs) != 3 || evs[2].Kind != EventDone {
			t.Fatalf("事件序列 = %+v，预期为思考、正文、流结束", evs)
		}
		if evs[0].Kind != EventReasoning || evs[0].Text != "先想" {
			t.Errorf("evs[0] = %+v, want reasoning/先想", evs[0])
		}
		if evs[1].Kind != EventContent || evs[1].Text != "后答" {
			t.Errorf("evs[1] = %+v, want content/后答", evs[1])
		}
	})

	t.Run("finish 事件携带 FinishReason，Text 必须为空", func(t *testing.T) {
		evs, err := drainEvents(strings.NewReader(sseLine("", "", "content_filter") + "data: [DONE]\n\n"))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(evs) != 2 || evs[0].Kind != EventFinish || evs[1].Kind != EventDone {
			t.Fatalf("事件序列 = %+v，预期为停止原因、流结束", evs)
		}
		if evs[0].FinishReason != "content_filter" {
			t.Errorf("FinishReason = %q, want content_filter", evs[0].FinishReason)
		}
		if evs[0].Text != "" {
			t.Errorf("finish 事件的 Text = %q, want 空（finish_reason 不是文本）", evs[0].Text)
		}
	})

	t.Run("纯思考流不产出任何 content 事件", func(t *testing.T) {
		evs, err := drainEvents(strings.NewReader(
			sseLine("只思考", "", "") + sseLine("", "", "length") + "data: [DONE]\n\n"))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(evs) != 3 || evs[0].Kind != EventReasoning || evs[1].Kind != EventFinish || evs[2].Kind != EventDone {
			t.Fatalf("事件序列 = %+v，预期为思考、停止原因、流结束", evs)
		}
	})

	t.Run("role 首块与空 choices 块不产出事件", func(t *testing.T) {
		body := `data: {"choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
			`data: {"choices":[]}` + "\n\n" +
			"data: [DONE]\n\n"
		evs, err := drainEvents(strings.NewReader(body))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(evs) != 1 || !equalStreamEvent(evs[0], StreamEvent{Kind: EventDone}) {
			t.Fatalf("事件序列 = %+v，预期只有流结束事件", evs)
		}
	})

	t.Run("收到停止原因后直接读到文件末尾，不产出流结束事件", func(t *testing.T) {
		body := chunkLine("半截回复") + sseLine("", "", "stop")
		evs, err := drainEvents(strings.NewReader(body))
		if err != nil {
			t.Fatalf("读取流失败: %v", err)
		}

		if len(evs) != 2 || !equalStreamEvent(evs[0], StreamEvent{Kind: EventContent, Text: "半截回复"}) ||
			!equalStreamEvent(evs[1], StreamEvent{Kind: EventFinish, FinishReason: "stop"}) {
			t.Fatalf("事件序列 = %+v，预期只有正文和停止原因", evs)
		}
	})
}

// 消费者 break 时 handleStream 必须返回 nil，且不再继续读流。
// 三处 yield 点（思考/回答/结束）任何一处收到 false 都必须立即停。
func TestHandleStreamBreakIsNotError(t *testing.T) {
	t.Run("回答事件后 break", func(t *testing.T) {
		body := chunkLine("a") + chunkLine("b") + chunkLine("c")

		var n int
		err := handleStream(strings.NewReader(body), func(StreamEvent, error) bool {
			n++
			return n < 2 // 收到第 2 块就 break
		})
		if err != nil {
			t.Fatalf("break 不该产生错误: %v", err)
		}
		if n != 2 {
			t.Fatalf("yield 被调了 %d 次, want 2（第 3 块不该被读取）", n)
		}
	})

	t.Run("思考事件就 break，不再读到回答", func(t *testing.T) {
		body := sseLine("思考", "", "") + chunkLine("答案") + "data: [DONE]\n\n"

		var kinds []EventKind
		err := handleStream(strings.NewReader(body), func(ev StreamEvent, err error) bool {
			kinds = append(kinds, ev.Kind)
			return false // 第一个事件就 break
		})
		if err != nil {
			t.Fatalf("break 不该产生错误: %v", err)
		}
		if len(kinds) != 1 || kinds[0] != EventReasoning {
			t.Fatalf("kinds = %v, want 仅 [reasoning]", kinds)
		}
	})

	t.Run("结束事件后 break", func(t *testing.T) {
		body := sseLine("", "", "stop") + chunkLine("不该出现")

		var n int
		err := handleStream(strings.NewReader(body), func(StreamEvent, error) bool {
			n++
			return false
		})
		if err != nil {
			t.Fatalf("break 不该产生错误: %v", err)
		}
		if n != 1 {
			t.Fatalf("yield 被调了 %d 次, want 1", n)
		}
	})
}

// 边界值：行长恰好跨越 scanner 缓冲区上限。
func TestHandleStreamLineLengthBoundary(t *testing.T) {
	t.Run("100KB 行在上限内，应正常解析", func(t *testing.T) {
		big := strings.Repeat("a", 100*1024)
		got, err := drain(strings.NewReader(chunkLine(big) + "data: [DONE]\n\n"))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if len(got) != len(big) {
			t.Fatalf("got %d 字节, want %d", len(got), len(big))
		}
	})

	t.Run("2MB 行超过上限，应报 ErrTooLong 而非静默截断", func(t *testing.T) {
		big := strings.Repeat("a", 2*1024*1024)
		_, err := drain(strings.NewReader(chunkLine(big) + "data: [DONE]\n\n"))
		if err == nil {
			t.Fatal("期望超长行报错，实际 err = nil")
		}
		if !strings.Contains(err.Error(), bufio.ErrTooLong.Error()) &&
			err != bufio.ErrTooLong {
			t.Fatalf("err = %v, 期望包含 %v", err, bufio.ErrTooLong)
		}
	})
}

// 流中途断开必须被报为错误，不能被当成"正常读完"。
func TestHandleStreamTruncatedStream(t *testing.T) {
	_, err := drain(&errReader{
		data: chunkLine("partial"),
		err:  fmt.Errorf("connection reset by peer"),
	})
	if err == nil {
		t.Fatal("底层读错误被吞掉了，scanner.Err() 没有向上冒")
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("err = %v, 期望保留底层错误信息", err)
	}
}

// errReader 先返回一段数据，之后的读取一律失败，用于模拟网络中断。
type errReader struct {
	data string
	err  error
	eof  bool
}

func (r *errReader) Read(p []byte) (int, error) {
	if !r.eof {
		r.eof = true
		return copy(p, r.data), nil
	}
	return 0, r.err
}

// 标准库 iotest 提供的边界 reader。这些是签名收成 io.Reader 之后才用得上的：
// 它们验证解析不依赖"一次 Read 就读满整行"这类隐含假设。
func TestHandleStreamReaderBoundaries(t *testing.T) {
	body := chunkLine("hello") + chunkLine(" world") + "data: [DONE]\n\n"

	t.Run("HalfReader：每次只返回请求量的一半（短读）", func(t *testing.T) {
		got, err := drain(iotest.HalfReader(strings.NewReader(body)))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got != "hello world" {
			t.Fatalf("got %q, want %q", got, "hello world")
		}
	})

	t.Run("DataErrReader：最后一批数据与 EOF 在同一次 Read 返回", func(t *testing.T) {
		got, err := drain(iotest.DataErrReader(strings.NewReader(body)))
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if got != "hello world" {
			t.Fatalf("got %q, want %q", got, "hello world")
		}
	})

	t.Run("ErrReader：所有 Read 都失败", func(t *testing.T) {
		wantErr := errors.New("boom")
		_, err := drain(iotest.ErrReader(wantErr))
		if err == nil {
			t.Fatal("期望错误，实际 err = nil")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, 期望能 errors.Is 到 %v", err, wantErr)
		}
	})
}

// ---------- 层 1：StreamChat ----------

// 端到端：请求头、请求体契约、流式响应聚合。
func TestStreamChatRequestContract(t *testing.T) {
	type captured struct {
		header http.Header
		req    chatRequest
	}
	got := make(chan captured, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var cr chatRequest
		if err := json.Unmarshal(b, &cr); err != nil {
			t.Errorf("请求体不是合法 JSON: %v (%q)", err, b)
		}
		got <- captured{header: r.Header.Clone(), req: cr}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"role":"assistant","content":""}}]}`+"\n\n")
		fmt.Fprint(w, chunkLine("pong"))
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cli := testClient(srv.URL)
	var sb strings.Builder
	for ev, err := range cli.StreamChat(context.Background(),
		[]Message{{Role: "user", Content: "ping"}}, StreamOptions{}) {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if ev.Kind == EventContent {
			sb.WriteString(ev.Text)
		}
	}
	if sb.String() != "pong" {
		t.Fatalf("聚合结果 = %q, want %q", sb.String(), "pong")
	}

	c := <-got

	// 请求头
	if h := c.header.Get("Content-Type"); h != "application/json" {
		t.Errorf("Content-Type = %q", h)
	}
	if h := c.header.Get("Accept"); h != "text/event-stream" {
		t.Errorf("Accept = %q", h)
	}
	if want := "Bearer " + cli.APIKey; c.header.Get("Authorization") != want {
		t.Errorf("Authorization = %q, want %q", c.header.Get("Authorization"), want)
	}

	// 请求体契约
	if c.req.Model != cli.Model {
		t.Errorf("Model = %q, want %q（配置下发的型号应原样进请求）", c.req.Model, cli.Model)
	}
	if !c.req.Stream {
		t.Error("Stream = false, 流式请求必须为 true")
	}
	// V4.1 flash 思考模式默认开启，零值 opts 必须显式关闭，
	// 否则前半段全是 reasoning_content
	if c.req.Thinking == nil {
		t.Fatal("Thinking 字段缺失，思考模式不会被关闭")
	}
	if c.req.Thinking.Type != "disabled" {
		t.Errorf("Thinking.Type = %q, want disabled", c.req.Thinking.Type)
	}
	if len(c.req.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(c.req.Messages))
	}
	if c.req.Messages[0].Role != "user" || c.req.Messages[0].Content != "ping" {
		t.Errorf("Messages[0] = %+v", c.req.Messages[0])
	}
}

// opts 到请求体的翻译契约：思考开关、effort 与 thinking 的耦合、omitempty。
// 字段缺失必须用原始 JSON 断言——unmarshal 之后空串与字段缺失无法区分。
func TestStreamChatRequestThinkingContract(t *testing.T) {
	tests := []struct {
		name       string
		opts       StreamOptions
		wantType   string
		wantEffort string // "" 表示该字段必须不出现
	}{
		{"零值：思考关闭", StreamOptions{}, "disabled", ""},
		{"开启思考不给 effort，交给服务端默认", StreamOptions{Thinking: true}, "enabled", ""},
		{"开启思考并指定 effort", StreamOptions{Thinking: true, ReasoningEffort: "low"}, "enabled", "low"},
		{"关闭思考但给了 effort：必须被忽略", StreamOptions{ReasoningEffort: "low"}, "disabled", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type captured struct {
				body string
			}
			got := make(chan captured, 1)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				got <- captured{body: string(b)}

				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer srv.Close()

			for _, err := range testClient(srv.URL).StreamChat(context.Background(),
				[]Message{{Role: "user", Content: "hi"}}, tt.opts) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
			}

			c := <-got
			var cr chatRequest
			if err := json.Unmarshal([]byte(c.body), &cr); err != nil {
				t.Fatalf("请求体不是合法 JSON: %v (%s)", err, c.body)
			}

			if cr.Thinking == nil {
				t.Fatalf("thinking 字段缺失: %s", c.body)
			}
			if cr.Thinking.Type != tt.wantType {
				t.Errorf("thinking.type = %q, want %q（原始: %s）", cr.Thinking.Type, tt.wantType, c.body)
			}

			if tt.wantEffort == "" {
				if strings.Contains(c.body, "reasoning_effort") {
					t.Errorf("reasoning_effort 不该出现在请求体中: %s", c.body)
				}
			} else if !strings.Contains(c.body, `"reasoning_effort":"`+tt.wantEffort+`"`) {
				t.Errorf("reasoning_effort 缺失或值不对, want %q（原始: %s）", tt.wantEffort, c.body)
			}
		})
	}
}

// 验证开启思考时，从请求到结束标记的完整事件序列
func TestStreamChatThinkingFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"role":"assistant","content":""}}]}`+"\n\n")
		fmt.Fprint(w, sseLine("思考A", "", ""))
		fmt.Fprint(w, sseLine("思考B", "", ""))
		fmt.Fprint(w, sseLine("", "回答1", ""))
		fmt.Fprint(w, sseLine("", "回答2", ""))
		fmt.Fprint(w, sseLine("", "", "stop"))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	var evs []StreamEvent
	for ev, err := range testClient(srv.URL).StreamChat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, StreamOptions{Thinking: true}) {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		evs = append(evs, ev)
	}

	wantKinds := []EventKind{EventReasoning, EventReasoning, EventContent, EventContent, EventFinish, EventDone}
	if len(evs) != len(wantKinds) {
		t.Fatalf("事件数 = %d, want %d: %+v", len(evs), len(wantKinds), evs)
	}
	for i, want := range wantKinds {
		if evs[i].Kind != want {
			t.Fatalf("evs[%d].Kind = %v, want %v（全序列: %v）", i, evs[i].Kind, want, evs)
		}
	}
	if finish := evs[len(evs)-2]; finish.FinishReason != "stop" {
		t.Errorf("停止原因 = %q，预期为 stop", finish.FinishReason)
	}
}

// 非 200 时错误必须带上状态码与响应体片段，否则 401 与 429 无法区分。
func TestStreamChatNon200(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"401 密钥错误", http.StatusUnauthorized, `{"error":{"message":"invalid api key"}}`},
		{"429 触发限流", http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`},
		{"500 服务端错误", http.StatusInternalServerError, `internal error`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			var yielded bool
			for ev, err := range testClient(srv.URL).StreamChat(context.Background(),
				[]Message{{Role: "user", Content: "hi"}}, StreamOptions{}) {
				yielded = true
				if err == nil {
					t.Fatalf("期望错误，却收到事件 %+v", ev)
				}
				if !strings.Contains(err.Error(), fmt.Sprint(tt.status)) {
					t.Errorf("错误 %q 不含状态码 %d", err, tt.status)
				}
				if !strings.Contains(err.Error(), tt.body) {
					t.Errorf("错误 %q 不含响应体片段 %q", err, tt.body)
				}
				if !equalStreamEvent(ev, StreamEvent{}) {
					t.Errorf("出错时事件应为零值，实际 %+v", ev)
				}
			}
			if !yielded {
				t.Fatal("期望 yield 一次错误，实际迭代器没有产出任何值")
			}
		})
	}
}

// 惰性契约：只构造 seq 不发请求；range 时才发。
func TestStreamChatIsLazy(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	seq := testClient(srv.URL).StreamChat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, StreamOptions{})
	time.Sleep(100 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("仅构造 seq 就发出了 %d 次请求，期望 0（应完全惰性）", n)
	}

	for _, err := range seq {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("range 后请求次数 = %d, want 1", n)
	}
}

// 初始化在闭包内，所以同一个 seq 可以重复消费，每次都是一轮完整请求。
func TestStreamChatSeqReusable(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, chunkLine("pong"))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	seq := testClient(srv.URL).StreamChat(context.Background(),
		[]Message{{Role: "user", Content: "ping"}}, StreamOptions{})
	for i := 1; i <= 2; i++ {
		var sb strings.Builder
		for ev, err := range seq {
			if err != nil {
				t.Fatalf("第 %d 次消费: %v", i, err)
			}
			if ev.Kind == EventContent {
				sb.WriteString(ev.Text)
			}
		}
		if sb.String() != "pong" {
			t.Fatalf("第 %d 次消费得到 %q, want pong", i, sb.String())
		}
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("服务器被调用 %d 次, want 2", n)
	}
}

// 消费者 break 后连接必须被关闭，否则泄漏。
func TestStreamChatBreakClosesConnection(t *testing.T) {
	disconnected := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter 不支持 http.Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			select {
			case <-r.Context().Done(): // 客户端断开连接
				select {
				case disconnected <- struct{}{}:
				default:
				}
				return
			default:
			}
			fmt.Fprint(w, chunkLine("x"))
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	n := 0
	for ev, err := range testClient(srv.URL).StreamChat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, StreamOptions{}) {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if ev.Kind != EventContent || ev.Text != "x" {
			t.Fatalf("ev = %+v, want content/x", ev)
		}
		n++
		if n == 3 {
			break
		}
	}
	if n != 3 {
		t.Fatalf("收到 %d 块, want 3", n)
	}

	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("break 后服务器未观察到连接关闭：defer resp.Body.Close() 可能没生效")
	}
}

// ctx 取消必须中断读流并通过 yield 报错，不能静默结束。
func TestStreamChatContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("ResponseWriter 不支持 http.Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			fmt.Fprint(w, chunkLine("x"))
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()

	// 外层再套一个超时，保证 cancel 万一不生效时测试也不会挂死
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var gotErr error
	n := 0
	for _, err := range testClient(srv.URL).StreamChat(ctx,
		[]Message{{Role: "user", Content: "hi"}}, StreamOptions{}) {
		if err != nil {
			gotErr = err
			break
		}
		n++
		if n == 2 {
			cancel()
		}
	}

	if gotErr == nil {
		t.Fatal("ctx 取消后应通过 yield 收到错误，实际流被当成正常结束")
	}
	t.Logf("ctx 取消后收到错误（%d 块之后）: %v", n, gotErr)
}

// 端点不可达时的错误路径。
func TestStreamChatUnreachableEndpoint(t *testing.T) {
	var yielded bool
	for _, err := range testClient("http://127.0.0.1:1/chat/completions").StreamChat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, StreamOptions{}) {
		yielded = true
		if err == nil {
			t.Fatal("期望错误")
		}
	}
	if !yielded {
		t.Fatal("期望 yield 一次错误")
	}
}

// 非法 URL 导致 NewRequestWithContext 失败，错误同样只能通过 yield 报出。
func TestStreamChatInvalidEndpoint(t *testing.T) {
	var gotErr error
	for _, err := range testClient("://not-a-valid-url").StreamChat(context.Background(),
		[]Message{{Role: "user", Content: "hi"}}, StreamOptions{}) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if gotErr == nil {
		t.Fatal("非法 URL 应通过 yield 报出错误")
	}
	if !strings.Contains(gotErr.Error(), "://not-a-valid-url") {
		t.Logf("错误信息未包含原始 URL: %v", gotErr)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// 验证 Client.HTTP 可注入自定义传输层，不再强制走包级全局客户端。
func TestClientCustomHTTP(t *testing.T) {
	called := false
	cli := &Client{
		Endpoint: "https://example.invalid/chat/completions",
		APIKey:   "custom-key",
		Model:    "custom-model",
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				called = true
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(chunkLine("ok") + "data: [DONE]\n\n")),
				}, nil
			}),
		},
	}

	var got strings.Builder
	for ev, err := range cli.StreamChat(context.Background(), []Message{{Role: "user", Content: "hi"}}, StreamOptions{}) {
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if ev.Kind == EventContent {
			got.WriteString(ev.Text)
		}
	}
	if !called || got.String() != "ok" {
		t.Fatalf("自定义 HTTP 未生效: called=%v, got=%q", called, got.String())
	}
}
