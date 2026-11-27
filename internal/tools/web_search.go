package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"go-agent-framework/internal/apis"
)

const (
	webSearchDefaultCount   = 5
	webSearchMinCount       = 1
	webSearchMaxCount       = 50
	webSearchMaxQueryRunes  = 128
	webSearchMaxOutputRunes = 24000
)

// WebSearch 调用 DeepSeek Search API 联网检索公开网页并返回精简摘要
// 属于只读外部查询，执行时不触发本地命令确认卡片
type WebSearch struct {
	Client func() *apis.Client // 动态获取最新 API 客户端，支持运行期热更新配置
}

type webSearchArgs struct {
	Query            string `json:"query"`
	Count            *int   `json:"count,omitempty"`
	OriginalQuestion string `json:"ori_question,omitempty"`
}

func (WebSearch) Definition() apis.Tool {
	return apis.Tool{
		Type: "function",
		Function: apis.FunctionDef{
			Name:        "web_search",
			Description: "使用 DeepSeek 搜索引擎联网检索公开网页，返回相关结果的标题、链接、相关性评分与摘要。支持 site:、-site:、精确引号短语、after:/before: 等检索语法。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "搜索关键词（1-128 个字符），支持 site:、-site:、精确引号短语等语法。",
					},
					"count": map[string]any{
						"type":        "integer",
						"description": "返回结果条数（1-50，默认 5；通常 5-10 条即可）。",
					},
					"ori_question": map[string]any{
						"type":        "string",
						"description": "可选，用户触发搜索的原始问题（最多 128 个字符），有助于提升查询理解。",
					},
				},
				"required":             []string{"query"},
				"additionalProperties": false,
			},
		},
	}
}

func (w WebSearch) Run(ctx context.Context, _ string, rawArgs string, _ ConfirmFunc) (string, error) {
	var args webSearchArgs
	decoder := json.NewDecoder(strings.NewReader(rawArgs))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return fmt.Sprintf("web_search 参数解析错误: %v", err), nil
	}

	query := strings.TrimSpace(args.Query)
	if query == "" {
		return "web_search 查询词为空", nil
	}
	if utf8.RuneCountInString(query) > webSearchMaxQueryRunes {
		return fmt.Sprintf("web_search 查询词长度不能超过 %d 个字符", webSearchMaxQueryRunes), nil
	}

	count := webSearchDefaultCount
	if args.Count != nil {
		if *args.Count < webSearchMinCount || *args.Count > webSearchMaxCount {
			return fmt.Sprintf("web_search 返回条数 count 必须在 %d 到 %d 之间", webSearchMinCount, webSearchMaxCount), nil
		}
		count = *args.Count
	}

	originalQuestion := strings.TrimSpace(args.OriginalQuestion)
	if originalQuestion != "" {
		originalQuestion, _ = truncateRunes(originalQuestion, webSearchMaxQueryRunes)
	}

	var client *apis.Client
	if w.Client != nil {
		client = w.Client()
	}
	if client == nil {
		return "web_search 未配置 API 客户端", nil
	}

	resp, err := client.Search(ctx, apis.SearchRequest{
		Query:            query,
		Count:            count,
		OriginalQuestion: originalQuestion,
		NeedContent:      false,
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return fmt.Sprintf("web_search 失败: %v", err), nil
	}

	return formatSearchResults(query, resp.Results), nil
}

func formatSearchResults(query string, results []apis.SearchResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("未找到与 %q 相关的搜索结果。", query)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "共找到 %d 条结果（query: %q）：\n", len(results), query)
	for i, item := range results {
		title := strings.TrimSpace(item.Title)
		link := strings.TrimSpace(item.URL)
		if title == "" {
			title = link
		}
		if title == "" {
			title = "（无标题）"
		}

		fmt.Fprintf(&b, "\n%d. %s\n", i+1, title)
		if link != "" {
			fmt.Fprintf(&b, "   URL: %s\n", link)
		}

		var meta []string
		if item.RerankScore > 0 {
			meta = append(meta, fmt.Sprintf("评分: %.2f", item.RerankScore))
		}
		if date := strings.TrimSpace(item.Date); date != "" {
			meta = append(meta, "日期: "+date)
		}
		if len(meta) > 0 {
			fmt.Fprintf(&b, "   %s\n", strings.Join(meta, " | "))
		}

		abstract := strings.TrimSpace(item.Summary)
		if abstract == "" {
			abstract = strings.TrimSpace(item.Snippet)
		}
		if abstract != "" {
			abstract = strings.ReplaceAll(abstract, "\r\n", "\n")
			fmt.Fprintf(&b, "   摘要: %s\n", abstract)
		}
	}

	out := strings.TrimRight(b.String(), "\n")
	if clipped, ok := truncateRunes(out, webSearchMaxOutputRunes); ok {
		return strings.TrimRight(clipped, "\n") + "\n\n…（搜索结果过长，已截断部分内容）"
	}
	return out
}

// 按 Unicode 字符数截断字符串，避免破坏多字节 UTF-8 字符
func truncateRunes(s string, maxRunes int) (string, bool) {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s, false
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i], true
		}
		count++
	}
	return s, false
}
