package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"go-agent-framework/internal/apis"
)

func TestWebFetchValidationAndSkipConfirm(t *testing.T) {
	neverHTTP := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("参数非法或缺少客户端时不应发起网络请求")
			return nil, nil
		}),
	}
	neverConfirm := func(context.Context, string, string) (bool, error) {
		t.Fatal("web_fetch 为只读工具，不应触发用户确认")
		return false, nil
	}

	reg := NewRegistry(WebFetch{
		Client: func() *apis.Client {
			return &apis.Client{APIKey: "sk-test", HTTP: neverHTTP}
		},
	})

	tooLongURL := "https://example.com/" + strings.Repeat("a", webFetchMaxURLRunes)
	cases := []struct {
		name string
		args string
		want string
	}{
		{"残缺 JSON", `{"url":`, "必须是 JSON 对象"},
		{"未知字段", `{"url":"https://example.com","timeout":10}`, `unknown field "timeout"`},
		{"缺少链接", `{}`, "链接为空"},
		{"空白链接", `{"url":" \t\n "}`, "链接为空"},
		{"非 http 协议", `{"url":"file:///etc/passwd"}`, "合法的 http:// 或 https://"},
		{"缺少主机名", `{"url":"https://"}`, "合法的 http:// 或 https://"},
		{"包含用户名密码", `{"url":"https://user:pass@example.com/page"}`, "不能包含用户名或密码"},
		{"超过8192字符", `{"url":"` + tooLongURL + `"}`, "不能超过 8192 个字符"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := apis.ToolCall{ID: "f1", Function: apis.ToolCallFunction{Name: "web_fetch", Args: tc.args}}
			got, err := reg.Run(t.Context(), t.TempDir(), call, neverConfirm)
			if err != nil || !strings.Contains(got, tc.want) {
				t.Fatalf("结果 = %q，错误 = %v，预期包含 %q", got, err, tc.want)
			}
		})
	}

	t.Run("客户端为空返回模型可见提示", func(t *testing.T) {
		noClientReg := NewRegistry(WebFetch{})
		call := apis.ToolCall{ID: "f2", Function: apis.ToolCallFunction{Name: "web_fetch", Args: `{"url":"https://example.com"}`}}
		got, err := noClientReg.Run(t.Context(), t.TempDir(), call, neverConfirm)
		if err != nil || !strings.Contains(got, "未配置 API 客户端") {
			t.Fatalf("结果 = %q，错误 = %v", got, err)
		}
	})
}

func TestWebFetchExecutionFormattingAndTruncation(t *testing.T) {
	neverConfirm := func(context.Context, string, string) (bool, error) {
		t.Fatal("web_fetch 不应触发用户确认")
		return false, nil
	}

	t.Run("正常抓取与 Markdown 正文格式化", func(t *testing.T) {
		var gotPayload apis.FetchRequest
		client := &apis.Client{
			Endpoint: "https://api.deepseek.com/chat/completions",
			APIKey:   "sk-live",
			HTTP: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
						t.Fatalf("解码请求体失败: %v", err)
					}
					return jsonResponse(http.StatusOK, `{
						"title": "DeepSeek Docs",
						"content": "# 标题\r\n\r\n正文内容"
					}`), nil
				}),
			},
		}

		reg := NewRegistry(WebFetch{Client: func() *apis.Client { return client }})
		call := apis.ToolCall{
			ID:       "f-ok",
			Function: apis.ToolCallFunction{Name: "web_fetch", Args: `{"url":"  https://api-docs.deepseek.com/zh-cn/guides/search/  "}`},
		}

		got, err := reg.Run(t.Context(), t.TempDir(), call, neverConfirm)
		if err != nil {
			t.Fatalf("预期无程序错误，实际: %v", err)
		}
		if gotPayload.URL != "https://api-docs.deepseek.com/zh-cn/guides/search/" {
			t.Fatalf("请求 URL 应去除空白: %+v", gotPayload)
		}
		for _, want := range []string{
			"标题: DeepSeek Docs",
			"URL: https://api-docs.deepseek.com/zh-cn/guides/search/",
			"# 标题\n\n正文内容",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("输出缺少 %q，完整输出:\n%s", want, got)
			}
		}
	})

	t.Run("空正文与超长正文截断", func(t *testing.T) {
		empty := formatFetchResult("https://example.com/empty", apis.FetchResponse{Title: "", Content: "  \n "})
		if !strings.Contains(empty, "未能提取到网页正文内容") || strings.Contains(empty, "标题:") {
			t.Fatalf("空正文格式异常:\n%s", empty)
		}

		huge := formatFetchResult("https://example.com/huge", apis.FetchResponse{
			Title:   "Huge Doc",
			Content: strings.Repeat("文", webFetchMaxContentRunes+200),
		})
		if !strings.Contains(huge, "已截断至前 16000 字符") {
			t.Fatalf("超长正文应截断并附提示:\n%s", huge[len(huge)-120:])
		}
	})

	t.Run("API 错误转为模型可见文本，上下文取消返回程序错误", func(t *testing.T) {
		errClient := &apis.Client{
			APIKey: "sk-test",
			HTTP: &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return jsonResponse(http.StatusTooManyRequests, `rate limit`), nil
				}),
			},
		}
		reg := NewRegistry(WebFetch{Client: func() *apis.Client { return errClient }})
		call := apis.ToolCall{ID: "f-429", Function: apis.ToolCallFunction{Name: "web_fetch", Args: `{"url":"https://example.com"}`}}
		got, err := reg.Run(t.Context(), t.TempDir(), call, neverConfirm)
		if err != nil || !strings.Contains(got, "web_fetch 失败") || !strings.Contains(got, "HTTP 429") {
			t.Fatalf("429 结果 = %q, err = %v", got, err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		cancelClient := &apis.Client{
			APIKey: "sk-test",
			HTTP: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					cancel()
					<-r.Context().Done()
					return nil, r.Context().Err()
				}),
			},
		}
		cancelReg := NewRegistry(WebFetch{Client: func() *apis.Client { return cancelClient }})
		_, err = cancelReg.Run(ctx, t.TempDir(), call, neverConfirm)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消时返回 err = %v，预期 context.Canceled", err)
		}
	})
}
