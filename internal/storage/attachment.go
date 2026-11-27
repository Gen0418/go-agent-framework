package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go-agent-framework/internal/apis"
)

const attachmentsPicRel = "attachments/pic"

// SaveImage 校验并将图片二进制原子写入 attachments/pic/<sessionID>/ 目录
// 返回携带可访问路径与标准元信息的 apis.Image，供消息发送与入库复用
func (s *Store) SaveImage(ctx context.Context, sessionID int64, name string, data []byte) (apis.Image, error) {
	if sessionID <= 0 {
		return apis.Image{}, fmt.Errorf("无效会话编号: %d", sessionID)
	}
	if _, err := s.QuerySession(ctx, sessionID); err != nil {
		return apis.Image{}, fmt.Errorf("保存图片前校验会话 %d 失败: %w", sessionID, err)
	}
	if len(data) == 0 {
		return apis.Image{}, errors.New("图片内容为空")
	}
	if len(data) > apis.MaxImageBytes {
		return apis.Image{}, fmt.Errorf("图片大小超过上限（%d MiB）", apis.MaxImageBytes>>20)
	}

	mime, ext, ok := apis.DetectImageMIME(data)
	if !ok {
		return apis.Image{}, errors.New("仅支持 JPEG、PNG、GIF、WebP 格式图片")
	}

	baseDir, err := s.ensureBaseDir()
	if err != nil {
		return apis.Image{}, err
	}

	sessionDir := filepath.Join(baseDir, filepath.FromSlash(attachmentsPicRel), strconv.FormatInt(sessionID, 10))
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return apis.Image{}, fmt.Errorf("创建图片附件目录失败: %w", err)
	}

	fileName, err := newAttachmentFileName(ext)
	if err != nil {
		return apis.Image{}, fmt.Errorf("生成图片文件名失败: %w", err)
	}
	fullPath := filepath.Join(sessionDir, fileName)
	if err := writeFileAtomic(sessionDir, fullPath, data); err != nil {
		return apis.Image{}, fmt.Errorf("写入图片附件失败: %w", err)
	}

	return apis.Image{
		Path: fullPath,
		MIME: mime,
		Name: cleanAttachmentName(name, fileName),
		Size: int64(len(data)),
	}, nil
}

func (s *Store) ensureBaseDir() (string, error) {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	if s.baseDir != "" {
		return s.baseDir, nil
	}
	dir, err := os.MkdirTemp("", "saison-attachments-*")
	if err != nil {
		return "", fmt.Errorf("创建临时附件目录失败: %w", err)
	}
	s.baseDir = dir
	s.tempBaseDir = true
	return s.baseDir, nil
}

func (s *Store) currentBaseDir() string {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	return s.baseDir
}

// 将图片路径归一化为相对 baseDir 的可移植路径（以 / 分隔）存入数据库
func (s *Store) toStoredImages(imgs []apis.Image) []apis.Image {
	baseDir := s.currentBaseDir()
	out := make([]apis.Image, len(imgs))
	for i, img := range imgs {
		out[i] = img
		if baseDir != "" && img.Path != "" {
			if rel, err := filepath.Rel(baseDir, img.Path); err == nil &&
				rel != "" && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
				out[i].Path = filepath.ToSlash(rel)
			}
		}
	}
	return out
}

// 将数据库中存储的相对图片路径解析回当前 baseDir 下的实际文件路径
func (s *Store) fromStoredImages(imgs []apis.Image) []apis.Image {
	baseDir := s.currentBaseDir()
	out := make([]apis.Image, len(imgs))
	for i, img := range imgs {
		out[i] = img
		if baseDir != "" && img.Path != "" && !filepath.IsAbs(img.Path) {
			out[i].Path = filepath.Join(baseDir, filepath.FromSlash(img.Path))
		}
	}
	return out
}

func (s *Store) removeSessionAttachments(sessionID int64) error {
	baseDir := s.currentBaseDir()
	if baseDir == "" || sessionID <= 0 {
		return nil
	}
	sessionDir := filepath.Join(baseDir, filepath.FromSlash(attachmentsPicRel), strconv.FormatInt(sessionID, 10))
	if err := os.RemoveAll(sessionDir); err != nil {
		return fmt.Errorf("清理会话 %d 图片附件目录失败: %w", sessionID, err)
	}
	return nil
}

func newAttachmentFileName(ext string) (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d-%s%s", time.Now().UnixMilli(), hex.EncodeToString(raw[:]), ext), nil
}

func cleanAttachmentName(name, fallback string) string {
	cleaned := strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return fallback
	}
	return cleaned
}

func writeFileAtomic(dir, target string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".pic-*.tmp")
	if err != nil {
		return err
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
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return err
	}
	cleanup = false
	return nil
}
