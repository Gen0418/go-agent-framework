package apis

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestStreamChatToolCalls(t *testing.T) {
	// 编号二先到，编号零随后到达，参数分片交错返回
	first := `data: {"choices":[{"delta":{"tool_calls":[{"index":2,"id":"call_list","type":"function","function":{"name":"list_files","arguments":"{\"dir\":"}},{"index":0,"id":"call_read","type":"function","function":{"name":"read_test_file","arguments":"{\"path\":\""}}]}}]}` + "\n\n"
	second := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"测试 文件.txt\"}"}},{"index":2,"function":{"arguments":"\"docs\"}"}}]}}]}` + "\n\n"
	finish := sseLine("", "", "tool_calls")
	done := "data: [DONE]\n\n"

	wantFinish := StreamEvent{
		Kind:         EventFinish,
		FinishReason: "tool_calls",
		ToolCalls: []ToolCall{
			{ID: "call_read", Type: "function", Function: ToolCallFunction{Name: "read_test_file", Args: `{"path":"测试 文件.txt"}`}},
			{ID: "call_list", Type: "function", Function: ToolCallFunction{Name: "list_files", Args: `{"dir":"docs"}`}},
		},
	}

	tests := []struct {
		name string
		body string
		want []StreamEvent
	}{
		{"跳号调用按编号返回完整内容", first + second + finish + done, []StreamEvent{wantFinish, {Kind: EventDone}}},
		{"参数中途断流", first, nil},
		{"停止原因之后断流", first + second + finish, []StreamEvent{wantFinish}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			var got []StreamEvent
			for event, err := range testClient(server.URL).StreamChat(t.Context(), []Message{{Role: "user", Content: "读取测试文件"}}, StreamOptions{}) {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, event)
			}

			if !slices.EqualFunc(got, tt.want, equalStreamEvent) {
				t.Fatalf("收到事件 %+v，预期 %+v", got, tt.want)
			}
		})
	}
}

func TestHandleStreamRejectsInvalidToolCalls(t *testing.T) {
	finish := sseLine("", "", "tool_calls") + "data: [DONE]\n\n"
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "调用列表为空",
			body: finish,
			want: "调用列表为空",
		},
		{
			name: "缺少工具ID",
			body: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"bash"}}]}}]} ` + "\n\n" + finish,
			want: "缺少ID",
		},
		{
			name: "工具ID重复",
			body: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"dup","type":"function","function":{"name":"bash"}},{"index":1,"id":"dup","type":"function","function":{"name":"bash"}}]}}]} ` + "\n\n" + finish,
			want: "重复",
		},
		{
			name: "工具类型无效",
			body: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"other","function":{"name":"bash"}}]}}]} ` + "\n\n" + finish,
			want: "类型无效",
		},
		{
			name: "缺少函数名",
			body: `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"  "}}]}}]} ` + "\n\n" + finish,
			want: "缺少函数名",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := drainEvents(strings.NewReader(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v，预期包含 %q", err, tt.want)
			}
		})
	}
}
