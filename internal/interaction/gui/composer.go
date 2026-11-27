package gui

import (
	"fmt"
	"slices"
	"strings"

	"go-agent-framework/internal/apis"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

var reasoningEfforts = []string{"low", "high", "max"}

type inputKey int64

// 会话选项浮层中的独立开关项（本地命令、联网搜索）
type optionToggle struct {
	label    string
	checked  bool
	onChange func(bool)
}

// 输入区附件预览与交互回调函数集合
type attachmentHandlers struct {
	thumbForPending   func(pendingImage) *ui.Bitmap
	thumbForPath      func(path string) *ui.Bitmap
	onPickFiles       func()
	onPasteImageFiles func(paths []string)
	onPasteImage      func(data []byte)
	onRemoveImage     func(id int64)
	onRemoveFile      func(id int64)
	onRetryFile       func(id int64)
}

// 输入区参数
type composerProps struct {
	sessionID   int64
	model       string
	dragOver    bool
	state       *sessionState
	optsPopover *optsPopover
	toolToggles []optionToggle
	attachments attachmentHandlers
	onSubmit    func(text string)
	onStop      func()
}

// 底部输入区组件
// 一体化圆角卡片，含待发图片/文件预览条、自适应输入框（Enter 发送、Shift+Enter 换行、支持粘贴图片与拖入附件）、添加附件、选项浮层与发送/停止按钮
func renderComposer(c *ui.Context, props composerProps) {
	state := props.state
	if state == nil {
		return
	}
	svc := c.Services()
	att := props.attachments
	blocking := state.hasBlockingFiles()

	composerCard(c, props.dragOver, func() {
		if len(state.pendingImages) > 0 || len(state.pendingFiles) > 0 {
			pendingAttachmentStrip(
				c,
				state.pendingImages,
				state.pendingFiles,
				att.thumbForPending,
				att.thumbForPath,
				att.onRemoveImage,
				att.onRemoveFile,
				att.onRetryFile,
			)
		}

		ui.TextAreaBase(c.Key(inputKey(props.sessionID)), &state.inputText).
			FillWidth().
			Lines(1, 6).
			FontSize(14).
			LineHeight(1.5).
			Padding(2, 2).
			Placeholder("输入消息…  Enter 发送，Shift+Enter 换行，支持粘贴截图或拖入附件").
			Label("消息输入框").
			OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyEnter || k.Mods != 0 {
					return false
				}
				text := k.Text()
				hasAttachment := len(state.pendingImages) > 0 || len(state.pendingFiles) > 0
				if state.running || blocking || (strings.TrimSpace(text) == "" && !hasAttachment) || props.onSubmit == nil {
					return true
				}
				state.inputText = text
				props.onSubmit(text)
				if state.inputText == "" {
					k.SetText("")
				}
				return true
			}).
			OnPaste(func(text string) bool {
				if state.running {
					return false
				}
				formats := svc.ClipboardFormats()
				if slices.Contains(formats, transfer.FileList) {
					data := svc.ReadClipboardData(transfer.FileList)
					if paths, err := data.Files(); err == nil {
						if files := filterSupportedImagePaths(paths); len(files) > 0 && att.onPasteImageFiles != nil {
							att.onPasteImageFiles(files)
							return true
						}
					}
				}
				if strings.TrimSpace(text) == "" && slices.Contains(formats, transfer.PNG) {
					data := svc.ReadClipboardData(transfer.PNG)
					if imgBytes, err := data.Read(transfer.PNG); err == nil && len(imgBytes) > 0 && att.onPasteImage != nil {
						att.onPasteImage(imgBytes)
						return true
					}
				}
				return false
			})

		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			if props.optsPopover != nil {
				props.optsPopover.view(c, props)
			}
			if att.onPickFiles != nil {
				attachBtn := chipButton(c, "+ 附件", "添加附件", false).Disabled(state.running)
				if attachBtn.Clicked() {
					att.onPickFiles()
				}
			}
			renderOptsSummary(c, &state.opts)
			ui.Spacer(c)
			renderSubmitButton(c, props, blocking)
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
	ui.Text(c, "思考强度 "+effort).
		FontSize(12).
		TextColor(c.Theme().TextMuted).
		SingleLine().
		MinWidth(0)
}

// 会话选项浮层组件
// 包含当前模型展示、思考开关与强度切换，以及本地命令、联网搜索与文档图表识别开关
type optsPopover struct {
	visible bool
}

func (p *optsPopover) view(c *ui.Context, props composerProps) {
	t := c.Theme()
	state := props.state
	optsButton := chipButton(c, "选项", "选项", p.visible)
	if optsButton.Clicked() {
		p.visible = !p.visible
	}

	ui.Popover(c, optsButton, &p.visible, func() {
		ui.Column(c).MinWidth(230).Padding(4).Gap(10).Children(func() {
			ui.Text(c, fmt.Sprintf("模型: %s", props.model)).FontSize(12).TextColor(t.TextMuted).SingleLine()
			ui.Divider(c)

			ui.Checkbox(c, &state.opts.Thinking, "思考")
			if state.opts.Thinking {
				effortIdx := slices.Index(reasoningEfforts, state.opts.ReasoningEffort)
				if effortIdx < 0 { // 未设强度时默认 low
					effortIdx = 0
					state.opts.ReasoningEffort = reasoningEfforts[0]
				}
				if ui.Segmented(c, &effortIdx, reasoningEfforts...).FillWidth().Changed() {
					state.opts.ReasoningEffort = reasoningEfforts[effortIdx]
				}
			}

			if len(props.toolToggles) > 0 {
				ui.Divider(c)
				for _, item := range props.toolToggles {
					checked := item.checked
					if ui.Checkbox(c, &checked, item.label).Changed() && item.onChange != nil {
						item.onChange(checked)
					}
				}
			}

			ui.Divider(c)
			ui.Checkbox(c, &state.docRichLayout, "识别文档图表").
				Tooltip("默认仅提取纯文本；勾选后在解析 PDF/Word/PPT 时保留并识别图表与版面")
		})
	})
}

func renderSubmitButton(c *ui.Context, props composerProps, blocking bool) {
	state := props.state
	if state.running {
		if dangerButton(c, "停止").Clicked() && props.onStop != nil {
			props.onStop()
		}
		return
	}
	hasAttachment := len(state.pendingImages) > 0 || len(state.pendingFiles) > 0
	empty := strings.TrimSpace(state.inputText) == "" && !hasAttachment
	if ui.PrimaryButton(c, "发送").Disabled(empty || blocking).Padding(6, 16).Clicked() {
		if props.onSubmit != nil {
			props.onSubmit(state.inputText)
		}
	}
}
