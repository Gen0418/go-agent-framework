package gui

import (
	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

type scrollKey int64

type chatProps struct {
	session         storage.Session
	model           string
	state           *sessionState
	listState       *ui.ListState
	md              *markdown.Options
	onExpandSidebar func() // 非空表示侧边栏已收起，顶栏展示展开按钮
	thumbForPath    func(path string) *ui.Bitmap
	onOpenPath      func(path string)
	onDropFiles     func(paths []string)
	composer        composerProps
}

// 聊天主区组件
// 包含顶部标题栏、可关闭错误横幅、自动跟底消息列表/空态引导、工具确认卡与底部输入区，并支持拖入附件
func renderChat(c *ui.Context, props chatProps) {
	state := props.state
	if state == nil {
		return
	}
	t := c.Theme()

	root := ui.Column(c).Grow(1).MinWidth(0).FillHeight().Background(t.Background)
	if files := root.DroppedFiles(); len(files) > 0 && !state.running && props.onDropFiles != nil {
		props.onDropFiles(files)
	}
	dragOver := !state.running && root.FileDragOver()

	root.Children(func() {
		renderChatHeader(c, props)

		if state.err != nil {
			dangerBanner(c, state.err.Error(), func() {
				state.err = nil
			})
		}

		renderMessageList(c, props)

		if state.confirm != nil {
			ui.Box(c.Key("confirm")).Shrink(0).Padding(0, 20, 8, 20).Children(func() {
				renderConfirmCard(c, state.confirm, state.resolveConfirm)
			})
		}

		composer := props.composer
		composer.sessionID = props.session.ID
		composer.model = props.model
		composer.state = state
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
		if props.onExpandSidebar != nil && iconButton(c, "»", "展开侧边栏").Clicked() {
			props.onExpandSidebar()
		}
		ui.Text(c, props.session.Title).FontSize(14).FontWeight(600).SingleLine().Grow(1).MinWidth(0)
		if props.state.running {
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
	state := props.state

	ui.Box(c.Key("messages")).Grow(1).MinHeight(0).FillWidth().Children(func() {
		if len(state.messages) == 0 {
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

		ui.List(c.Key(scrollKey(props.session.ID)), list, len(state.messages), func(i int) {
			msg := state.messages[i]
			chatColumn(c).Children(func() {
				renderMessage(c, msg, messageProps{
					streaming: state.running && msg == state.curReply,
					md:        props.md,
					onToggleFold: func() {
						if list != nil {
							list.HoldPlace()
						}
					},
					thumbForPath: props.thumbForPath,
					onOpenPath:   props.onOpenPath,
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
