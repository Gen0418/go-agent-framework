package apis

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestDeriveBaseURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空地址回退官方域名", "", "https://api.deepseek.com"},
		{"官方默认对话地址", "https://api.deepseek.com/chat/completions", "https://api.deepseek.com"},
		{"带 v1 前缀的对话地址", "https://api.deepseek.com/v1/chat/completions/", "https://api.deepseek.com"},
		{"自定义代理带子路径", "https://proxy.example.com/ds/v1/chat/completions", "https://proxy.example.com/ds"},
		{"自定义代理不带 v1", "https://proxy.example.com/ds/chat/completions?foo=1", "https://proxy.example.com/ds"},
		{"仅域名保持原样", "http://127.0.0.1:8080/", "http://127.0.0.1:8080"},
		{"非法协议回退官方域名", "ftp://example.com/chat/completions", "https://api.deepseek.com"},
		{"无效地址回退官方域名", "not-a-url", "https://api.deepseek.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveBaseURL(tc.in); got != tc.want {
				t.Fatalf("deriveBaseURL(%q) = %q，预期 %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestClientSearchSuccess(t *testing.T) {
	var gotMethod, gotURL, gotAuth, gotAccept string
	var gotPayload SearchRequest

	client := &Client{
		Endpoint: "https://proxy.example.com/ds/v1/chat/completions",
		APIKey:   "sk-live",
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotMethod = r.Method
				gotURL = r.URL.String()
				gotAuth = r.Header.Get("Authorization")
				gotAccept = r.Header.Get("Accept")
				if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
					t.Fatalf("解码请求体失败: %v", err)
				}
				return jsonResp(http.StatusOK, `{
					"results": [
						{
							"rerank_score": 2.85,
							"title": "DeepSeek Search",
							"url": "https://api-docs.deepseek.com/zh-cn/guides/search/",
							"snippet": "短摘要",
							"summary": "长摘要",
							"date": "2026-05-01"
						}
					]
				}`), nil
			}),
		},
	}

	resp, err := client.Search(t.Context(), SearchRequest{
		Query:            "DeepSeek Search API",
		Count:            5,
		OriginalQuestion: "怎么接 DeepSeek 搜索",
		NeedContent:      false,
	})
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if gotMethod != http.MethodPost || gotURL != "https://proxy.example.com/ds/v1/tools/search" {
		t.Fatalf("请求目标异常: method=%q url=%q", gotMethod, gotURL)
	}
	if gotAuth != "Bearer sk-live" || gotAccept != "application/json" {
		t.Fatalf("请求头异常: auth=%q accept=%q", gotAuth, gotAccept)
	}
	if gotPayload.Query != "DeepSeek Search API" || gotPayload.Count != 5 || gotPayload.NeedContent {
		t.Fatalf("请求体异常: %+v", gotPayload)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "DeepSeek Search" || resp.Results[0].Summary != "长摘要" {
		t.Fatalf("响应解析异常: %+v", resp)
	}
}

func TestClientSearchErrorsTimeoutAndCancel(t *testing.T) {
	t.Run("空客户端或缺少 APIKey 报错", func(t *testing.T) {
		var nilClient *Client
		if _, err := nilClient.Search(t.Context(), SearchRequest{Query: "go"}); err == nil || !strings.Contains(err.Error(), "未配置 APIKey") {
			t.Fatalf("nil client 返回 err = %v", err)
		}
		emptyKey := &Client{APIKey: "  "}
		if _, err := emptyKey.Search(t.Context(), SearchRequest{Query: "go"}); err == nil || !strings.Contains(err.Error(), "未配置 APIKey") {
			t.Fatalf("空 APIKey 返回 err = %v", err)
		}
	})

	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"401认证失败", http.StatusUnauthorized, `{"error":"invalid api key"}`, "认证失败（HTTP 401）"},
		{"429限流", http.StatusTooManyRequests, `{"error":"rate limit"}`, "触发限流（HTTP 429）"},
		{"500服务端异常", http.StatusInternalServerError, `upstream error`, "请求失败（HTTP 500）"},
		{"响应非合法JSON", http.StatusOK, `not-json`, "解析响应失败"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{
				APIKey: "sk-test",
				HTTP: &http.Client{
					Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						return jsonResp(tc.status, tc.body), nil
					}),
				},
			}
			_, err := client.Search(t.Context(), SearchRequest{Query: "go"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v，预期包含 %q", err, tc.want)
			}
		})
	}

	t.Run("内部超时返回包装 DeadlineExceeded 的错误", func(t *testing.T) {
		client := &Client{
			APIKey: "sk-test",
			HTTP: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}),
			},
		}
		var out SearchResponse
		err := client.postToolJSON(t.Context(), searchToolPath, 30*time.Millisecond, SearchRequest{Query: "go"}, &out)
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "请求超时") {
			t.Fatalf("超时错误 = %v，预期包装 context.DeadlineExceeded", err)
		}
	})

	t.Run("父上下文取消直接传播 context.Canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		client := &Client{
			APIKey: "sk-test",
			HTTP: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					cancel()
					<-r.Context().Done()
					return nil, r.Context().Err()
				}),
			},
		}
		_, err := client.Search(ctx, SearchRequest{Query: "go"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误 = %v，预期 context.Canceled", err)
		}
	})
}
