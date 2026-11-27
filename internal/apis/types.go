package apis

// 核心对话领域模型（跨 storage / agent / tools / 交互层共享的数据契约）

// Image 表示随用户消息发送的本地图片附件元数据
// 持久化与内存中仅保存路径及元信息，发送请求时再按需读取编码为 data URL
type Image struct {
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	Name   string `json:"name,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Detail string `json:"detail,omitempty"` // 可选："low" / "high" / "auto"
}

// File 表示通过 Files API 上传并在对话中按 file_id 引用的附件元数据
// 本地同时保留备份路径供展示与系统打开，对话请求中仅发送轻量 file_id
type File struct {
	FileID    string `json:"file_id"`
	Path      string `json:"path,omitempty"`
	Name      string `json:"name"`
	Purpose   string `json:"purpose,omitempty"`
	Size      int64  `json:"size,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

type Message struct {
	Role             string     `json:"role"` // "system" / "user" / "assistant" / "tool"
	Content          string     `json:"content"`
	Images           []Image    `json:"images,omitempty"`
	Files            []File     `json:"files,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
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

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name string `json:"name"`
	Args string `json:"arguments"`
}

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

// 流式事件
type StreamEvent struct {
	Kind         EventKind
	Text         string
	FinishReason string
	ToolCalls    []ToolCall
}
