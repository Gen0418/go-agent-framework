package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
)

const (
	defaultEndpoint = "https://api.deepseek.com/chat/completions"
	defaultModel    = "deepseek-flash" // = DeepSeek-V4.1-Flash-1017
)

type Config struct {
	APIKey       string `json:"api_key"`
	Endpoint     string `json:"endpoint"`
	DefaultModel string `json:"default_model"`
}

// 清理首尾空白并为 Endpoint/DefaultModel 补齐内置默认值
func (c Config) WithDefaults() Config {
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.DefaultModel = strings.TrimSpace(c.DefaultModel)
	if c.Endpoint == "" {
		c.Endpoint = defaultEndpoint
	}
	if c.DefaultModel == "" {
		c.DefaultModel = defaultModel
	}
	return c
}

// 从.env和环境变量读取
func NewConfigFromEnv() (Config, error) {
	cfg, err := loadEnvConfig()
	if err != nil {
		return cfg, err
	}
	cfg = cfg.WithDefaults()
	if cfg.APIKey == "" {
		return cfg, fmt.Errorf("未找到DEEPSEEK_API_KEY")
	}
	return cfg, nil
}

// 加载配置
// 本地配置文件非空项 > .env/环境变量 > 内置默认值
func Load(path string) (Config, error) {
	cfg, err := loadEnvConfig()
	if err != nil {
		return cfg, err
	}

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return cfg, fmt.Errorf("读取配置文件失败: %w", err)
		}
		if err == nil {
			var fileCfg Config
			if err := json.Unmarshal(data, &fileCfg); err != nil {
				return cfg, fmt.Errorf("解析配置文件失败: %w", err)
			}
			if key := strings.TrimSpace(fileCfg.APIKey); key != "" {
				cfg.APIKey = key
			}
			if endpoint := strings.TrimSpace(fileCfg.Endpoint); endpoint != "" {
				cfg.Endpoint = endpoint
			}
			if model := strings.TrimSpace(fileCfg.DefaultModel); model != "" {
				cfg.DefaultModel = model
			}
		}
	}

	return cfg.WithDefaults(), nil
}

// 原子写入本地配置文件
func Save(path string, cfg Config) error {
	if path == "" {
		return errors.New("配置文件路径为空")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}

	data, err := json.MarshalIndent(cfg.WithDefaults(), "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置配置文件权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时配置文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("保存配置文件失败: %w", err)
	}

	cleanup = false
	return nil
}

func loadEnvConfig() (Config, error) {
	var cfg Config
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, fmt.Errorf("解析.env失败: %w", err)
	}
	cfg.APIKey = strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	cfg.Endpoint = strings.TrimSpace(os.Getenv("DEEPSEEK_ENDPOINT"))
	return cfg, nil
}
