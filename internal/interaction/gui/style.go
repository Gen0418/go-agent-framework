package gui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"go-agent-framework/internal/apis"

	"github.com/egoist/mygo/ui"
)

const (
	sidebarWidth        = 240
	headerHeight        = 48
	chatMaxWidth        = 760
	toolFoldMaxLines    = 5
	toolFoldMaxRunes    = 240
	toolScrollHeight    = 220
	confirmCompactMaxH  = 96
	confirmExpandedMaxH = 180
	bannerMaxHeight     = 72
	dialogErrorMaxH     = 80
)

// 应用极简黑白灰主题
// 跟随系统浅色/深色外观与字体缩放，统一无彩色阶及主按钮悬停反馈
func applyTheme(c *ui.Context) {
	t := *c.Theme()
	t.Radius = 8
	if t.Dark {
		t.Background = ui.Hex("#141416")
		t.Surface = ui.Hex("#1d1d20")
		t.SurfaceHover = ui.Hex("#27272b")
		t.SurfacePressed = ui.Hex("#323238")
		t.Border = ui.Hex("#2c2c31")
		t.Text = ui.Hex("#f4f4f5")
		t.TextMuted = ui.Hex("#a1a1aa")
		t.Accent = ui.Hex("#f4f4f5")
		t.AccentHover = ui.Hex("#ffffff")
		t.AccentPressed = ui.Hex("#d4d4d8")
		t.AccentText = ui.Hex("#18181b")
		t.Selection = ui.RGBA(244, 244, 245, 0.18)
		t.Focus = ui.RGBA(244, 244, 245, 0.45)
	} else {
		t.Background = ui.Hex("#ffffff")
		t.Surface = ui.Hex("#f4f4f5")
		t.SurfaceHover = ui.Hex("#e8e8ec")
		t.SurfacePressed = ui.Hex("#dcdce2")
		t.Border = ui.Hex("#e4e4e7")
		t.Text = ui.Hex("#18181b")
		t.TextMuted = ui.Hex("#71717a")
		t.Accent = ui.Hex("#18181b")
		t.AccentHover = ui.Hex("#27272a")
		t.AccentPressed = ui.Hex("#3f3f46")
		t.AccentText = ui.Hex("#ffffff")
		t.Selection = ui.RGBA(24, 24, 27, 0.14)
		t.Focus = ui.RGBA(24, 24, 27, 0.35)
	}
	c.SetTheme(&t)
}

// 侧边栏容器底板
// 固定宽度、次级灰阶背景与右侧细分割线
func sidebarPanel(c *ui.Context, fn func()) {
	t := c.Theme()
	ui.Column(c).
		Width(sidebarWidth).
		Shrink(0).
		FillHeight().
		Background(t.Surface).
		BorderWidth(0, 1, 0, 0).
		BorderColor(t.Border).
		Children(fn)
}

// 等高顶栏容器
// 统一侧边栏与聊天区顶部高度和底部分割线，保持横向对齐
func topBar(c *ui.Context, padX float32, fn func()) {
	t := c.Theme()
	ui.Row(c).
		Height(headerHeight).
		Shrink(0).
		Padding(0, padX).
		Gap(10).
		AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).
		BorderColor(t.Border).
		Children(fn)
}

// 聊天内容居中限宽列
// 大窗口下限制阅读宽度，返回列元素便于调用方补充 Gap 等属性
func chatColumn(c *ui.Context) ui.Element {
	return ui.Column(c).FillWidth().MaxWidth(chatMaxWidth).Margin(0, ui.Auto)
}

// 紧凑图标按钮
// 带悬停底色、Tooltip 与无障碍标签，适用于收起/展开/关闭等轻操作
func iconButton(c *ui.Context, icon, label string) ui.Element {
	t := c.Theme()
	btn := ui.ButtonBase(c).
		Size(28, 28).
		Center().
		Radius(6).
		Shrink(0).
		Label(label).
		Tooltip(label)
	if btn.Hovered() {
		btn.Background(t.SurfaceHover)
	}
	btn.Children(func() {
		ui.Text(c, icon).FontSize(14).TextColor(t.TextMuted)
	})
	return btn
}

// 全宽细边框按钮
// 用于侧边栏“新建会话”等次主要操作，active 为真时加重边框
func outlineButton(c *ui.Context, text, label string, active bool) ui.Element {
	t := c.Theme()
	btn := ui.ButtonBase(c).
		FillWidth().
		Padding(8, 12).
		Radius(t.Radius).
		Background(t.Background).
		Border(1, t.Border).
		Label(label)
	if btn.Hovered() {
		btn.Background(t.SurfaceHover)
	}
	if active {
		btn.Border(1, t.Text.Alpha(0.35))
	}
	btn.Children(func() {
		ui.Text(c, text).FontSize(13).FontWeight(500).SingleLine()
	})
	return btn
}

// 全宽幽灵按钮
// 默认透明底色，悬停浮现浅背景，用于侧边栏底部“设置”等低干扰入口
func ghostButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	btn := ui.ButtonBase(c).
		FillWidth().
		Padding(8, 10).
		Radius(t.Radius).
		Label(label)
	if btn.Hovered() {
		btn.Background(t.SurfaceHover)
	}
	btn.Children(func() {
		ui.Text(c, label).FontSize(13).TextColor(t.TextMuted).SingleLine()
	})
	return btn
}

// 紧凑胶囊标签
// 用于模型名、待确认等轻量状态标识
func pill(c *ui.Context, text string, fg, bg ui.Color) {
	ui.Text(c, text).
		FontSize(11).
		TextColor(fg).
		Padding(2, 8).
		Radius(999).
		Background(bg).
		Shrink(0)
}

func statusSpinner(c *ui.Context, label string) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Spinner(c).Size(12, 12).Label(label)
		ui.Text(c, label).FontSize(12).TextColor(c.Theme().TextMuted)
	})
}

// 会话级错误横幅
// 淡红底细边框容器，超长报错在限高区内部滚动，关闭按钮固定在右上角
func dangerBanner(c *ui.Context, msg string, onDismiss func()) {
	t := c.Theme()
	ui.Box(c.Key("banner")).Shrink(0).Padding(12, 20, 0, 20).Children(func() {
		ui.Row(c).
			FillWidth().
			MaxWidth(chatMaxWidth).
			Margin(0, ui.Auto).
			Padding(10, 14).
			Gap(10).
			AlignItems(ui.Start).
			Radius(t.Radius).
			Background(t.Danger.Alpha(0.08)).
			Border(1, t.Danger.Alpha(0.32)).
			Children(func() {
				ui.Scroll(c).MaxHeight(bannerMaxHeight).Grow(1).MinWidth(0).Children(func() {
					ui.Text(c, msg).FontSize(13).LineHeight(1.45).TextColor(t.Danger).Selectable()
				})
				if onDismiss != nil {
					closeBtn := ui.ButtonBase(c).
						Size(22, 22).
						Center().
						Radius(4).
						Shrink(0).
						Label("关闭错误提示")
					if closeBtn.Hovered() {
						closeBtn.Background(t.Danger.Alpha(0.14))
					}
					closeBtn.Children(func() {
						ui.Text(c, "×").FontSize(14).TextColor(t.Danger)
					})
					if closeBtn.Clicked() {
						onDismiss()
					}
				}
			})
	})
}

// 用户消息右对齐气泡
// 支持纯文本、纯图片及图文混排，缩略图点击可打开系统预览，文字支持划词复制
func userBubble(c *ui.Context, text string, images []apis.Image, thumbFn func(apis.Image) *ui.Bitmap, onOpenImage func(apis.Image)) {
	t := c.Theme()
	ui.Row(c).FillWidth().Children(func() {
		ui.Spacer(c)
		ui.Column(c).AlignItems(ui.End).Gap(6).MaxWidthPercent(78).MaxWidth(540).Children(func() {
			if len(images) > 0 {
				ui.Row(c).Wrap().Justify(ui.End).Gap(6).Children(func() {
					for i, img := range images {
						renderHistoryImageThumb(c, i, img, thumbFn, onOpenImage)
					}
				})
			}
			if text != "" {
				ui.Text(c, text).
					Selectable().
					FontSize(14).
					LineHeight(1.5).
					Padding(10, 14).
					Radius(16).
					Background(t.Surface)
			}
		})
	})
}

func renderHistoryImageThumb(c *ui.Context, idx int, img apis.Image, thumbFn func(apis.Image) *ui.Bitmap, onOpenImage func(apis.Image)) {
	t := c.Theme()
	name := strings.TrimSpace(img.Name)
	if name == "" {
		name = fmt.Sprintf("图片 %d", idx+1)
	}
	var bmp *ui.Bitmap
	if thumbFn != nil {
		bmp = thumbFn(img)
	}

	tile := ui.ButtonBase(c.Key(fmt.Sprintf("%d:%s", idx, img.Path))).
		Size(120, 120).
		Radius(12).
		Background(t.Surface).
		Border(1, t.Border).
		Label("查看图片 " + name).
		Tooltip(name)
	if tile.Hovered() {
		tile.Border(1, t.Text.Alpha(0.35))
	}
	tile.Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Fill().Fit(ui.Cover).Radius(11)
			return
		}
		ui.Column(c).Fill().Center().Padding(8).Gap(4).Children(func() {
			ui.Text(c, "图片不可用").FontSize(11).TextColor(t.TextMuted).SingleLine()
			ui.Text(c, name).FontSize(11).TextColor(t.TextMuted).MaxLines(2).TextAlign(ui.Center)
		})
	})
	if tile.Clicked() && onOpenImage != nil {
		onOpenImage(img)
	}
}

// 输入区待发送图片缩略图预览条
// 多图时自动换行并限制最大高度，支持点击右上角 × 移除单张附件
func pendingImageStrip(c *ui.Context, images []pendingImage, thumbFn func(pendingImage) *ui.Bitmap, onRemove func(id int64)) {
	t := c.Theme()
	ui.Scroll(c).MaxHeight(136).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
			for _, img := range images {
				var bmp *ui.Bitmap
				if thumbFn != nil {
					bmp = thumbFn(img)
				}
				ui.Box(c.Key(img.id)).
					Size(64, 64).
					Radius(8).
					Background(t.Background).
					Border(1, t.Border).
					Children(func() {
						if bmp != nil {
							ui.Image(c, bmp).Fill().Fit(ui.Cover).Radius(7).Label(img.name)
						} else {
							ui.Column(c).Fill().Center().Padding(4).Children(func() {
								ui.Text(c, img.name).FontSize(10).TextColor(t.TextMuted).MaxLines(2).TextAlign(ui.Center)
							})
						}
						rm := ui.ButtonBase(c).
							Attach(ui.AnchorTopEnd, ui.AnchorTopEnd).
							Top(2).
							End(2).
							Size(18, 18).
							Center().
							Radius(999).
							Background(ui.RGBA(0, 0, 0, 0.62)).
							Label("移除图片 " + img.name).
							Tooltip("移除 " + img.name)
						if rm.Hovered() {
							rm.Background(t.Danger)
						}
						rm.Children(func() {
							ui.Text(c, "×").FontSize(11).TextColor(ui.Hex("#ffffff"))
						})
						if rm.Clicked() && onRemove != nil {
							onRemove(img.id)
						}
					})
			}
		})
	})
}

// 思考过程可折叠卡片
// 淡色边框容器内嵌左侧细竖条与次要正文，返回本帧是否发生展开/收起切换
func reasoningBox(c *ui.Context, label string, open *bool, text string) bool {
	t := c.Theme()
	changed := false
	ui.Column(c).
		FillWidth().
		Padding(6, 10).
		Radius(t.Radius).
		Background(t.Surface.Alpha(0.6)).
		Border(1, t.Border.Alpha(0.75)).
		Children(func() {
			col := ui.Collapsible(c, label, open, func() {
				ui.Box(c).
					FillWidth().
					Padding(2, 0, 2, 10).
					BorderWidth(0, 0, 0, 2).
					BorderColor(t.Border).
					Children(func() {
						ui.Text(c, text).
							FontSize(13).
							LineHeight(1.5).
							TextColor(t.TextMuted).
							Selectable()
					})
			})
			changed = col.Changed()
		})
	return changed
}

func isLongToolText(s string) bool {
	return strings.Count(s, "\n")+1 > toolFoldMaxLines || utf8.RuneCountInString(s) > toolFoldMaxRunes
}

func toolTextSummary(s string) string {
	lines := strings.Count(s, "\n") + 1
	if lines > 1 {
		return fmt.Sprintf("共 %d 行", lines)
	}
	return fmt.Sprintf("共 %d 字符", utf8.RuneCountInString(s))
}

func foldToggleButton(c *ui.Context, expanded bool, label string) ui.Element {
	t := c.Theme()
	text := "展开"
	if expanded {
		text = "收起"
	}
	btn := ui.ButtonBase(c).Padding(2, 8).Radius(6).Shrink(0).Label(label)
	if btn.Hovered() {
		btn.Background(t.SurfaceHover)
	}
	btn.Children(func() {
		ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine()
	})
	return btn
}

// 工具调用卡片
// 短参数直接展示；长参数默认折叠，展开后在限高滚动区中展示，返回本帧是否切换折叠态
func toolCallCard(c *ui.Context, key any, name, args string) bool {
	t := c.Theme()
	long := isLongToolText(args)
	changed := false

	card := ui.Column(c.Key(key)).
		FillWidth().
		Padding(8, 12).
		Gap(6).
		Radius(t.Radius).
		Background(t.Surface).
		Border(1, t.Border)
	expanded := ui.Local(card, "expanded", func() bool { return false })

	card.Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			pill(c, "工具调用", t.TextMuted, t.Background)
			ui.Text(c, name).Font("monospace").FontSize(12).FontWeight(600).SingleLine().MinWidth(0)
			if long {
				ui.Spacer(c)
				ui.Text(c, toolTextSummary(args)).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(0)
				if foldToggleButton(c, *expanded, "切换工具参数展开").Clicked() {
					*expanded = !*expanded
					changed = true
				}
			}
		})
		if args == "" {
			return
		}
		if !long {
			ui.Text(c, args).
				Font("monospace").
				FontSize(12).
				LineHeight(1.45).
				TextColor(t.TextMuted).
				Selectable()
			return
		}
		if *expanded {
			ui.Scroll(c).MaxHeight(toolScrollHeight).FillWidth().Children(func() {
				ui.Text(c, args).
					Font("monospace").
					FontSize(12).
					LineHeight(1.45).
					TabSize(4).
					TextColor(t.TextMuted).
					Selectable()
			})
		}
	})
	return changed
}

// 工具输出卡片
// 短输出直接展示；长输出默认在限高滚动区展示并支持手动收起/展开，返回本帧是否切换折叠态
func toolResultCard(c *ui.Context, key any, output string) bool {
	t := c.Theme()
	long := isLongToolText(output)
	changed := false

	card := ui.Column(c.Key(key)).
		FillWidth().
		Padding(8, 12).
		Gap(6).
		Radius(t.Radius).
		Background(t.Surface).
		Border(1, t.Border)
	expanded := ui.Local(card, "expanded", func() bool { return true })

	card.Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			pill(c, "工具输出", t.TextMuted, t.Background)
			if long {
				ui.Spacer(c)
				ui.Text(c, toolTextSummary(output)).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(0)
				if foldToggleButton(c, *expanded, "切换工具输出展开").Clicked() {
					*expanded = !*expanded
					changed = true
				}
			}
		})
		if output == "" {
			ui.Text(c, "（无输出）").FontSize(12).TextColor(t.TextMuted)
			return
		}
		if !long {
			ui.Text(c, output).
				Font("monospace").
				FontSize(12).
				LineHeight(1.45).
				TabSize(4).
				Selectable()
			return
		}
		if *expanded {
			ui.Scroll(c).MaxHeight(toolScrollHeight).FillWidth().Children(func() {
				ui.Text(c, output).
					Font("monospace").
					FontSize(12).
					LineHeight(1.45).
					TabSize(4).
					Selectable()
			})
		}
	})
	return changed
}

// 悬浮回到底部胶囊按钮
// 在消息列表未停留在末尾时浮于底部中央，点击立即滚回最新消息
func scrollToEndButton(c *ui.Context) ui.Element {
	t := c.Theme()
	btn := ui.ButtonBase(c).
		Attach(ui.AnchorBottom, ui.AnchorBottom).
		Bottom(12).
		Padding(6, 14).
		Radius(999).
		Background(t.Background).
		Border(1, t.Border).
		Shadow(0, 2, 10, 0, ui.RGBA(0, 0, 0, 0.14)).
		Label("回到底部")
	if btn.Hovered() {
		btn.Background(t.SurfaceHover)
	}
	btn.Children(func() {
		ui.Text(c, "↓ 回到底部").FontSize(12).TextColor(t.Text).SingleLine()
	})
	return btn
}

// 底部一体化输入卡片容器
// 子输入框获焦或文件拖经时加重边框提示，提供现代桌面聊天的输入区层次
func composerCard(c *ui.Context, dragOver bool, fn func()) {
	t := c.Theme()
	box := ui.Column(c).
		FillWidth().
		Padding(10, 12, 8, 12).
		Gap(8).
		Radius(14).
		Background(t.Surface).
		Border(1, t.Border)
	switch {
	case dragOver:
		box.Border(2, t.Accent)
	case box.FocusWithin():
		box.Border(1, t.Text.Alpha(0.32))
	}
	box.Children(fn)
}

// 紧凑次级芯片按钮
// 用于输入区底部工具条的选项触发器，展开或悬停时呈现清晰轮廓
func chipButton(c *ui.Context, text, label string, active bool) ui.Element {
	t := c.Theme()
	btn := ui.ButtonBase(c).
		Padding(5, 10).
		Radius(8).
		Border(1, t.Border).
		Shrink(0).
		Label(label)
	switch {
	case active:
		btn.Background(t.Background).Border(1, t.Text.Alpha(0.35))
	case btn.Hovered():
		btn.Background(t.SurfaceHover)
	}
	btn.Children(func() {
		ui.Text(c, text).FontSize(12).TextColor(t.Text).SingleLine()
	})
	return btn
}

// 危险/中断主按钮
// 红色强调底色配白色文字，用于“停止”生成与“确认删除”等不可逆或中断操作
func dangerButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	btn := ui.ButtonBase(c).
		Padding(6, 16).
		Radius(t.Radius).
		Background(t.Danger).
		Shrink(0).
		Label(label)
	if btn.Hovered() {
		btn.Background(t.Danger.Alpha(0.88))
	}
	btn.Children(func() {
		ui.Text(c, label).FontSize(13).FontWeight(500).TextColor(ui.Hex("#ffffff")).SingleLine()
	})
	return btn
}

// 工具执行确认卡片
// 琥珀色警示边框包裹目标目录、可滚动命令区与固定可见的拒绝/批准操作栏；长命令默认收起至紧凑高度
func confirmCard(c *ui.Context, confirm *confirmToolCall, onDecide func(allow bool)) {
	t := c.Theme()
	long := isLongToolText(confirm.command)
	maxH := float32(confirmCompactMaxH)
	if confirm.expanded {
		maxH = confirmExpandedMaxH
	}

	ui.Column(c).
		FillWidth().
		Padding(12, 14).
		Gap(10).
		Radius(12).
		Background(t.Warning.Alpha(0.08)).
		Border(1, t.Warning.Alpha(0.38)).
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
				pill(c, "等待确认", t.Warning, t.Warning.Alpha(0.16))
				ui.Text(c, "请求执行本地命令").FontSize(13).FontWeight(600).Shrink(0)
				ui.Text(c, confirm.dir).FontSize(12).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0).TextAlign(ui.End)
				if long {
					ui.Text(c, toolTextSummary(confirm.command)).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(0)
					if foldToggleButton(c, confirm.expanded, "切换待确认命令展开").Clicked() {
						confirm.expanded = !confirm.expanded
					}
				}
			})
			ui.Box(c).
				FillWidth().
				Padding(8, 10).
				Radius(t.Radius).
				Background(t.Background).
				Border(1, t.Border).
				Children(func() {
					ui.Scroll(c).MaxHeight(maxH).FillWidth().Children(func() {
						ui.Text(c, confirm.command).
							Font("monospace").
							FontSize(12).
							LineHeight(1.45).
							TabSize(4).
							Selectable()
					})
				})
			ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Shrink(0).Children(func() {
				if ui.Button(c, "拒绝").Clicked() && onDecide != nil {
					onDecide(false)
				}
				if ui.PrimaryButton(c, "批准").Clicked() && onDecide != nil {
					onDecide(true)
				}
			})
		})
}

// 统一样式模态弹框容器
// 附带细边框、阴影与窄/矮窗最大尺寸保护；主体内容溢出时内部滚动，确保标题与操作按钮始终可见
func modalDialog(c *ui.Context, open *bool, width float32, title string, body, actions func()) {
	t := c.Theme()
	ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.
			Width(width).
			MaxWidthPercent(90).
			MaxHeightPercent(88).
			Padding(20).
			Gap(14).
			Radius(12).
			Background(t.Background).
			Border(1, t.Border).
			Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.32))
		ui.Text(c, title).FontSize(16).FontWeight(600).Shrink(0)
		ui.Scroll(c).Shrink(1).MinHeight(0).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Gap(14).Children(body)
		})
		if actions != nil {
			dialogActions(c, actions)
		}
	})
}

// 表单字段分组
// 统一标签字号、次要文字色与输入控件间距
func formField(c *ui.Context, label string, fn func()) {
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, label).FontSize(12).FontWeight(500).TextColor(c.Theme().TextMuted)
		fn()
	})
}

func dialogError(c *ui.Context, err error) {
	if err == nil {
		return
	}
	t := c.Theme()
	ui.Box(c).
		FillWidth().
		Padding(8, 10).
		Radius(t.Radius).
		Background(t.Danger.Alpha(0.08)).
		Border(1, t.Danger.Alpha(0.3)).
		Children(func() {
			ui.Scroll(c).MaxHeight(dialogErrorMaxH).FillWidth().Children(func() {
				ui.Text(c, err.Error()).FontSize(12).LineHeight(1.45).TextColor(t.Danger).Selectable()
			})
		})
}

func dialogActions(c *ui.Context, fn func()) {
	ui.Row(c).FillWidth().Shrink(0).Justify(ui.End).Gap(8).Padding(4, 0, 0, 0).Children(fn)
}

// 侧边栏会话菜单配置
// 覆盖默认强调色高亮，改为与选中会话一致的柔和灰阶底色
func sessionMenuOptions(c *ui.Context) ui.PopoverMenuButtonOptions {
	t := c.Theme()
	return ui.PopoverMenuButtonOptions{
		At:   ui.AnchorBottomEnd,
		Self: ui.AnchorTopEnd,
		Item: func(item ui.Element) {
			if item.Highlighted() {
				item.Background(t.SurfacePressed).TextColor(t.Text)
			}
		},
	}
}
