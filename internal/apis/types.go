package apis

// 请求

type Message struct {
	Role             string     `json:"role"` // "system" / "user" / "assistant" / "tool"
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallId       string     `json:"tool_call_id,omitempty"`
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

type chatRequest struct {
	Model           string          `json:"model"`
	Messages        []Message       `json:"messages"`
	Stream          bool            `json:"stream"`
	Thinking        *enableThinking `json:"thinking,omitzero"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Tools           []Tool          `json:"tools,omitempty"`
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
