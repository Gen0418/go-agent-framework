package gui

import (
	"fmt"
	"slices"
	"strings"

	"go-agent-framework/internal/apis"

	"github.com/egoist/mygo/ui"
)

var reasoningEfforts = []string{"low", "high", "max"}

// 输入区参数
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
// 一体化圆角卡片，含自适应高度输入框（Enter 发送、Shift+Enter 换行）、选项浮层与发送/停止按钮
func renderComposer(c *ui.Context, props composerProps) {
	composerCard(c, func() {
		ui.TextAreaBase(c.Key(inputKey(props.sessionID)), props.inputText).
			FillWidth().
			Lines(1, 6).
			FontSize(14).
			LineHeight(1.5).
			Padding(2, 2).
			Placeholder("输入消息…  Enter 发送，Shift+Enter 换行").
			Label("消息输入框").
			OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyEnter || k.Mods != 0 {
					return false
				}
				text := k.Text()
				if props.running || strings.TrimSpace(text) == "" || props.onSubmit == nil {
					return true
				}
				*props.inputText = text
				props.onSubmit(text)
				if *props.inputText == "" {
					k.SetText("")
				}
				return true
			})

		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			props.optsPopover.view(c, props.model, props.opts, props.hasTools, props.onToggleTools)
			renderOptsSummary(c, props.opts)
			ui.Spacer(c)
			renderSubmitButton(c, props)
		})
	})
}

func renderOptsSummary(c *ui.Context, opts *apis.StreamOptions) {
	if opts == nil || !opts.Thinking {
		return
	}
	effort := opts.ReasoningEffort
	if effort == "" {
		effort = reasoningEfforts[0]
	}
	ui.Text(c, "思考强度 "+effort).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
}

// 会话选项浮层组件
// 包含当前模型展示、思考开关与强度切换、工具调用开关
type optsPopover struct {
	visible bool
}

func (p *optsPopover) view(c *ui.Context, model string, opts *apis.StreamOptions, hasTools bool, onToggleTools func(enabled bool)) {
	t := c.Theme()
	optsButton := chipButton(c, "选项", "选项", p.visible)
	if optsButton.Clicked() {
		p.visible = !p.visible
	}

	ui.Popover(c, optsButton, &p.visible, func() {
		ui.Column(c).MinWidth(230).Padding(4).Gap(10).Children(func() {
			ui.Text(c, fmt.Sprintf("模型: %s", model)).FontSize(12).TextColor(t.TextMuted).SingleLine()
			ui.Divider(c)

			ui.Checkbox(c, &opts.Thinking, "思考")
			if opts.Thinking {
				effortIdx := slices.Index(reasoningEfforts, opts.ReasoningEffort)
				if effortIdx < 0 { // 未设强度时默认 low
					effortIdx = 0
					opts.ReasoningEffort = reasoningEfforts[0]
				}
				if ui.Segmented(c, &effortIdx, reasoningEfforts...).FillWidth().Changed() {
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
		if dangerButton(c, "停止").Clicked() && props.onStop != nil {
			props.onStop()
		}
		return
	}
	empty := strings.TrimSpace(*props.inputText) == ""
	if ui.PrimaryButton(c, "发送").Disabled(empty).Padding(6, 16).Clicked() {
		if props.onSubmit != nil {
			props.onSubmit(*props.inputText)
		}
	}
}
