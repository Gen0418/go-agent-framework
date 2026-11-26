package storage

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Store 封装 SQLite 数据库句柄，对外隐藏底层 *sql.DB 与序列化细节
type Store struct {
	db *sql.DB
}

// Open 打开并按版本迁移 SQLite 数据库
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s 失败: %w", path, err)
	}

	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil { // 连接探测
		db.Close()
		return nil, fmt.Errorf("数据库 %s 连接异常: %w", path, err)
	}
	if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("数据库 %s 启用外键约束失败: %w", path, err)
	}

	if err = syncDBVersion(ctx, db); err != nil { // 表版本同步
		db.Close()
		return nil, fmt.Errorf("数据库 %s 同步版本异常: %w", path, err)
	}

	return &Store{db: db}, nil
}

// Close 关闭底层数据库连接
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// 根据 schemas 中的 ddl 自动初始化并迁移表结构
func syncDBVersion(ctx context.Context, db *sql.DB) error {
	var version int

	err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if err != nil {
		return fmt.Errorf("初始化数据库错误: %w", err)
	}
	if version > latestVersion || version < 0 {
		return fmt.Errorf("数据库实际版本: %d 超出指定版本范围: 0-%d", version, latestVersion)
	} else if version == latestVersion {
		return nil
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("初始化数据库错误: %w", err)
	}
	defer tx.Rollback()

	for version < latestVersion {
		for _, stmt := range schema[version] {
			if _, err = tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("初始化数据库错误: %w", err)
			}
		}
		version++
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d;", version)); err != nil {
			return fmt.Errorf("更新数据库版本失败: %w", err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("初始化数据库错误: %w", err)
	}

	return nil
}
