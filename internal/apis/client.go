package apis

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL   = "https://api.deepseek.com"
	thinkingEnabled  = "enabled"
	thinkingDisabled = "disabled"
)

type enableThinking struct {
	Type string `json:"type"` // "enabled" / "disabled"
}

// Chat Completions 协议层请求体
type chatRequest struct {
	Model           string           `json:"model"`
	Messages        []requestMessage `json:"messages"`
	Stream          bool             `json:"stream"`
	Thinking        *enableThinking  `json:"thinking,omitzero"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	Tools           []Tool           `json:"tools,omitempty"`
}

// 模型接口客户端（HTTP 为空时复用默认连接池客户端）
type Client struct {
	Endpoint string
	APIKey   string
	Model    string
	HTTP     *http.Client
}

var defaultHTTPClient = newDefaultHTTPClient()

func newDefaultHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	// 文档同步解析上限可达 180s，适当放宽响应头等待时间；各接口仍由各自的 context.WithTimeout 精确控时
	transport.ResponseHeaderTimeout = 200 * time.Second
	return &http.Client{Transport: transport}
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
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
}

func (c *Client) setJSONHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
}

// 从对话接口 Endpoint 推算同源工具 API 路径
// 自动剥离 /chat/completions 与 /v1/chat/completions 后缀，空或非法地址回退官方域名
func (c *Client) toolEndpoint(apiPath string) string {
	return deriveBaseURL(c.Endpoint) + apiPath
}

func deriveBaseURL(endpoint string) string {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return defaultBaseURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return defaultBaseURL
	}

	path := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(path, "/v1/chat/completions"):
		path = strings.TrimSuffix(path, "/v1/chat/completions")
	case strings.HasSuffix(path, "/chat/completions"):
		path = strings.TrimSuffix(path, "/chat/completions")
	}
	u.Path = strings.TrimRight(path, "/")
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func (c *Client) newChatRequest(msgs []Message, opts StreamOptions) (chatRequest, error) {
	thinkingType := thinkingDisabled
	effort := ""
	if opts.Thinking {
		thinkingType = thinkingEnabled
		effort = opts.ReasoningEffort
	}

	reqMsgs, err := encodeRequestMessages(msgs)
	if err != nil {
		return chatRequest{}, err
	}

	return chatRequest{
		Model:           c.Model,
		Messages:        reqMsgs,
		Stream:          true,
		Thinking:        &enableThinking{Type: thinkingType},
		ReasoningEffort: effort,
		Tools:           opts.Tools,
	}, nil
}
