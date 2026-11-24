package storage_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"go-agent-framework/internal/storage"
)

func TestOpenInitializesSchema(t *testing.T) {
	db := openTestDB(t, ":memory:")

	var version int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("数据库版本 = %d，预期为 1", version)
	}

	objects := []struct {
		name string
		kind string
	}{
		{"sessions", "table"},
		{"messages", "table"},
		{"api_calls", "table"},
		{"idx_messages_session", "index"},
		{"idx_api_calls_session", "index"},
	}
	for _, object := range objects {
		var kind string
		err := db.QueryRowContext(t.Context(),
			"SELECT type FROM sqlite_master WHERE name = ?", object.name,
		).Scan(&kind)
		if err != nil || kind != object.kind {
			t.Errorf("对象 %s 的类型 = %q，预期为 %q，查询错误 = %v", object.name, kind, object.kind, err)
		}
	}
}

func TestOpenPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	db := openTestDB(t, path)

	title := "SAISON 的 'SQLite' 会话"
	sessionID := createTestSession(t, db, title)
	want := []storage.Message{
		{SessionId: sessionID, Role: "user", Content: "这条消息能重开后读到吗？"},
		{SessionId: sessionID, Role: "assistant", Content: "可以。", ReasoningContent: "验证重开后的消息"},
	}
	for _, msg := range want {
		if err := storage.CreateMessage(t.Context(), db, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestDB(t, path)
	var gotTitle string
	if err := reopened.QueryRowContext(t.Context(),
		"SELECT title FROM sessions WHERE id = ?", sessionID,
	).Scan(&gotTitle); err != nil {
		t.Fatal(err)
	}
	if gotTitle != title {
		t.Fatalf("会话标题 = %q，预期为 %q", gotTitle, title)
	}

	for i := range want {
		want[i].ToolCalls = "[]"
	}
	assertMessages(t, reopened, sessionID, want)
}

func TestOpenRejectsUnsupportedVersion(t *testing.T) {
	tests := []struct {
		name string
		ddl  string
	}{
		{"负版本", "PRAGMA user_version = -1"},
		{"高版本", "PRAGMA user_version = 2147483647"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unsupported.db")
			db := openTestDB(t, path)
			if _, err := db.ExecContext(t.Context(), tt.ddl); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			assertOpenFails(t, path)
		})
	}
}

func TestOpenRejectsInvalidPath(t *testing.T) {
	assertOpenFails(t, filepath.Join(t.TempDir(), "missing", "agent.db"))
}

func TestOpenRollsBackFailedMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migration.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 缺少会话列，建消息索引时触发错误
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE messages (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}

	assertOpenFails(t, path)

	var version int
	if err := db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Errorf("回滚后的数据库版本 = %d，预期为 0", version)
	}

	var count int
	if err := db.QueryRowContext(t.Context(),
		"SELECT count(*) FROM sqlite_master WHERE name IN ('sessions', 'api_calls')",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("回滚后残留 %d 张新表，预期为 0", count)
	}
}

func TestQueryMessagesBySession(t *testing.T) {
	db := openTestDB(t, ":memory:")
	firstID := createTestSession(t, db, "第一个会话")
	secondID := createTestSession(t, db, "第二个会话")

	assertMessages(t, db, firstID, nil)

	first := storage.Message{SessionId: firstID, Role: "user", Content: "你好，'SAISON'\n第二行"}
	second := storage.Message{SessionId: secondID, Role: "user", Content: "另一条会话"}
	reply := storage.Message{SessionId: firstID, Role: "assistant", Content: "收到。", ReasoningContent: "思考内容"}
	for _, msg := range []storage.Message{first, second, reply} {
		if err := storage.CreateMessage(t.Context(), db, msg); err != nil {
			t.Fatal(err)
		}
	}

	first.ToolCalls = "[]"
	second.ToolCalls = "[]"
	reply.ToolCalls = "[]"
	assertMessages(t, db, firstID, []storage.Message{first, reply})
	assertMessages(t, db, secondID, []storage.Message{second})
}

func TestCreateMessageUpdatesSession(t *testing.T) {
	db := openTestDB(t, ":memory:")
	sessionID := createTestSession(t, db, "更新时间")

	const previous = "2000-01-01 00:00:00"
	if _, err := db.ExecContext(t.Context(),
		"UPDATE sessions SET updated_at = ? WHERE id = ?", previous, sessionID,
	); err != nil {
		t.Fatal(err)
	}
	if err := storage.CreateMessage(t.Context(), db, storage.Message{
		SessionId: sessionID,
		Role:      "user",
		Content:   "新消息",
	}); err != nil {
		t.Fatal(err)
	}

	var updatedAt string
	if err := db.QueryRowContext(t.Context(),
		"SELECT updated_at FROM sessions WHERE id = ?", sessionID,
	).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt <= previous {
		t.Fatalf("会话更新时间 = %q，预期晚于 %q", updatedAt, previous)
	}
}

func TestCreateMessageRollsBackForMissingSession(t *testing.T) {
	db := openTestDB(t, ":memory:")
	const missingSessionID int64 = 42

	err := storage.CreateMessage(t.Context(), db, storage.Message{
		SessionId: missingSessionID,
		Role:      "user",
		Content:   "应随事务回滚的消息",
	})
	if err == nil {
		t.Fatal("向不存在的会话写入消息应返回错误")
	}

	assertMessages(t, db, missingSessionID, nil)
}

func TestUpdateSessionTitle(t *testing.T) {
	db := openTestDB(t, ":memory:")
	firstID := createTestSession(t, db, "旧标题")
	secondID := createTestSession(t, db, "另一会话")

	if err := storage.UpdateSessionTitle(t.Context(), db, firstID, "新标题"); err != nil {
		t.Fatal(err)
	}

	gotFirst, err := storage.QuerySession(t.Context(), db, firstID)
	if err != nil {
		t.Fatal(err)
	}
	if gotFirst != "新标题" {
		t.Fatalf("更新后标题 = %q，预期 新标题", gotFirst)
	}

	// 不影响其他会话
	gotSecond, err := storage.QuerySession(t.Context(), db, secondID)
	if err != nil {
		t.Fatal(err)
	}
	if gotSecond != "另一会话" {
		t.Fatalf("其他会话标题被误改: %q", gotSecond)
	}

	// 不存在的会话返回 sql.ErrNoRows
	err = storage.UpdateSessionTitle(t.Context(), db, 9999, "不存在")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("更新不存在会话返回错误 = %v，预期包装 sql.ErrNoRows", err)
	}
}

func TestDeleteSession(t *testing.T) {
	db := openTestDB(t, ":memory:")
	targetID := createTestSession(t, db, "待删除会话")
	keepID := createTestSession(t, db, "保留会话")

	for _, sid := range []int64{targetID, keepID} {
		if err := storage.CreateMessage(t.Context(), db, storage.Message{
			SessionId: sid,
			Role:      "user",
			Content:   "测试消息",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(),
			`INSERT INTO api_calls (session_id, model) VALUES (?, ?)`, sid, "deepseek-chat",
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := storage.DeleteSession(t.Context(), db, targetID); err != nil {
		t.Fatal(err)
	}

	// 目标会话及其关联消息、api_calls 均应清理干净
	if _, err := storage.QuerySession(t.Context(), db, targetID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("删除后查询目标会话返回 = %v，预期 sql.ErrNoRows", err)
	}
	assertMessages(t, db, targetID, nil)
	var targetCalls int
	if err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM api_calls WHERE session_id = ?`, targetID,
	).Scan(&targetCalls); err != nil {
		t.Fatal(err)
	}
	if targetCalls != 0 {
		t.Fatalf("目标会话残留 %d 条 api_calls，预期 0", targetCalls)
	}

	// 其他会话的数据保持完整
	gotKeepTitle, err := storage.QuerySession(t.Context(), db, keepID)
	if err != nil || gotKeepTitle != "保留会话" {
		t.Fatalf("保留会话标题 = %q, err = %v", gotKeepTitle, err)
	}
	assertMessages(t, db, keepID, []storage.Message{
		{SessionId: keepID, Role: "user", Content: "测试消息", ToolCalls: "[]"},
	})
	var keepCalls int
	if err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM api_calls WHERE session_id = ?`, keepID,
	).Scan(&keepCalls); err != nil {
		t.Fatal(err)
	}
	if keepCalls != 1 {
		t.Fatalf("保留会话 api_calls 数量 = %d，预期 1", keepCalls)
	}

	// 重复删除或不存在的会话返回 sql.ErrNoRows
	if err := storage.DeleteSession(t.Context(), db, targetID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("重复删除返回错误 = %v，预期包装 sql.ErrNoRows", err)
	}
}

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := storage.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("关闭测试数据库失败: %v", err)
		}
	})

	return db
}

func assertOpenFails(t *testing.T, path string) {
	t.Helper()
	db, err := storage.Open(t.Context(), path)
	if db != nil {
		db.Close()
	}
	if db != nil || err == nil {
		t.Fatalf("应返回空句柄和错误，实际返回句柄 = %t，错误 = %v", db != nil, err)
	}
}

func createTestSession(t *testing.T, db *sql.DB, title string) int64 {
	t.Helper()
	id, err := storage.CreateSession(t.Context(), db, title)
	if err != nil {
		t.Fatalf("创建测试会话失败: %v", err)
	}
	if id <= 0 {
		t.Fatalf("会话编号 = %d，预期为正数", id)
	}

	return id
}

func assertMessages(t *testing.T, db *sql.DB, sessionID int64, want []storage.Message) {
	t.Helper()
	got, err := storage.QueryMessages(t.Context(), db, sessionID)
	if err != nil {
		t.Fatalf("查询会话 %d 的消息失败: %v", sessionID, err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("会话 %d 的消息 = %+v，预期为 %+v", sessionID, got, want)
	}
}
