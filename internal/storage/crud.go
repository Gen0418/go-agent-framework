package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// 创建 sessions 记录，返回sessionId
func CreateSession(ctx context.Context, db *sql.DB, title string) (int64, error) {
	res, err := db.ExecContext(ctx, "INSERT INTO sessions (title) VALUES (?)", title)
	if err != nil {
		return 0, fmt.Errorf("插入 sessions 失败: %w", err)
	}

	return res.LastInsertId()
}

// 更新 sessions 标题
func UpdateSessionTitle(ctx context.Context, db *sql.DB, sessionId int64, title string) error {
	res, err := db.ExecContext(
		ctx,
		`UPDATE sessions SET title = ? WHERE id = ?;`,
		title,
		sessionId,
	)
	if err != nil {
		return fmt.Errorf("更新 sessions 标题失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新 sessions 标题失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("会话 %d 不存在: %w", sessionId, sql.ErrNoRows)
	}

	return nil
}

// 删除 sessions 记录及其关联的 messages 和 api_calls
func DeleteSession(ctx context.Context, db *sql.DB, sessionId int64) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("删除 sessions 失败: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ?;`, sessionId); err != nil {
		return fmt.Errorf("删除 sessions 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_calls WHERE session_id = ?;`, sessionId); err != nil {
		return fmt.Errorf("删除 sessions 失败: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?;`, sessionId)
	if err != nil {
		return fmt.Errorf("删除 sessions 失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("删除 sessions 失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("会话 %d 不存在: %w", sessionId, sql.ErrNoRows)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("删除 sessions 失败: %w", err)
	}

	return nil
}

// 创建 messages 记录
func CreateMessage(ctx context.Context, db *sql.DB, msg Message) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("插入 messages 失败: %w", err)
	}
	defer tx.Rollback()

	if msg.ToolCalls == "" {
		msg.ToolCalls = "[]"
	}

	if _, err = tx.ExecContext(
		ctx,
		`INSERT INTO messages
		(session_id, role, content, reasoning_content, tool_calls, tool_call_id)
		VALUES (?, ?, ?, ?, ?, ?);`,
		msg.SessionId,
		msg.Role,
		msg.Content,
		msg.ReasoningContent,
		msg.ToolCalls,
		msg.ToolCallId,
	); err != nil {
		return fmt.Errorf("插入 messages 失败: %w", err)
	}

	res, err := tx.ExecContext( // 更新 sessions 更新时间
		ctx,
		`UPDATE sessions
		SET updated_at = datetime('now')
		WHERE id = ?;`,
		msg.SessionId,
	)
	if err != nil {
		return fmt.Errorf("插入 messages 失败: %w", err)
	}
	affected, err := res.RowsAffected() // 检查修改行数
	if err != nil {
		return fmt.Errorf("插入 messages 失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("会话 %d 不存在", msg.SessionId)
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("插入 messages 失败: %w", err)
	}

	return nil
}

// 按 sessionId 查询 sessions 记录
func QuerySession(ctx context.Context, db *sql.DB, sessionId int64) (string, error) {
	var res string
	err := db.QueryRowContext(
		ctx,
		`SELECT title FROM sessions WHERE id = ?`,
		sessionId,
	).Scan(&res)
	if err != nil {
		return "", fmt.Errorf("查询 sessions 失败: %w", err)
	}

	return res, nil
}

// 按 sessionId 查询 messages 记录
func QueryMessages(ctx context.Context, db *sql.DB, sessionId int64) ([]Message, error) {
	rows, err := db.QueryContext(
		ctx,
		`SELECT role, content, reasoning_content, tool_calls, tool_call_id
		FROM messages
		WHERE session_id = ?
		ORDER BY id;`,
		sessionId,
	)
	if err != nil {
		return nil, fmt.Errorf("查询 messages 失败: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		msg := Message{SessionId: sessionId}

		if err := rows.Scan(
			&msg.Role,
			&msg.Content,
			&msg.ReasoningContent,
			&msg.ToolCalls,
			&msg.ToolCallId,
		); err != nil {
			return nil, fmt.Errorf("查询 messages 失败: %w", err)
		}

		msgs = append(msgs, msg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("查询 messages 失败: %w", err)
	}

	return msgs, nil
}

// 查询全部 sessions，更新时间倒序
func ListSessions(ctx context.Context, db *sql.DB) ([]Session, error) {
	sessions := []Session{}
	rows, err := db.QueryContext(
		ctx,
		`SELECT id, title FROM sessions ORDER BY updated_at DESC, id DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.Id, &session.Title); err != nil {
			return nil, fmt.Errorf("列出 sessions 失败: %w", err)
		}

		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("列出 sessions 失败: %w", err)
	}
	return sessions, nil
}
