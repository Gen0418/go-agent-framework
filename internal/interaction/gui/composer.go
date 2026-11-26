package gui

import (
	"fmt"
	"slices"
	"strings"

	"go-agent-framework/internal/apis"

	"github.com/egoist/mygo/ui"
)

var reasoningEfforts = []string{"low", "high", "max"}

// 输入区
type composerProps struct {
	sessionID     int64
	inputText     *string
	model         string
	opts          *apis.StreamOptions
	hasTools      bool
	optsPopover   *optsPopover
	running       bool
	onToggleTools func(enabled bool)
	onSubmit      func(text string)
	onStop        func()
}

// 底部输入区组件
// 输入框、会话选项浮层、发送/停止按钮
func renderComposer(c *ui.Context, props composerProps) {
	ui.TextArea(c.Key(inputKey(props.sessionID)), props.inputText)
	ui.Row(c).Children(func() {
		props.optsPopover.view(c, props.model, props.opts, props.hasTools, props.onToggleTools)
		renderSubmitButton(c, props)
	})
}

// 会话选项浮层组件
// 包含当前模型展示、思考开关与强度切换、工具调用开关
type optsPopover struct {
	visible bool
}

func (p *optsPopover) view(c *ui.Context, model string, opts *apis.StreamOptions, hasTools bool, onToggleTools func(enabled bool)) {
	optsButton := ui.Button(c, "选项")
	if optsButton.Clicked() {
		p.visible = !p.visible
	}

	ui.Popover(c, optsButton, &p.visible, func() {
		ui.Column(c).Children(func() {
			ui.Text(c, fmt.Sprintf("模型: %s", model))

			ui.Checkbox(c, &opts.Thinking, "思考")
			if opts.Thinking {
				effortIdx := slices.Index(reasoningEfforts, opts.ReasoningEffort)
				if effortIdx < 0 { // 未设强度时默认 low
					effortIdx = 0
					opts.ReasoningEffort = reasoningEfforts[0]
				}
				if ui.Segmented(c, &effortIdx, reasoningEfforts...).Changed() {
					opts.ReasoningEffort = reasoningEfforts[effortIdx]
				}
			}

			if hasTools {
				toolsOn := len(opts.Tools) > 0
				if ui.Checkbox(c, &toolsOn, "允许工具调用").Changed() && onToggleTools != nil {
					onToggleTools(toolsOn)
				}
			}
		})
	})
}

func renderSubmitButton(c *ui.Context, props composerProps) {
	if props.running {
		if ui.Button(c, "停止").Clicked() && props.onStop != nil {
			props.onStop()
		}
		return
	}
	if ui.Button(c, "提交").Clicked() {
		if strings.TrimSpace(*props.inputText) == "" {
			return
		}
		if props.onSubmit != nil {
			props.onSubmit(*props.inputText)
		}
	}
}
