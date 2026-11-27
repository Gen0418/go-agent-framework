package storage

var latestVersion = len(schema)

var schema = [][]string{
	rawV1,
	rawV2,
}

var rawV1 = []string{
	`CREATE TABLE IF NOT EXISTS sessions (
      id         INTEGER PRIMARY KEY AUTOINCREMENT,
      title      TEXT NOT NULL DEFAULT '',
      created_at TEXT NOT NULL DEFAULT (datetime('now')),
      updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);`,

	`CREATE TABLE IF NOT EXISTS messages (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      session_id        INTEGER NOT NULL,
      role              TEXT NOT NULL,
      content           TEXT NOT NULL DEFAULT '',
      reasoning_content TEXT NOT NULL DEFAULT '',
      tool_calls        TEXT NOT NULL DEFAULT '[]',
      tool_call_id      TEXT NOT NULL DEFAULT '',
      created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);`,

	`CREATE TABLE IF NOT EXISTS api_calls (
      id                       INTEGER PRIMARY KEY AUTOINCREMENT,
      session_id               INTEGER NOT NULL,
      model                    TEXT NOT NULL,
      thinking                 INTEGER NOT NULL DEFAULT 0,
      prompt_tokens            INTEGER NOT NULL DEFAULT 0,
      completion_tokens        INTEGER NOT NULL DEFAULT 0,
      prompt_cache_hit_tokens  INTEGER NOT NULL DEFAULT 0,
      prompt_cache_miss_tokens INTEGER NOT NULL DEFAULT 0,
      finish_reason            TEXT NOT NULL DEFAULT '',
      duration_ms              INTEGER NOT NULL DEFAULT 0,
      created_at               TEXT NOT NULL DEFAULT (datetime('now'))
);`,

	`CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, id);`,
	`CREATE INDEX IF NOT EXISTS idx_api_calls_session ON api_calls(session_id, id);`,
}

var rawV2 = []string{
	`ALTER TABLE messages ADD COLUMN images TEXT NOT NULL DEFAULT '[]';`,
}
