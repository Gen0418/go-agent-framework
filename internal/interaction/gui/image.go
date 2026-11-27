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
	// 单条草稿消息允许附带的剪贴板 Base64 原始图片总字节上限（36 MiB 原始数据编码后约 48 MiB）
	maxDraftImageTotalBytes = 36 << 20
	maxThumbCacheEntries    = 128
	draftThumbMaxSide       = 160
	historyThumbMaxSide     = 320
	fileSniffReadBytes      = 8192
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

func (c *imageCache) getPath(path string, maxSide int) *ui.Bitmap {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil
	}
	key := pathThumbKey(trimmed, maxSide)
	if bmp, ok := c.entries[key]; ok {
		return bmp
	}
	data, err := readBoundedFile(trimmed, apis.MaxImageFileBytes)
	var bmp *ui.Bitmap
	if err == nil {
		if decoded, decErr := ui.DecodeBitmapScaled(data, maxSide); decErr == nil {
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

func pathThumbKey(path string, maxSide int) string {
	return fmt.Sprintf("file:%d:%s", maxSide, strings.TrimSpace(path))
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

// 检查待上传附件的可读性、体积上限与 Files API 上传规格（仅读取前 8 KiB 做内容嗅探）
func inspectAttachmentFile(path string, richLayout bool) (name string, size int64, spec apis.FileUploadSpec, err error) {
	cleaned := strings.TrimSpace(path)
	name = filepath.Base(strings.ReplaceAll(cleaned, "\\", "/"))
	f, err := os.Open(cleaned)
	if err != nil {
		return "", 0, apis.FileUploadSpec{}, fmt.Errorf("打开文件 %q 失败: %w", name, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", 0, apis.FileUploadSpec{}, fmt.Errorf("读取文件 %q 信息失败: %w", name, err)
	}
	if info.IsDir() {
		return "", 0, apis.FileUploadSpec{}, fmt.Errorf("%q 是目录，不能作为附件上传", name)
	}
	if info.Size() == 0 {
		return "", 0, apis.FileUploadSpec{}, fmt.Errorf("文件 %q 为空文件", name)
	}

	head := make([]byte, fileSniffReadBytes)
	n, readErr := f.Read(head)
	if readErr != nil && readErr != io.EOF {
		return "", 0, apis.FileUploadSpec{}, fmt.Errorf("读取文件 %q 失败: %w", name, readErr)
	}
	spec, err = apis.ResolveFileUploadSpec(name, head[:n], richLayout)
	if err != nil {
		return "", 0, apis.FileUploadSpec{}, fmt.Errorf("文件 %q: %w", name, err)
	}
	if info.Size() > spec.MaxBytes {
		return "", 0, apis.FileUploadSpec{}, fmt.Errorf("文件 %q 超过大小上限（%d MiB）", name, spec.MaxBytes>>20)
	}
	return name, info.Size(), spec, nil
}

func isImageFileName(name string) bool {
	switch strings.ToLower(filepath.Ext(strings.TrimSpace(name))) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func fileExtBadge(name string) string {
	ext := strings.TrimPrefix(strings.ToUpper(filepath.Ext(strings.TrimSpace(name))), ".")
	if ext == "" {
		return "TXT"
	}
	if len(ext) > 5 {
		return ext[:5]
	}
	return ext
}

func formatByteSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

// 筛选剪贴板文件列表中扩展名属于常见图片的路径
// 用于粘贴文件时区分“复制了图片文件”和“复制了普通代码/文档文件”
func filterSupportedImagePaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if isImageFileName(p) {
			out = append(out, p)
		}
	}
	return out
}
