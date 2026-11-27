package gui

import (
	"go-agent-framework/internal/apis"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

type chatProps struct {
	sessionID        int64
	title            string
	model            string
	running          bool
	sidebarCollapsed bool
	onExpandSidebar  func()
	err              error
	onDismissErr     func()
	messages         []*chatMessage
	curReply         *chatMessage
	listState        *ui.ListState
	md               *markdown.Options
	thumbForImage    func(apis.Image) *ui.Bitmap
	onOpenImage      func(apis.Image)
	onDropFiles      func(paths []string)
	confirm          *confirmToolCall
	onConfirm        func(allow bool)
	composer         composerProps
}

// 聊天主区组件
// 包含顶部标题栏、可关闭错误横幅、自动跟底消息列表/空态引导、工具确认卡与底部输入区，并支持拖入图片文件
func renderChat(c *ui.Context, props chatProps) {
	t := c.Theme()

	root := ui.Column(c).Grow(1).MinWidth(0).FillHeight().Background(t.Background)
	if files := root.DroppedFiles(); len(files) > 0 && !props.running && props.onDropFiles != nil {
		props.onDropFiles(files)
	}
	dragOver := !props.running && root.FileDragOver()

	root.Children(func() {
		renderChatHeader(c, props)

		if props.err != nil {
			dangerBanner(c, props.err.Error(), props.onDismissErr)
		}

		renderMessageList(c, props)

		if props.confirm != nil {
			ui.Box(c.Key("confirm")).Shrink(0).Padding(0, 20, 8, 20).Children(func() {
				renderConfirmCard(c, props.confirm, props.onConfirm)
			})
		}

		composer := props.composer
		composer.dragOver = dragOver
		ui.Box(c.Key("composer")).Shrink(0).Padding(8, 20, 16, 20).Children(func() {
			chatColumn(c).Children(func() {
				renderComposer(c, composer)
			})
		})
	})
}

func renderChatHeader(c *ui.Context, props chatProps) {
	t := c.Theme()

	topBar(c, 20, func() {
		if props.sidebarCollapsed && iconButton(c, "»", "展开侧边栏").Clicked() && props.onExpandSidebar != nil {
			props.onExpandSidebar()
		}
		ui.Text(c, props.title).FontSize(14).FontWeight(600).SingleLine().Grow(1).MinWidth(0)
		if props.running {
			statusSpinner(c, "生成中")
		}
		if props.model != "" {
			pill(c, props.model, t.TextMuted, t.Surface)
		}
	})
}

// 消息滚动列表区域
// 使用 ui.List 跟随流式输出末尾，用户上滑时自动暂停并显示回到底部按钮
func renderMessageList(c *ui.Context, props chatProps) {
	t := c.Theme()

	ui.Box(c.Key("messages")).Grow(1).MinHeight(0).FillWidth().Children(func() {
		if len(props.messages) == 0 {
			ui.Column(c).Fill().Children(func() {
				ui.Column(c).Margin(ui.Auto).AlignItems(ui.Center).Gap(8).Padding(24).Children(func() {
					ui.Text(c, "SAISON").FontSize(22).FontWeight(600).LetterSpacing(1.5)
					ui.Text(c, "输入消息开始新对话，或从左侧切换已有会话").FontSize(13).TextColor(t.TextMuted)
				})
			})
			return
		}

		list := props.listState
		if list != nil {
			list.Key = func(i int) any { return i }
			list.FollowEnd = true
			list.FollowAnimation = &ui.Smooth
		}

		ui.List(c.Key(scrollKey(props.sessionID)), list, len(props.messages), func(i int) {
			msg := props.messages[i]
			chatColumn(c).Children(func() {
				renderMessage(c, msg, messageProps{
					streaming: props.running && msg == props.curReply,
					md:        props.md,
					onToggleFold: func() {
						if list != nil {
							list.HoldPlace()
						}
					},
					thumbForImage: props.thumbForImage,
					onOpenImage:   props.onOpenImage,
				})
			})
		}).Fill().Padding(16, 20).Gap(16)

		if list != nil && !list.AtEnd() && scrollToEndButton(c).Clicked() {
			list.ScrollToEnd()
		}
	})
}

// 工具执行确认卡片
// 居中对齐聊天列宽，展示目录、可滚动命令区与拒绝/批准操作
func renderConfirmCard(c *ui.Context, confirm *confirmToolCall, onDecide func(allow bool)) {
	chatColumn(c).Children(func() {
		confirmCard(c, confirm, onDecide)
	})
}
