package storage

import (
	"context"
	"database/sql"
	"fmt"
)

type Session struct {
	ID    int64
	Title string
}

// CreateSession 创建 sessions 记录，返回 sessionID
func (s *Store) CreateSession(ctx context.Context, title string) (int64, error) {
	res, err := s.db.ExecContext(ctx, "INSERT INTO sessions (title) VALUES (?)", title)
	if err != nil {
		return 0, fmt.Errorf("插入 sessions 失败: %w", err)
	}
	return res.LastInsertId()
}

// QuerySession 按 sessionID 查询会话标题
func (s *Store) QuerySession(ctx context.Context, sessionID int64) (string, error) {
	var title string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT title FROM sessions WHERE id = ?`,
		sessionID,
	).Scan(&title)
	if err != nil {
		return "", fmt.Errorf("查询 sessions 失败: %w", err)
	}
	return title, nil
}

// ListSessions 查询全部 sessions，按更新时间倒序排列
func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	sessions := []Session{}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, title FROM sessions ORDER BY updated_at DESC, id DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("列出 sessions 失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.ID, &session.Title); err != nil {
			return nil, fmt.Errorf("扫描 sessions 失败: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 sessions 失败: %w", err)
	}
	return sessions, nil
}

// UpdateSessionTitle 更新 sessions 标题
func (s *Store) UpdateSessionTitle(ctx context.Context, sessionID int64, title string) error {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE sessions SET title = ? WHERE id = ?;`,
		title,
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("更新 sessions 标题失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("检查 sessions 标题更新行数失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("会话 %d 不存在: %w", sessionID, sql.ErrNoRows)
	}
	return nil
}

// DeleteSession 在单事务中删除会话及其关联的 messages 和 api_calls
func (s *Store) DeleteSession(ctx context.Context, sessionID int64) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("开启删除会话事务失败: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ?;`, sessionID); err != nil {
		return fmt.Errorf("删除会话关联 messages 失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM api_calls WHERE session_id = ?;`, sessionID); err != nil {
		return fmt.Errorf("删除会话关联 api_calls 失败: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?;`, sessionID)
	if err != nil {
		return fmt.Errorf("删除 sessions 记录失败: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("检查 sessions 删除行数失败: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("会话 %d 不存在: %w", sessionID, sql.ErrNoRows)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交删除会话事务失败: %w", err)
	}
	if err := s.removeSessionAttachments(sessionID); err != nil {
		return err
	}
	return nil
}
