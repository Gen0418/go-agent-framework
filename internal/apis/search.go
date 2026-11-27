package apis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	searchToolPath       = "/v1/tools/search"
	defaultToolTimeout   = 30 * time.Second
	maxToolErrorBytes    = 512
	maxToolResponseBytes = 4 << 20 // 4 MiB，避免异常超大响应占用过多内存
)

// SearchRequest 表示 DeepSeek Search API 的请求参数
type SearchRequest struct {
	Query            string `json:"query"`
	Count            int    `json:"count,omitempty"`
	OriginalQuestion string `json:"ori_question,omitempty"`
	NeedContent      bool   `json:"need_content"`
}

// SearchResponse 表示 DeepSeek Search API 的响应体
type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

// SearchResult 表示单条网页搜索结果
type SearchResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	Content     string  `json:"content,omitempty"`
	Date        string  `json:"date,omitempty"`
}

// Search 调用 DeepSeek Search API 检索公开网页
func (c *Client) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	var resp SearchResponse
	if err := c.postToolJSON(ctx, searchToolPath, defaultToolTimeout, req, &resp); err != nil {
		return SearchResponse{}, err
	}
	return resp, nil
}

// 发送 JSON 格式的 DeepSeek 工具 API 请求并解码响应
func (c *Client) postToolJSON(ctx context.Context, apiPath string, timeout time.Duration, payload, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || strings.TrimSpace(c.APIKey) == "" {
		return errors.New("未配置 APIKey，请先在设置中填写")
	}

	reqBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("构造请求失败: %w", err)
	}

	if timeout <= 0 {
		timeout = defaultToolTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(execCtx, http.MethodPost, c.toolEndpoint(apiPath), bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	c.setJSONHeaders(req)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("请求超时（超过 %s）: %w", timeout, execCtx.Err())
		}
		return fmt.Errorf("网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return formatToolHTTPError(resp.StatusCode, resp.Body)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxToolResponseBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("读取响应超时（超过 %s）: %w", timeout, execCtx.Err())
		}
		return fmt.Errorf("读取响应失败: %w", err)
	}
	if len(body) > maxToolResponseBytes {
		return fmt.Errorf("响应体过大（超过 %d 字节）", maxToolResponseBytes)
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	return nil
}

func formatToolHTTPError(status int, body io.Reader) error {
	raw, _ := io.ReadAll(io.LimitReader(body, maxToolErrorBytes))
	detail := strings.TrimSpace(string(raw))
	switch status {
	case http.StatusUnauthorized:
		if detail == "" {
			return errors.New("认证失败（HTTP 401），请检查 APIKey 是否有效")
		}
		return fmt.Errorf("认证失败（HTTP 401），请检查 APIKey 是否有效: %s", detail)
	case http.StatusTooManyRequests:
		if detail == "" {
			return errors.New("触发限流（HTTP 429），请稍后重试")
		}
		return fmt.Errorf("触发限流（HTTP 429），请稍后重试: %s", detail)
	default:
		if detail == "" {
			return fmt.Errorf("请求失败（HTTP %d）", status)
		}
		return fmt.Errorf("请求失败（HTTP %d）: %s", status, detail)
	}
}
