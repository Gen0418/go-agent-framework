package gui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

type chatProps struct {
	sessionID int64
	title     string
	err       error
	messages  []*chatMessage
	confirm   *confirmToolCall
	onConfirm func(allow bool)
	composer  composerProps
}

// 聊天主区组件
// 包含标题栏、错误提示、消息滚动区、工具确认卡与底部输入区
func renderChat(c *ui.Context, props chatProps) {
	ui.Column(c).Grow(1).FillHeight().Children(func() {
		ui.Text(c, props.title) // 标题栏

		if props.err != nil {
			ui.Text(c, props.err.Error())
		}

		ui.Scroll(c.Key(scrollKey(props.sessionID))).Grow(1).Children(func() {
			for _, msg := range props.messages {
				renderMessage(c, msg)
			}
		})

		if props.confirm != nil {
			renderConfirmCard(c, props.confirm, props.onConfirm)
		}

		renderComposer(c, props.composer)
	})
}

// 工具执行确认卡片
// 展示目标目录、待执行命令及批准/拒绝按钮
func renderConfirmCard(c *ui.Context, confirm *confirmToolCall, onDecide func(allow bool)) {
	ui.Column(c).Children(func() {
		ui.Text(c, fmt.Sprintf("请求执行命令 (目录: %s)", confirm.dir))
		ui.Text(c, confirm.command)
		ui.Row(c).Children(func() {
			if ui.Button(c, "批准").Clicked() && onDecide != nil {
				onDecide(true)
			}
			if ui.Button(c, "拒绝").Clicked() && onDecide != nil {
				onDecide(false)
			}
		})
	})
}
