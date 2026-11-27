package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"go-agent-framework/internal/apis"
)

const (
	webFetchMaxURLRunes     = 8192
	webFetchMaxContentRunes = 16000
)

// WebFetch 调用 DeepSeek Fetch API 抓取单个公开网页并转为 Markdown 正文
// 属于只读外部查询，执行时不触发本地命令确认卡片
type WebFetch struct {
	Client func() *apis.Client // 动态获取最新 API 客户端，支持运行期热更新配置
}

type webFetchArgs struct {
	URL string `json:"url"`
}

func (WebFetch) Definition() apis.Tool {
	return apis.Tool{
		Type: "function",
		Function: apis.FunctionDef{
			Name:        "web_fetch",
			Description: "抓取单个公开网页的正文并转换为 Markdown 格式返回。通常配合 web_search 使用，在需要阅读具体页面全文时调用。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "目标网页的完整 http:// 或 https:// 链接（不超过 8192 个字符，且不能包含用户名或密码）。",
					},
				},
				"required":             []string{"url"},
				"additionalProperties": false,
			},
		},
	}
}

func (w WebFetch) Run(ctx context.Context, _ string, rawArgs string, _ ConfirmFunc) (string, error) {
	var args webFetchArgs
	decoder := json.NewDecoder(strings.NewReader(rawArgs))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return fmt.Sprintf("web_fetch 参数解析错误: %v", err), nil
	}

	targetURL, invalidMsg := validateFetchURL(args.URL)
	if invalidMsg != "" {
		return invalidMsg, nil
	}

	var client *apis.Client
	if w.Client != nil {
		client = w.Client()
	}
	if client == nil {
		return "web_fetch 未配置 API 客户端", nil
	}

	resp, err := client.Fetch(ctx, apis.FetchRequest{URL: targetURL})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return fmt.Sprintf("web_fetch 失败: %v", err), nil
	}

	return formatFetchResult(targetURL, resp), nil
}

func validateFetchURL(raw string) (string, string) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", "web_fetch 链接为空"
	}
	if utf8.RuneCountInString(target) > webFetchMaxURLRunes {
		return "", fmt.Sprintf("web_fetch 链接长度不能超过 %d 个字符", webFetchMaxURLRunes)
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "web_fetch 链接必须是合法的 http:// 或 https:// 地址"
	}
	if u.User != nil {
		return "", "web_fetch 链接不能包含用户名或密码"
	}
	return target, ""
}

func formatFetchResult(targetURL string, resp apis.FetchResponse) string {
	var b strings.Builder
	if title := strings.TrimSpace(resp.Title); title != "" {
		fmt.Fprintf(&b, "标题: %s\n", title)
	}
	fmt.Fprintf(&b, "URL: %s\n", targetURL)

	content := strings.TrimSpace(strings.ReplaceAll(resp.Content, "\r\n", "\n"))
	if content == "" {
		b.WriteString("\n（未能提取到网页正文内容）")
		return b.String()
	}

	totalRunes := utf8.RuneCountInString(content)
	if clipped, ok := truncateRunes(content, webFetchMaxContentRunes); ok {
		fmt.Fprintf(&b, "\n%s\n\n…（正文共 %d 字符，已截断至前 %d 字符）",
			strings.TrimRight(clipped, "\n"), totalRunes, webFetchMaxContentRunes)
		return b.String()
	}

	fmt.Fprintf(&b, "\n%s", content)
	return b.String()
}
