package apis

import (
	"context"
	"time"
)

const (
	fetchToolPath    = "/v1/tools/fetch"
	fetchToolTimeout = 60 * time.Second
)

// FetchRequest 表示 DeepSeek Fetch API 的请求参数
type FetchRequest struct {
	URL string `json:"url"`
}

// FetchResponse 表示 DeepSeek Fetch API 的响应体
type FetchResponse struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

// Fetch 调用 DeepSeek Fetch API 抓取单个网页并返回 Markdown 正文
func (c *Client) Fetch(ctx context.Context, req FetchRequest) (FetchResponse, error) {
	var resp FetchResponse
	if err := c.postToolJSON(ctx, fetchToolPath, fetchToolTimeout, req, &resp); err != nil {
		return FetchResponse{}, err
	}
	return resp, nil
}
