package gui

import (
	"fmt"

	"go-agent-framework/internal/config"
	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo/ui"
)

// 重命名会话弹框组件
// 包含标题输入框、错误提示及取消/保存按钮，支持回车提交
type renameDialog struct {
	visible   bool
	sessionId int64
	title     string
	err       error
}

func (d *renameDialog) open(session storage.Session) {
	*d = renameDialog{
		visible:   true,
		sessionId: session.Id,
		title:     session.Title,
	}
}

func (d *renameDialog) close() {
	d.visible = false
	d.err = nil
}

func (d *renameDialog) view(c *ui.Context, onSave func(sessionId int64, title string) error) {
	ui.Modal(c, &d.visible, func() {
		ui.Column(c).Children(func() {
			ui.Text(c, "重命名会话")
			submitted := ui.TextInput(c.Key(d.sessionId), &d.title).Submitted()
			if d.err != nil {
				ui.Text(c, d.err.Error())
			}
			ui.Row(c).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					d.close()
				}
				if ui.Button(c, "保存").Clicked() || submitted {
					if err := onSave(d.sessionId, d.title); err != nil {
						d.err = err
						return
					}
					d.close()
				}
			})
		})
	})
}

// 删除会话确认弹框组件
// 包含待删会话提示、错误展示及取消/确认删除按钮
type deleteDialog struct {
	visible   bool
	sessionId int64
	title     string
	err       error
}

func (d *deleteDialog) open(session storage.Session) {
	*d = deleteDialog{
		visible:   true,
		sessionId: session.Id,
		title:     session.Title,
	}
}

func (d *deleteDialog) close() {
	d.visible = false
	d.err = nil
}

func (d *deleteDialog) view(c *ui.Context, onConfirm func(sessionId int64) error) {
	ui.Modal(c, &d.visible, func() {
		ui.Column(c).Children(func() {
			ui.Text(c, "删除会话")
			ui.Text(c, fmt.Sprintf("确定删除“%s”吗？", d.title))
			if d.err != nil {
				ui.Text(c, d.err.Error())
			}
			ui.Row(c).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					d.close()
				}
				if ui.Button(c, "确认删除").Clicked() {
					if err := onConfirm(d.sessionId); err != nil {
						d.err = err
						return
					}
					d.close()
				}
			})
		})
	})
}

// 应用设置弹框组件
// 包含 APIKey 掩码输入与明文切换、Endpoint、默认模型及取消/保存按钮
type settingsDialog struct {
	visible bool
	cfg     config.Config
	showKey bool
	err     error
}

func (d *settingsDialog) open(cfg config.Config) {
	*d = settingsDialog{
		visible: true,
		cfg:     cfg,
	}
}

func (d *settingsDialog) close() {
	d.visible = false
	d.err = nil
}

func (d *settingsDialog) view(c *ui.Context, onSave func(cfg config.Config) error) {
	ui.Modal(c, &d.visible, func() {
		ui.Column(c).Children(func() {
			ui.Text(c, "应用设置")

			ui.Text(c, "APIKey")
			keyInput := ui.TextInput(c, &d.cfg.APIKey).Placeholder("sk-...")
			if !d.showKey {
				keyInput.Password()
			}
			ui.Checkbox(c, &d.showKey, "显示 APIKey")

			ui.Text(c, "Endpoint")
			ui.TextInput(c, &d.cfg.Endpoint)

			ui.Text(c, "默认模型")
			ui.TextInput(c, &d.cfg.DefaultModel)

			if d.err != nil {
				ui.Text(c, d.err.Error())
			}

			ui.Row(c).Children(func() {
				if ui.Button(c, "取消").Clicked() {
					d.close()
				}
				if ui.Button(c, "保存").Clicked() {
					if err := onSave(d.cfg); err != nil {
						d.err = err
						return
					}
					d.close()
				}
			})
		})
	})
}
