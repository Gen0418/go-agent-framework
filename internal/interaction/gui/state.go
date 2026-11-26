package gui

import (
	"context"
	"slices"
	"strings"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
	"go-agent-framework/internal/storage"
)

type scrollKey int64
type inputKey int64

// 单会话状态
type sessionState struct {
	loop          *agent.Loop        // 会话所绑定 Loop
	opts          apis.StreamOptions // 会话设置项(Thinking/ReasoningEffort/Tools)
	messages      []*chatMessage
	curReply      *chatMessage
	confirm       *confirmToolCall
	inputText     string // 提交内容
	historyLoaded bool   // 判断会话内容是否已加载
	running       bool   // 判断会话是否正在运行
	cancel        context.CancelFunc
	err           error // 会话级错误
}

// 按结构化事件追加流式输出
// 工具结果非流式直接追加，并结束当前 assistant 缓冲
func (s *sessionState) appendStreamEvent(event agent.OutputEvent) {
	if event.Kind == agent.OutputToolResult { // 工具结果
		s.messages = append(s.messages, &chatMessage{
			Message: apis.Message{
				Role:       "tool",
				Content:    event.Text,
				ToolCallID: event.ToolCallID,
			},
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

	switch event.Kind {
	case agent.OutputReasoning: // 思考内容
		s.curReply.ReasoningContent += event.Text
	case agent.OutputContent: // 输出内容
		s.curReply.Content += event.Text
	case agent.OutputToolCall: // 工具调用
		if event.ToolCall != nil {
			s.curReply.ToolCalls = append(s.curReply.ToolCalls, *event.ToolCall)
		}
	}
}

// 用持久化历史快照对齐界面消息列表
// 保留对应位置同角色消息的思考块折叠状态，确保界面与数据库一致
func (s *sessionState) syncHistory(msgs []apis.Message) {
	next := make([]*chatMessage, len(msgs))
	for i, msg := range msgs {
		open := false
		if i < len(s.messages) && s.messages[i].Role == msg.Role {
			open = s.messages[i].reasoningOpen
		}
		next[i] = &chatMessage{
			Message:       msg,
			reasoningOpen: open,
		}
	}
	s.messages = next
	s.curReply = nil
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
// 重新开启时从默认工具列表拷贝恢复，避免共享切片
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

// 全会话列表
type sessionList struct {
	list       []storage.Session // 全部会话列表
	selectedID int64             // 当前选中会话
}

func (l *sessionList) indexOf(id int64) int {
	return slices.IndexFunc(l.list, func(s storage.Session) bool {
		return s.ID == id
	})
}

func (l *sessionList) selected() (storage.Session, bool) {
	idx := l.indexOf(l.selectedID)
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
		return l.selectedID, false
	}

	l.list = slices.Delete(l.list, idx, idx+1)
	if l.selectedID != id {
		return l.selectedID, true
	}

	if len(l.list) == 0 {
		l.selectedID = 0
	} else if idx < len(l.list) {
		l.selectedID = l.list[idx].ID
	} else {
		l.selectedID = l.list[len(l.list)-1].ID
	}
	return l.selectedID, true
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
