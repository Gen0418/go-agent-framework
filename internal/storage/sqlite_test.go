package storage

import (
	"bytes"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go-agent-framework/internal/apis"
)

func TestOpenInitializesSchema(t *testing.T) {
	store := openTestStore(t, ":memory:")

	var version int
	if err := store.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != latestVersion {
		t.Fatalf("数据库版本 = %d，预期为 %d", version, latestVersion)
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
		err := store.db.QueryRowContext(t.Context(),
			"SELECT type FROM sqlite_master WHERE name = ?", object.name,
		).Scan(&kind)
		if err != nil || kind != object.kind {
			t.Errorf("对象 %s 的类型 = %q，预期为 %q，查询错误 = %v", object.name, kind, object.kind, err)
		}
	}
}

func TestOpenPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store := openTestStore(t, path)

	title := "SAISON 的 'SQLite' 会话"
	sessionID := createTestSession(t, store, title)
	call := apis.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: apis.ToolCallFunction{
			Name: "bash",
			Args: `{"command":"pwd"}`,
		},
	}
	want := []apis.Message{
		{Role: "user", Content: "这条消息能重开后读到吗？"},
		{Role: "assistant", Content: "可以。", ReasoningContent: "验证重开后的消息", ToolCalls: []apis.ToolCall{call}},
		{Role: "tool", ToolCallID: "call-1", Content: "/workspace"},
	}
	for _, msg := range want {
		if err := store.AppendMessage(t.Context(), sessionID, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, path)
	gotTitle, err := reopened.QuerySession(t.Context(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if gotTitle != title {
		t.Fatalf("会话标题 = %q，预期为 %q", gotTitle, title)
	}

	assertHistory(t, reopened, sessionID, want)
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
			store := openTestStore(t, path)
			if _, err := store.db.ExecContext(t.Context(), tt.ddl); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
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

func TestLoadHistoryBySession(t *testing.T) {
	store := openTestStore(t, ":memory:")
	firstID := createTestSession(t, store, "第一个会话")
	secondID := createTestSession(t, store, "第二个会话")

	assertHistory(t, store, firstID, nil)

	first := apis.Message{Role: "user", Content: "你好，'SAISON'\n第二行"}
	second := apis.Message{Role: "user", Content: "另一条会话"}
	reply := apis.Message{Role: "assistant", Content: "收到。", ReasoningContent: "思考内容"}
	if err := store.AppendMessage(t.Context(), firstID, first); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessage(t.Context(), secondID, second); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessage(t.Context(), firstID, reply); err != nil {
		t.Fatal(err)
	}

	assertHistory(t, store, firstID, []apis.Message{first, reply})
	assertHistory(t, store, secondID, []apis.Message{second})

	// 不存在的会话返回包装后的 sql.ErrNoRows
	if _, err := store.LoadHistory(t.Context(), 9999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("加载不存在会话历史返回 = %v，预期包装 sql.ErrNoRows", err)
	}

	// tool_calls JSON 损坏时报错
	brokenID := createTestSession(t, store, "损坏会话")
	if _, err := store.db.ExecContext(t.Context(),
		`INSERT INTO messages (session_id, role, tool_calls) VALUES (?, 'assistant', '[')`,
		brokenID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadHistory(t.Context(), brokenID); err == nil {
		t.Fatal("损坏的 tool_calls JSON 应返回错误")
	}
}

func TestAppendMessageUpdatesSession(t *testing.T) {
	store := openTestStore(t, ":memory:")
	sessionID := createTestSession(t, store, "更新时间")

	const previous = "2000-01-01 00:00:00"
	if _, err := store.db.ExecContext(t.Context(),
		"UPDATE sessions SET updated_at = ? WHERE id = ?", previous, sessionID,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessage(t.Context(), sessionID, apis.Message{
		Role:    "user",
		Content: "新消息",
	}); err != nil {
		t.Fatal(err)
	}

	var updatedAt string
	if err := store.db.QueryRowContext(t.Context(),
		"SELECT updated_at FROM sessions WHERE id = ?", sessionID,
	).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt <= previous {
		t.Fatalf("会话更新时间 = %q，预期晚于 %q", updatedAt, previous)
	}
}

func TestAppendMessageRollsBackForMissingSession(t *testing.T) {
	store := openTestStore(t, ":memory:")
	const missingSessionID int64 = 42

	err := store.AppendMessage(t.Context(), missingSessionID, apis.Message{
		Role:    "user",
		Content: "应随事务回滚的消息",
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("向不存在的会话写入消息应返回 sql.ErrNoRows，实际 = %v", err)
	}

	var count int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM messages WHERE session_id = ?`, missingSessionID,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("回滚后残留 %d 条消息，预期 0", count)
	}
}

func TestUpdateSessionTitle(t *testing.T) {
	store := openTestStore(t, ":memory:")
	firstID := createTestSession(t, store, "旧标题")
	secondID := createTestSession(t, store, "另一会话")

	if err := store.UpdateSessionTitle(t.Context(), firstID, "新标题"); err != nil {
		t.Fatal(err)
	}

	gotFirst, err := store.QuerySession(t.Context(), firstID)
	if err != nil {
		t.Fatal(err)
	}
	if gotFirst != "新标题" {
		t.Fatalf("更新后标题 = %q，预期 新标题", gotFirst)
	}

	// 不影响其他会话
	gotSecond, err := store.QuerySession(t.Context(), secondID)
	if err != nil {
		t.Fatal(err)
	}
	if gotSecond != "另一会话" {
		t.Fatalf("其他会话标题被误改: %q", gotSecond)
	}

	// 不存在的会话返回 sql.ErrNoRows
	err = store.UpdateSessionTitle(t.Context(), 9999, "不存在")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("更新不存在会话返回错误 = %v，预期包装 sql.ErrNoRows", err)
	}
}

func TestDeleteSession(t *testing.T) {
	store := openTestStore(t, ":memory:")
	targetID := createTestSession(t, store, "待删除会话")
	keepID := createTestSession(t, store, "保留会话")

	for _, sid := range []int64{targetID, keepID} {
		if err := store.AppendMessage(t.Context(), sid, apis.Message{
			Role:    "user",
			Content: "测试消息",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(t.Context(),
			`INSERT INTO api_calls (session_id, model) VALUES (?, ?)`, sid, "deepseek-chat",
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.DeleteSession(t.Context(), targetID); err != nil {
		t.Fatal(err)
	}

	// 目标会话及其关联消息、api_calls 均应清理干净
	if _, err := store.QuerySession(t.Context(), targetID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("删除后查询目标会话返回 = %v，预期 sql.ErrNoRows", err)
	}
	var targetMsgs, targetCalls int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM messages WHERE session_id = ?`, targetID,
	).Scan(&targetMsgs); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM api_calls WHERE session_id = ?`, targetID,
	).Scan(&targetCalls); err != nil {
		t.Fatal(err)
	}
	if targetMsgs != 0 || targetCalls != 0 {
		t.Fatalf("目标会话残留 messages=%d, api_calls=%d，预期均为 0", targetMsgs, targetCalls)
	}

	// 其他会话的数据保持完整
	gotKeepTitle, err := store.QuerySession(t.Context(), keepID)
	if err != nil || gotKeepTitle != "保留会话" {
		t.Fatalf("保留会话标题 = %q, err = %v", gotKeepTitle, err)
	}
	assertHistory(t, store, keepID, []apis.Message{
		{Role: "user", Content: "测试消息"},
	})
	var keepCalls int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM api_calls WHERE session_id = ?`, keepID,
	).Scan(&keepCalls); err != nil {
		t.Fatal(err)
	}
	if keepCalls != 1 {
		t.Fatalf("保留会话 api_calls 数量 = %d，预期 1", keepCalls)
	}

	// 重复删除或不存在的会话返回 sql.ErrNoRows
	if err := store.DeleteSession(t.Context(), targetID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("重复删除返回错误 = %v，预期包装 sql.ErrNoRows", err)
	}
}

func makeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 120, G: 200, B: 80, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestOpenMigratesV1ToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}

	// 手工构造旧版 v1 表结构与既有数据（不含 messages.images 列）
	for _, stmt := range rawV1 {
		if _, err := db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), "PRAGMA user_version = 1;"); err != nil {
		t.Fatal(err)
	}
	res, err := db.ExecContext(t.Context(), `INSERT INTO sessions (title) VALUES ('旧版会话')`)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO messages (session_id, role, content) VALUES (?, 'user', '升级前消息')`, sid,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store := openTestStore(t, path)
	var version int
	if err := store.db.QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != latestVersion {
		t.Fatalf("迁移后版本 = %d，预期 %d", version, latestVersion)
	}
	assertHistory(t, store, sid, []apis.Message{{Role: "user", Content: "升级前消息"}})

	// 升级后的旧会话可以正常附加图片并重新读取
	img, err := store.SaveImage(t.Context(), sid, "after.png", makeTestPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	nextMsg := apis.Message{Role: "user", Content: "升级后带图", Images: []apis.Image{img}}
	if err := store.AppendMessage(t.Context(), sid, nextMsg); err != nil {
		t.Fatal(err)
	}
	assertHistory(t, store, sid, []apis.Message{
		{Role: "user", Content: "升级前消息"},
		nextMsg,
	})
}

func TestSaveLoadAndDeleteSessionImages(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "agent.db")
	store := openTestStore(t, dbPath)

	sid1 := createTestSession(t, store, "带图会话1")
	sid2 := createTestSession(t, store, "带图会话2")
	pngBytes := makeTestPNG(t)

	// 非法格式、空内容与不存在会话均应被拦截
	if _, err := store.SaveImage(t.Context(), sid1, "fake.png", []byte("plain text")); err == nil {
		t.Fatal("伪造的非图片数据应被拒绝")
	}
	if _, err := store.SaveImage(t.Context(), sid1, "empty.png", nil); err == nil {
		t.Fatal("空图片数据应被拒绝")
	}
	if _, err := store.SaveImage(t.Context(), 9999, "shot.png", pngBytes); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("不存在会话保存图片返回 = %v，预期包装 sql.ErrNoRows", err)
	}

	img1, err := store.SaveImage(t.Context(), sid1, `C:\Users\demo\截图.png`, pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	img2, err := store.SaveImage(t.Context(), sid2, "keep.png", pngBytes)
	if err != nil {
		t.Fatal(err)
	}

	wantPrefix := filepath.Join(dir, "attachments", "pic", "1") + string(filepath.Separator)
	if !strings.HasPrefix(img1.Path, wantPrefix) || img1.MIME != "image/png" || img1.Name != "截图.png" || img1.Size != int64(len(pngBytes)) {
		t.Fatalf("SaveImage 返回元数据异常: %+v", img1)
	}
	if saved, err := os.ReadFile(img1.Path); err != nil || !bytes.Equal(saved, pngBytes) {
		t.Fatalf("磁盘图片内容与原始数据不一致: err=%v", err)
	}

	msg1 := apis.Message{Role: "user", Content: "请分析截图", Images: []apis.Image{img1}}
	msg2 := apis.Message{Role: "user", Content: "保留图", Images: []apis.Image{img2}}
	if err := store.AppendMessage(t.Context(), sid1, msg1); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessage(t.Context(), sid2, msg2); err != nil {
		t.Fatal(err)
	}

	// 验证数据库内实际存储的是可移植的相对路径（attachments/pic/...）
	var rawImages string
	if err := store.db.QueryRowContext(t.Context(), `SELECT images FROM messages WHERE session_id = ?`, sid1).Scan(&rawImages); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rawImages, `"attachments/pic/1/`) || strings.Contains(rawImages, dir) {
		t.Fatalf("库内应存储相对路径，实际 images = %s", rawImages)
	}

	// 重开数据库后 LoadHistory 能自动解析回可用路径并与写入前完全一致
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, dbPath)
	assertHistory(t, reopened, sid1, []apis.Message{msg1})
	assertHistory(t, reopened, sid2, []apis.Message{msg2})

	// 删除会话 1 时应联动清理 attachments/pic/1 目录，不影响会话 2
	if err := reopened.DeleteSession(t.Context(), sid1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "attachments", "pic", "1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除会话 1 后附件目录仍存在: err=%v", err)
	}
	if _, err := os.Stat(img2.Path); err != nil {
		t.Fatalf("删除会话 1 不应影响会话 2 附件: err=%v", err)
	}

	// images JSON 损坏时应返回错误
	brokenID := createTestSession(t, reopened, "损坏图片记录")
	if _, err := reopened.db.ExecContext(t.Context(),
		`INSERT INTO messages (session_id, role, images) VALUES (?, 'user', '[')`, brokenID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.LoadHistory(t.Context(), brokenID); err == nil {
		t.Fatal("损坏的 images JSON 应返回错误")
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := Open(t.Context(), path)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("关闭测试数据库失败: %v", err)
		}
	})

	return store
}

func assertOpenFails(t *testing.T, path string) {
	t.Helper()
	store, err := Open(t.Context(), path)
	if store != nil {
		store.Close()
	}
	if store != nil || err == nil {
		t.Fatalf("应返回空句柄和错误，实际返回句柄 = %t，错误 = %v", store != nil, err)
	}
}

func createTestSession(t *testing.T, store *Store, title string) int64 {
	t.Helper()
	id, err := store.CreateSession(t.Context(), title)
	if err != nil {
		t.Fatalf("创建测试会话失败: %v", err)
	}
	if id <= 0 {
		t.Fatalf("会话编号 = %d，预期为正数", id)
	}

	return id
}

func assertHistory(t *testing.T, store *Store, sessionID int64, want []apis.Message) {
	t.Helper()
	got, err := store.LoadHistory(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("查询会话 %d 的消息失败: %v", sessionID, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("会话 %d 的消息 = %+v，预期为 %+v", sessionID, got, want)
	}
}
