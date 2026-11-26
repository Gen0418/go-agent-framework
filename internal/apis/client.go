package apis

import (
	"net/http"
	"time"
)

// 模型接口客户端（HTTP 为空时复用默认超时客户端）
type Client struct {
	Endpoint string
	APIKey   string
	Model    string
	HTTP     *http.Client
}

var defaultHTTPClient = &http.Client{
	Transport: &http.Transport{
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTPClient
}

func (c *Client) setStreamHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
}

func (c *Client) newChatRequest(msgs []Message, opts StreamOptions) chatRequest {
	thinkingType := thinkingDisabled
	effort := ""
	if opts.Thinking {
		thinkingType = thinkingEnabled
		effort = opts.ReasoningEffort
	}

	return chatRequest{
		Model:           c.Model,
		Messages:        msgs,
		Stream:          true,
		Thinking:        &enableThinking{Type: thinkingType},
		ReasoningEffort: effort,
		Tools:           opts.Tools,
	}
}
