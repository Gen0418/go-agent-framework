package gui

import (
	"fmt"

	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo/ui"
)

type sidebarItem struct {
	session        storage.Session
	running        bool
	pendingConfirm bool
}

type sidebarProps struct {
	items      []sidebarItem
	selectedID int64
	onNew      func()
	onCollapse func()
	onSelect   func(sessionID int64)
	onRename   func(session storage.Session)
	onDelete   func(session storage.Session)
	onSettings func()
}

// 侧边栏组件
// 包含品牌顶栏与收起入口、新建会话按钮、带选中/状态指示的会话列表与底部设置入口
func renderSidebar(c *ui.Context, props sidebarProps) {
	t := c.Theme()

	sidebarPanel(c, func() {
		topBar(c, 12, func() {
			ui.Column(c).Grow(1).Gap(1).Children(func() {
				ui.Text(c, "SAISON").FontSize(14).FontWeight(600).LetterSpacing(0.8).SingleLine()
				ui.Text(c, "Ready to use, free to brew.").FontSize(11).Italic().TextColor(t.TextMuted).SingleLine()
			})
			if iconButton(c, "«", "收起侧边栏").Clicked() && props.onCollapse != nil {
				props.onCollapse()
			}
		})

		ui.Box(c).Shrink(0).Padding(12, 12, 8, 12).Children(func() {
			if outlineButton(c, "+ 新建会话", "新建会话", props.selectedID == 0).Clicked() && props.onNew != nil {
				props.onNew()
			}
		})

		ui.Scroll(c).Grow(1).FillWidth().Padding(4, 8, 8, 8).Children(func() {
			if len(props.items) == 0 {
				ui.Text(c, "暂无历史会话").FontSize(12).TextColor(t.TextMuted).TextAlign(ui.Center).FillWidth().Padding(20, 8)
				return
			}
			ui.Column(c).Gap(2).FillWidth().Children(func() {
				for _, item := range props.items {
					renderSidebarItem(c, item, props)
				}
			})
		})

		ui.Box(c).Shrink(0).Padding(8, 12).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			if ghostButton(c, "设置").Clicked() && props.onSettings != nil {
				props.onSettings()
			}
		})
	})
}

func renderSidebarItem(c *ui.Context, item sidebarItem, props sidebarProps) {
	t := c.Theme()
	session := item.session
	selected := session.ID == props.selectedID

	row := ui.Row(c.Key(session.ID)).FillWidth().AlignItems(ui.Center).Gap(4).Padding(2, 4, 2, 10).Radius(t.Radius)
	switch {
	case selected:
		row.Background(t.SurfacePressed)
	case row.Hovered():
		row.Background(t.SurfaceHover)
	}

	row.Children(func() {
		selectBtn := ui.ButtonBase(c).Grow(1).AlignItems(ui.Center).Gap(6).Padding(6, 0).Label(session.Title)
		selectBtn.Children(func() {
			title := ui.Text(c, session.Title).Grow(1).FontSize(13).SingleLine()
			if selected {
				title.FontWeight(600).TextColor(t.Text)
			} else {
				title.TextColor(t.Text.Alpha(0.86))
			}
			if item.pendingConfirm {
				pill(c, "待确认", t.Warning, t.Warning.Alpha(0.14))
			} else if item.running {
				ui.Spinner(c).Size(12, 12).Label("生成中").Shrink(0)
			}
		})
		if selectBtn.Clicked() && props.onSelect != nil {
			props.onSelect(session.ID)
		}

		more := ui.ButtonBase(c).Size(26, 26).Center().Radius(6).Shrink(0).
			Label(fmt.Sprintf("更多操作 %d", session.ID)).
			PopoverMenuButton(func(m *ui.Menu) {
				if m.Item("重命名").Chosen() && props.onRename != nil {
					props.onRename(session)
				}
				if m.Item("删除").Chosen() && props.onDelete != nil {
					props.onDelete(session)
				}
			}, sessionMenuOptions(c))
		if more.Hovered() {
			more.Background(t.SurfacePressed)
		}
		moreColor := t.TextMuted.Alpha(0.55)
		if selected || row.Hovered() || more.Hovered() {
			moreColor = t.TextMuted
		}
		more.Children(func() {
			ui.Text(c, "···").FontSize(13).TextColor(moreColor)
		})
	})
}
