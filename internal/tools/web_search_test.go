package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"go-agent-framework/internal/apis"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestWebSearchValidationAndSkipConfirm(t *testing.T) {
	neverHTTP := &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("参数非法或缺少客户端时不应发起网络请求")
			return nil, nil
		}),
	}
	neverConfirm := func(context.Context, string, string) (bool, error) {
		t.Fatal("web_search 为只读工具，不应触发用户确认")
		return false, nil
	}

	reg := NewRegistry(WebSearch{
		Client: func() *apis.Client {
			return &apis.Client{APIKey: "sk-test", HTTP: neverHTTP}
		},
	})

	cases := []struct {
		name string
		args string
		want string
	}{
		{"残缺 JSON", `{"query":`, "必须是 JSON 对象"},
		{"未知字段", `{"query":"golang","need_content":true}`, `unknown field "need_content"`},
		{"缺少查询词", `{}`, "查询词为空"},
		{"空白查询词", `{"query":" \t\n "}`, "查询词为空"},
		{"查询词超过128字符", `{"query":"` + strings.Repeat("中", 129) + `"}`, "不能超过 128 个字符"},
		{"count 小于 1", `{"query":"go","count":0}`, "必须在 1 到 50 之间"},
		{"count 大于 50", `{"query":"go","count":51}`, "必须在 1 到 50 之间"},
		{"count 类型错误", `{"query":"go","count":"5"}`, "参数解析错误"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := apis.ToolCall{ID: "s1", Function: apis.ToolCallFunction{Name: "web_search", Args: tc.args}}
			got, err := reg.Run(t.Context(), t.TempDir(), call, neverConfirm)
			if err != nil || !strings.Contains(got, tc.want) {
				t.Fatalf("结果 = %q，错误 = %v，预期包含 %q", got, err, tc.want)
			}
		})
	}

	t.Run("客户端为空返回模型可见提示", func(t *testing.T) {
		noClientReg := NewRegistry(WebSearch{})
		call := apis.ToolCall{ID: "s2", Function: apis.ToolCallFunction{Name: "web_search", Args: `{"query":"golang"}`}}
		got, err := noClientReg.Run(t.Context(), t.TempDir(), call, neverConfirm)
		if err != nil || !strings.Contains(got, "未配置 API 客户端") {
			t.Fatalf("结果 = %q，错误 = %v", got, err)
		}
	})
}

func TestWebSearchExecutionAndFormatting(t *testing.T) {
	neverConfirm := func(context.Context, string, string) (bool, error) {
		t.Fatal("web_search 不应触发用户确认")
		return false, nil
	}

	t.Run("正常调用、参数规范化与结果格式化", func(t *testing.T) {
		var gotPayload apis.SearchRequest
		client := &apis.Client{
			Endpoint: "https://api.deepseek.com/chat/completions",
			APIKey:   "sk-live",
			HTTP: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
						t.Fatalf("解码请求体失败: %v", err)
					}
					return jsonResponse(http.StatusOK, `{
						"results": [
							{
								"rerank_score": 2.85,
								"title": "DeepSeek Search Docs",
								"url": "https://api-docs.deepseek.com/zh-cn/guides/search/",
								"snippet": "短摘要不应覆盖长摘要",
								"summary": "面向大模型的长摘要内容",
								"date": "2026-05-01"
							},
							{
								"rerank_score": 1.6,
								"title": "Fallback Snippet Page",
								"url": "https://example.com/fallback",
								"snippet": "仅有短摘要时的回退展示",
								"summary": ""
							}
						]
					}`), nil
				}),
			},
		}

		reg := NewRegistry(WebSearch{Client: func() *apis.Client { return client }})
		argsJSON, _ := json.Marshal(map[string]any{
			"query":        "DeepSeek Search API site:api-docs.deepseek.com",
			"ori_question": strings.Repeat("问", 150),
		})
		call := apis.ToolCall{
			ID:       "s-ok",
			Function: apis.ToolCallFunction{Name: "web_search", Args: string(argsJSON)},
		}

		got, err := reg.Run(t.Context(), t.TempDir(), call, neverConfirm)
		if err != nil {
			t.Fatalf("预期无程序错误，实际: %v", err)
		}
		if gotPayload.Count != webSearchDefaultCount || gotPayload.NeedContent {
			t.Fatalf("默认请求参数异常: %+v", gotPayload)
		}
		if len([]rune(gotPayload.OriginalQuestion)) != webSearchMaxQueryRunes {
			t.Fatalf("ori_question 长度 = %d，预期截断到 %d", len([]rune(gotPayload.OriginalQuestion)), webSearchMaxQueryRunes)
		}
		for _, want := range []string{
			"共找到 2 条结果",
			"1. DeepSeek Search Docs",
			"URL: https://api-docs.deepseek.com/zh-cn/guides/search/",
			"评分: 2.85 | 日期: 2026-05-01",
			"摘要: 面向大模型的长摘要内容",
			"2. Fallback Snippet Page",
			"摘要: 仅有短摘要时的回退展示",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("输出缺少 %q，完整输出:\n%s", want, got)
			}
		}
		if strings.Contains(got, "短摘要不应覆盖长摘要") {
			t.Fatalf("有 summary 时不应输出 snippet:\n%s", got)
		}
	})

	t.Run("空结果与超长摘要截断", func(t *testing.T) {
		client := &apis.Client{
			APIKey: "sk-test",
			HTTP: &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return jsonResponse(http.StatusOK, `{"results":[]}`), nil
				}),
			},
		}
		reg := NewRegistry(WebSearch{Client: func() *apis.Client { return client }})
		call := apis.ToolCall{ID: "s-empty", Function: apis.ToolCallFunction{Name: "web_search", Args: `{"query":"nothing-found","count":3}`}}
		got, err := reg.Run(t.Context(), t.TempDir(), call, neverConfirm)
		if err != nil || !strings.Contains(got, "未找到") {
			t.Fatalf("空结果输出 = %q, err = %v", got, err)
		}

		huge := formatSearchResults("big", []apis.SearchResult{{
			Title:   "Huge",
			URL:     "https://example.com",
			Summary: strings.Repeat("长", webSearchMaxOutputRunes+500),
		}})
		if !strings.Contains(huge, "已截断部分内容") {
			t.Fatal("超长搜索结果应触发截断并附提示")
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
		reg := NewRegistry(WebSearch{Client: func() *apis.Client { return errClient }})
		call := apis.ToolCall{ID: "s-429", Function: apis.ToolCallFunction{Name: "web_search", Args: `{"query":"go"}`}}
		got, err := reg.Run(t.Context(), t.TempDir(), call, neverConfirm)
		if err != nil || !strings.Contains(got, "web_search 失败") || !strings.Contains(got, "HTTP 429") {
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
		cancelReg := NewRegistry(WebSearch{Client: func() *apis.Client { return cancelClient }})
		_, err = cancelReg.Run(ctx, t.TempDir(), call, neverConfirm)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消时返回 err = %v，预期 context.Canceled", err)
		}
	})
}
