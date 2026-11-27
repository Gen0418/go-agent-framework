package gui

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go-agent-framework/internal/apis"

	"github.com/egoist/mygo/ui"
)

const (
	// 单条草稿消息允许附带的原始图片总字节上限（36 MiB 原始数据经 Base64 编码后约 48 MiB）
	maxDraftImageTotalBytes = 36 << 20
	maxThumbCacheEntries    = 128
	draftThumbMaxSide       = 160
	historyThumbMaxSide     = 320
)

// 缩略图位图缓存（仅在 UI 主线程访问）
// 使用缩放解码避免在内存中保留全分辨率大图，并缓存失败路径防止每帧重复读盘
type imageCache struct {
	entries map[string]*ui.Bitmap
	order   []string
}

func (c *imageCache) getPending(img pendingImage) *ui.Bitmap {
	key := pendingThumbKey(img.id)
	if bmp, ok := c.entries[key]; ok {
		return bmp
	}
	bmp, err := ui.DecodeBitmapScaled(img.data, draftThumbMaxSide)
	if err != nil {
		bmp = nil
	}
	c.put(key, bmp)
	return bmp
}

func (c *imageCache) getFile(img apis.Image) *ui.Bitmap {
	path := strings.TrimSpace(img.Path)
	if path == "" {
		return nil
	}
	key := "file:" + path
	if bmp, ok := c.entries[key]; ok {
		return bmp
	}
	data, err := readBoundedFile(path, apis.MaxImageBytes)
	var bmp *ui.Bitmap
	if err == nil {
		if decoded, decErr := ui.DecodeBitmapScaled(data, historyThumbMaxSide); decErr == nil {
			bmp = decoded
		}
	}
	c.put(key, bmp)
	return bmp
}

func (c *imageCache) evict(key string) {
	if c.entries == nil {
		return
	}
	if _, ok := c.entries[key]; !ok {
		return
	}
	delete(c.entries, key)
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *imageCache) put(key string, bmp *ui.Bitmap) {
	if c.entries == nil {
		c.entries = make(map[string]*ui.Bitmap)
	}
	if _, exists := c.entries[key]; !exists {
		if len(c.order) >= maxThumbCacheEntries {
			oldest := c.order[0]
			c.order = c.order[1:]
			delete(c.entries, oldest)
		}
		c.order = append(c.order, key)
	}
	c.entries[key] = bmp
}

func pendingThumbKey(id int64) string {
	return fmt.Sprintf("draft:%d", id)
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("超过单图大小上限（%d MiB）", maxBytes>>20)
	}
	return data, nil
}

func loadImageFile(path string) (name, mime string, data []byte, err error) {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"))
	data, err = readBoundedFile(path, apis.MaxImageBytes)
	if err != nil {
		return "", "", nil, fmt.Errorf("读取图片 %q 失败: %w", name, err)
	}
	mime, err = validateImageBytes(name, data)
	if err != nil {
		return "", "", nil, err
	}
	return name, mime, data, nil
}

func validateImageBytes(name string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("图片 %q 为空文件", name)
	}
	if len(data) > apis.MaxImageBytes {
		return "", fmt.Errorf("图片 %q 超过单图大小上限（%d MiB）", name, apis.MaxImageBytes>>20)
	}
	mime, _, ok := apis.DetectImageMIME(data)
	if !ok {
		return "", fmt.Errorf("图片 %q 格式不受支持（仅支持 JPEG/PNG/GIF/WebP）", name)
	}
	return mime, nil
}

// 筛选剪贴板文件列表中扩展名属于常见图片的路径
// 用于粘贴文件时区分“复制了图片文件”和“复制了普通代码/文档文件”
func filterSupportedImagePaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		switch strings.ToLower(filepath.Ext(strings.TrimSpace(p))) {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp":
			out = append(out, p)
		}
	}
	return out
}
