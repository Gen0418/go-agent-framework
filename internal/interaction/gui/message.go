package gui

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

type messageProps struct {
	streaming    bool
	md           *markdown.Options
	onToggleFold func()
}

// 单条消息组件
// 按角色渲染用户气泡、可折叠思考卡片、流式/静态 Markdown 正文与等宽工具卡片
func renderMessage(c *ui.Context, msg *chatMessage, props messageProps) {
	switch msg.Role {
	case "user":
		userBubble(c, msg.Content)
	case "assistant":
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			if msg.ReasoningContent != "" {
				label := "思考过程"
				if props.streaming && msg.Content == "" && len(msg.ToolCalls) == 0 {
					label = "思考中…"
				}
				if reasoningBox(c, label, &msg.reasoningOpen, msg.ReasoningContent) && props.onToggleFold != nil {
					props.onToggleFold()
				}
			}
			if msg.Content != "" {
				if props.streaming && len(msg.ToolCalls) == 0 {
					markdown.Stream(c, msg.Content, props.md)
				} else {
					markdown.View(c, msg.Content, props.md)
				}
			}
			for _, call := range msg.ToolCalls {
				toolCallCard(c, call.Function.Name, strings.TrimSpace(call.Function.Args))
			}
		})
	case "tool":
		toolResultCard(c, strings.TrimRight(msg.Content, "\n"))
	default:
		ui.Text(c, msg.Content).FontSize(14).LineHeight(1.5).Selectable()
	}
}
