package gui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// 单条消息组件
// 按角色渲染用户消息、可折叠思考块、回复正文、工具调用与工具结果
func renderMessage(c *ui.Context, msg *chatMessage) {
	ui.Column(c.Key(msg)).Children(func() {
		switch msg.Role {
		case "user":
			ui.Text(c, fmt.Sprintf("[用户] %s\n", msg.Content))
		case "assistant":
			if msg.ReasoningContent != "" { // 思考内容
				ui.Collapsible(
					c,
					"思考",
					&msg.reasoningOpen,
					func() {
						ui.Text(c, fmt.Sprintf("[思考内容] %s\n", msg.ReasoningContent))
					},
				)
			}
			if msg.Content != "" { // 正文内容
				ui.Text(c, fmt.Sprintf("[回复] %s\n", msg.Content))
			}
			for _, call := range msg.ToolCalls { // 工具调用
				ui.Text(c, fmt.Sprintf("[工具调用] %s %s\n", call.Function.Name, call.Function.Args))
			}
		case "tool": // 工具结果
			ui.Text(c, fmt.Sprintf("[工具结果] %s\n", msg.Content))
		default:
			ui.Text(c, fmt.Sprintf("%s\n", msg.Content))
		}
	})
}
