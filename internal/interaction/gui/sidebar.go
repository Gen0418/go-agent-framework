package gui

import (
	"fmt"

	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo/ui"
)

type sidebarProps struct {
	sessions   []storage.Session
	selectedID int64
	onNew      func()
	onSelect   func(sessionID int64)
	onRename   func(session storage.Session)
	onDelete   func(session storage.Session)
	onSettings func()
}

// 侧边栏组件
// 新建会话、会话列表与底部设置入口
func renderSidebar(c *ui.Context, props sidebarProps) {
	ui.Column(c).Width(200).FillHeight().Children(func() {
		if ui.Button(c, "新建会话").Clicked() && props.onNew != nil {
			props.onNew()
		}
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			for _, session := range props.sessions {
				renderSidebarItem(c, session, props)
			}
		})
		if ui.Button(c, "设置").Clicked() && props.onSettings != nil {
			props.onSettings()
		}
	})
}

func renderSidebarItem(c *ui.Context, session storage.Session, props sidebarProps) {
	ui.Row(c.Key(session.ID)).FillWidth().Children(func() {
		if ui.Button(c, session.Title).Grow(1).Clicked() && props.onSelect != nil {
			props.onSelect(session.ID)
		}
		ui.PopoverMenuButton(c, "...", func(m *ui.Menu) {
			if m.Item("重命名").Chosen() && props.onRename != nil {
				props.onRename(session)
			}
			if m.Item("删除").Chosen() && props.onDelete != nil {
				props.onDelete(session)
			}
		}).Label(fmt.Sprintf("更多操作 %d", session.ID))
	})
}
