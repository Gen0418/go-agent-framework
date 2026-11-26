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
	sessionID int64
	title     string
	err       error
}

func (d *renameDialog) open(session storage.Session) {
	*d = renameDialog{
		visible:   true,
		sessionID: session.ID,
		title:     session.Title,
	}
}

func (d *renameDialog) close() {
	d.visible = false
	d.err = nil
}

func (d *renameDialog) view(c *ui.Context, onSave func(sessionID int64, title string) error) {
	modalDialog(c, &d.visible, 360, "重命名会话", func() {
		submitted := false
		formField(c, "会话标题", func() {
			submitted = ui.TextInput(c.Key(d.sessionID), &d.title).
				FillWidth().
				AutoFocus().
				Placeholder("输入新的会话标题").
				Submitted()
		})
		dialogError(c, d.err)
		dialogActions(c, func() {
			if ui.Button(c, "取消").Clicked() {
				d.close()
			}
			if ui.PrimaryButton(c, "保存").Clicked() || submitted {
				if err := onSave(d.sessionID, d.title); err != nil {
					d.err = err
					return
				}
				d.close()
			}
		})
	})
}

// 删除会话确认弹框组件
// 包含待删会话提示、不可恢复说明、错误展示及取消/危险红确认删除按钮
type deleteDialog struct {
	visible   bool
	sessionID int64
	title     string
	err       error
}

func (d *deleteDialog) open(session storage.Session) {
	*d = deleteDialog{
		visible:   true,
		sessionID: session.ID,
		title:     session.Title,
	}
}

func (d *deleteDialog) close() {
	d.visible = false
	d.err = nil
}

func (d *deleteDialog) view(c *ui.Context, onConfirm func(sessionID int64) error) {
	t := c.Theme()
	modalDialog(c, &d.visible, 360, "删除会话", func() {
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			ui.Text(c, fmt.Sprintf("确定删除“%s”吗？", d.title)).FontSize(13).LineHeight(1.5)
			ui.Text(c, "删除后该会话的消息记录将无法恢复。").FontSize(12).TextColor(t.TextMuted)
		})
		dialogError(c, d.err)
		dialogActions(c, func() {
			if ui.Button(c, "取消").Clicked() {
				d.close()
			}
			if dangerButton(c, "确认删除").Clicked() {
				if err := onConfirm(d.sessionID); err != nil {
					d.err = err
					return
				}
				d.close()
			}
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
	t := c.Theme()
	modalDialog(c, &d.visible, 440, "应用设置", func() {
		ui.Text(c, "保存至本地 config.json，仅对后续发起的请求生效。").
			FontSize(12).
			TextColor(t.TextMuted)

		formField(c, "APIKey", func() {
			keyInput := ui.TextInput(c, &d.cfg.APIKey).
				FillWidth().
				AutoFocus().
				Placeholder("sk-...")
			if !d.showKey {
				keyInput.Password()
			}
			ui.Checkbox(c, &d.showKey, "显示 APIKey")
		})

		formField(c, "Endpoint", func() {
			ui.TextInput(c, &d.cfg.Endpoint).
				FillWidth().
				Placeholder("https://api.deepseek.com/chat/completions")
		})

		formField(c, "默认模型", func() {
			ui.TextInput(c, &d.cfg.DefaultModel).
				FillWidth().
				Placeholder("deepseek-flash")
		})

		dialogError(c, d.err)

		dialogActions(c, func() {
			if ui.Button(c, "取消").Clicked() {
				d.close()
			}
			if ui.PrimaryButton(c, "保存").Clicked() {
				if err := onSave(d.cfg); err != nil {
					d.err = err
					return
				}
				d.close()
			}
		})
	})
}
