package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go-agent-framework/internal/apis"
)

// AppendMessage 追加一条消息并刷新所属会话的 updated_at
func (s *Store) AppendMessage(ctx context.Context, sessionID int64, msg apis.Message) error {
	callsJSON := "[]"
	if len(msg.ToolCalls) > 0 {
		data, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return fmt.Errorf("序列化 tool_calls 失败: %w", err)
		}
		callsJSON = string(data)
	}

	imagesJSON := "[]"
	if len(msg.Images) > 0 {
		data, err := json.Marshal(s.toStoredImages(msg.Images))
		if err != nil {
			return fmt.Errorf("序列化 images 失败: %w", err)
		}
		imagesJSON = string(data)
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("开启写入消息事务失败: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO messages
		(session_id, role, content, reasoning_content, tool_calls, tool_call_id, images)
		VALUES (?, ?, ?, ?, ?, ?, ?);`,
		sessionID,
		msg.Role,
		msg.Content,
		msg.ReasoningContent,
		callsJSON,
		msg.ToolCallID,
		imagesJSON,
	); err != nil {
		return fmt.Errorf("插入 messages 失败: %w", err)
	}

	res, err := tx.ExecContext(
		ctx,
		`UPDATE sessions
		SET updated_at = datetime('now')
		WHERE id = ?;`,
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("更新 sessions 时间失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("检查 sessions 更新行数失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("会话 %d 不存在: %w", sessionID, sql.ErrNoRows)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交写入消息事务失败: %w", err)
	}
	return nil
}

// LoadHistory 校验会话存在并按消息顺序载入历史记录
func (s *Store) LoadHistory(ctx context.Context, sessionID int64) ([]apis.Message, error) {
	if _, err := s.QuerySession(ctx, sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("会话 %d 不存在: %w", sessionID, err)
		}
		return nil, err
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT role, content, reasoning_content, tool_calls, tool_call_id, images
		FROM messages
		WHERE session_id = ?
		ORDER BY id;`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("查询 messages 失败: %w", err)
	}
	defer rows.Close()

	var msgs []apis.Message
	for rows.Next() {
		var msg apis.Message
		var rawCalls, rawImages string
		if err := rows.Scan(
			&msg.Role,
			&msg.Content,
			&msg.ReasoningContent,
			&rawCalls,
			&msg.ToolCallID,
			&rawImages,
		); err != nil {
			return nil, fmt.Errorf("扫描 messages 失败: %w", err)
		}

		if rawCalls != "" {
			var calls []apis.ToolCall
			if err := json.Unmarshal([]byte(rawCalls), &calls); err != nil {
				return nil, fmt.Errorf("解析 tool_calls 失败: %w", err)
			}
			if len(calls) > 0 {
				msg.ToolCalls = calls
			}
		}
		if rawImages != "" {
			var imgs []apis.Image
			if err := json.Unmarshal([]byte(rawImages), &imgs); err != nil {
				return nil, fmt.Errorf("解析 images 失败: %w", err)
			}
			if len(imgs) > 0 {
				msg.Images = s.fromStoredImages(imgs)
			}
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 messages 失败: %w", err)
	}
	return msgs, nil
}
