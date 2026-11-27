package apis

// 请求

// Image 表示随用户消息发送的本地图片附件元数据
// 持久化与内存中仅保存路径及元信息，发送请求时再按需读取编码为 data URL
type Image struct {
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	Name   string `json:"name,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Detail string `json:"detail,omitempty"` // 可选："low" / "high" / "auto"
}

type Message struct {
	Role             string     `json:"role"` // "system" / "user" / "assistant" / "tool"
	Content          string     `json:"content"`
	Images           []Image    `json:"images,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type enableThinking struct {
	Type string `json:"type"` // "enabled" / "disabled"
}

const (
	thinkingEnabled  = "enabled"
	thinkingDisabled = "disabled"
)

type StreamOptions struct {
	Thinking        bool
	ReasoningEffort string // "low" / "high" / "max"
	Tools           []Tool
}

type EventKind int // 用于标识输出流类型

const (
	EventReasoning EventKind = iota
	EventContent
	EventFinish
	EventDone
)

func (k EventKind) String() string {
	switch k {
	case EventReasoning:
		return "reasoning"
	case EventContent:
		return "content"
	case EventFinish:
		return "finish"
	case EventDone:
		return "done"
	default:
		return "unknown"
	}
}

type Tool struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// 流式事件
type StreamEvent struct {
	Kind         EventKind
	Text         string
	FinishReason string
	ToolCalls    []ToolCall
}

type requestMessage struct {
	Role             string     `json:"role"`
	Content          any        `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type contentBlock struct {
	Type     string         `json:"type"`
	Text     string         `json:"text,omitempty"`
	ImageURL *imageURLBlock `json:"image_url,omitempty"`
}

type imageURLBlock struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type chatRequest struct {
	Model           string           `json:"model"`
	Messages        []requestMessage `json:"messages"`
	Stream          bool             `json:"stream"`
	Thinking        *enableThinking  `json:"thinking,omitzero"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	Tools           []Tool           `json:"tools,omitempty"`
}

// 响应

type delta struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ReasoningContent string          `json:"reasoning_content"`
	ToolCalls        []toolCallDelta `json:"tool_calls"`
}
type chatResponseChunk struct {
	Choices []struct {
		Delta        delta  `json:"delta"`
		FinishReason string `json:"finish_reason"` // ""表示流尚未结束
	} `json:"choices"`
}
