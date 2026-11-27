package apis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestClientFetchSuccess(t *testing.T) {
	var gotMethod, gotURL, gotAuth, gotAccept string
	var gotPayload FetchRequest

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
					"title": "Your First API Call | DeepSeek API Docs",
					"content": "# Your First API Call\n\nHello Markdown"
				}`), nil
			}),
		},
	}

	resp, err := client.Fetch(t.Context(), FetchRequest{
		URL: "https://api-docs.deepseek.com/",
	})
	if err != nil {
		t.Fatalf("Fetch 返回错误: %v", err)
	}
	if gotMethod != http.MethodPost || gotURL != "https://proxy.example.com/ds/v1/tools/fetch" {
		t.Fatalf("请求目标异常: method=%q url=%q", gotMethod, gotURL)
	}
	if gotAuth != "Bearer sk-live" || gotAccept != "application/json" {
		t.Fatalf("请求头异常: auth=%q accept=%q", gotAuth, gotAccept)
	}
	if gotPayload.URL != "https://api-docs.deepseek.com/" {
		t.Fatalf("请求体异常: %+v", gotPayload)
	}
	if resp.Title != "Your First API Call | DeepSeek API Docs" || !strings.Contains(resp.Content, "# Your First API Call") {
		t.Fatalf("响应解析异常: %+v", resp)
	}
}

func TestClientFetchErrorsAndCancel(t *testing.T) {
	t.Run("HTTP 429 触发限流错误", func(t *testing.T) {
		client := &Client{
			APIKey: "sk-test",
			HTTP: &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return jsonResp(http.StatusTooManyRequests, `domain qps exceeded`), nil
				}),
			},
		}
		_, err := client.Fetch(t.Context(), FetchRequest{URL: "https://example.com"})
		if err == nil || !strings.Contains(err.Error(), "触发限流（HTTP 429）") {
			t.Fatalf("429 错误 = %v", err)
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
		_, err := client.Fetch(ctx, FetchRequest{URL: "https://example.com"})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误 = %v，预期 context.Canceled", err)
		}
	})
}
