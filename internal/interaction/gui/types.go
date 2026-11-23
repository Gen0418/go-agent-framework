package gui

import (
	"context"
	"database/sql"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo"
)

// GUI应用主体
type app struct {
	// 应用级依赖
	ctx         context.Context
	cancel      context.CancelFunc
	db          *sql.DB
	win         *mygo.Window
	cfg         config.Config      // 应用级配置项(APIKey/Endpoint)
	defaultOpts apis.StreamOptions // 默认会话设置(Thinking/ReasoningEffort/Tools)

	// 会话级信息
	sessions      sessionList
	sessionStates map[int64]*sessionState // sessionId -> sessionState

	// 应用级错误
	err error
}

func (a *app) sessionState(id int64) *sessionState {
	if s, ok := a.sessionStates[id]; ok {
		return s
	}
	s := &sessionState{opts: a.defaultOpts}
	a.sessionStates[id] = s
	return s
}

func (a *app) selectedSessionState() (storage.Session, *sessionState, bool) {
	s, ok := a.sessions.selected()
	if !ok {
		return storage.Session{}, nil, false
	}
	return s, a.sessionState(s.Id), true
}

// 单会话状态
type sessionState struct {
	loop           *agent.AgentLoop   // 会话所绑定AgentLoop
	opts           apis.StreamOptions // 会话设置项(Thinking/ReasoningEffort/Tools)
	messages       []*chatMessage
	curReply       *chatMessage
	confirm        *confirmToolCall
	inputText      string // 提交内容
	historyLoaded  bool   // 判断会话内容是否已加载
	sessionRunning bool   // 判断会话是否正在运行
	cancel         context.CancelFunc
	err            error // 会话级错误
}

// 向 curReply 中流式写入
func (s *sessionState) appendStreamChunk(role agent.OutputRole, chunk string) {
	if role == agent.IsToolContent { // 工具结果
		// 	工具调用非流式，直接追加
		s.messages = append(s.messages, &chatMessage{
			Message: apis.Message{Role: "tool", Content: chunk},
		})
		s.curReply = nil
		return
	}

	if s.curReply == nil { // 当前输出结果buffer为空，新建
		s.curReply = &chatMessage{
			Message: apis.Message{Role: "assistant"},
		}
		s.messages = append(s.messages, s.curReply)
	}

	switch role {
	case agent.IsReasoningContent: // 思考内容
		s.curReply.ReasoningContent += chunk
	case agent.IsContent: // 输出内容
		s.curReply.Content += chunk
	}
}

func (s *sessionState) resolveConfirm(allow bool) {
	if s.confirm == nil {
		return
	}
	c := s.confirm
	s.confirm = nil
	select {
	case c.reply <- allow:
	default:
	}
}

type confirmToolCall struct {
	dir     string
	command string
	reply   chan bool // 容量为1
}

// 全会话列表
type sessionList struct {
	list       []storage.Session // 全部会话列表
	selectedId int64             // 当前选中会话
}

func (l *sessionList) selected() (storage.Session, bool) {
	for _, s := range l.list {
		if s.Id == l.selectedId {
			return s, true
		}
	}
	return storage.Session{}, false
}

type chatMessage struct {
	apis.Message
	reasoningOpen bool
}
