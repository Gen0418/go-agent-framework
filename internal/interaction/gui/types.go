package gui

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/config"
	"go-agent-framework/internal/storage"

	"github.com/egoist/mygo"
)

type scrollKey int64
type inputKey int64

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
	optsOpen      bool                    // 会话选项浮层是否展开
	renameDialog  renameDialog            // 重命名弹框状态
	deleteDialog  deleteDialog            // 删除确认弹框状态

	// 应用级错误
	err error
}

func (a *app) sessionState(id int64) *sessionState {
	if s, ok := a.sessionStates[id]; ok {
		return s
	}
	opts := a.defaultOpts
	opts.Tools = slices.Clone(opts.Tools) // 独立拷贝 Tools，避免跨会话共享切片
	s := &sessionState{opts: opts}
	a.sessionStates[id] = s
	return s
}

func (a *app) selectedSessionState() (storage.Session, *sessionState, bool) {
	if a.sessions.selectedId == 0 {
		return storage.Session{Id: 0, Title: "新对话"}, a.sessionState(0), true
	}
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

// 切换本会话是否启用工具
func (s *sessionState) setToolsEnabled(enabled bool, defaults []apis.Tool) {
	if !enabled {
		s.opts.Tools = nil
		return
	}
	if len(s.opts.Tools) == 0 {
		s.opts.Tools = slices.Clone(defaults)
	}
}

type confirmToolCall struct {
	dir     string
	command string
	reply   chan bool // 容量为1
}

type renameDialog struct {
	open      bool
	sessionId int64
	title     string
	err       error
}

type deleteDialog struct {
	open      bool
	sessionId int64
	title     string
	err       error
}

// 全会话列表
type sessionList struct {
	list       []storage.Session // 全部会话列表
	selectedId int64             // 当前选中会话
}

func (l *sessionList) indexOf(id int64) int {
	return slices.IndexFunc(l.list, func(s storage.Session) bool {
		return s.Id == id
	})
}

func (l *sessionList) selected() (storage.Session, bool) {
	idx := l.indexOf(l.selectedId)
	if idx < 0 {
		return storage.Session{}, false
	}
	return l.list[idx], true
}

func (l *sessionList) add(s storage.Session) {
	l.list = slices.Insert(l.list, 0, s)
}

func (l *sessionList) rename(id int64, title string) bool {
	idx := l.indexOf(id)
	if idx < 0 {
		return false
	}
	l.list[idx].Title = title
	return true
}

// 删除指定会话
// 若删的是当前选中项，则回退到相邻会话或新对话
func (l *sessionList) remove(id int64) (int64, bool) {
	idx := l.indexOf(id)
	if idx < 0 {
		return l.selectedId, false
	}

	l.list = slices.Delete(l.list, idx, idx+1)
	if l.selectedId != id {
		return l.selectedId, true
	}

	if len(l.list) == 0 {
		l.selectedId = 0
	} else if idx < len(l.list) {
		l.selectedId = l.list[idx].Id
	} else {
		l.selectedId = l.list[len(l.list)-1].Id
	}
	return l.selectedId, true
}

type chatMessage struct {
	apis.Message
	reasoningOpen bool
}

func makeSessionTitle(text string) string {
	cleaned := strings.Join(strings.Fields(text), " ")
	if cleaned == "" {
		return "新对话"
	}
	runes := []rune(cleaned)
	if len(runes) > 20 {
		return string(runes[:20]) + "..."
	}
	return cleaned
}
