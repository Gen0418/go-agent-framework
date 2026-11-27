package apis

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	// MaxImageBytes 为单张内联图片的原始字节上限（32 MiB）
	MaxImageBytes = 32 << 20
	// MaxImageRequestBytes 为含图片的整次 HTTP 请求体字节上限（48 MiB）
	MaxImageRequestBytes = 48 << 20
)

// DetectImageMIME 按文件内容魔数识别支持的图片格式及对应扩展名
// 仅接受 JPEG / PNG / GIF / WebP
func DetectImageMIME(data []byte) (mime string, ext string, ok bool) {
	if len(data) == 0 {
		return "", "", false
	}
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return "image/jpeg", ".jpg", true
	case "image/png":
		return "image/png", ".png", true
	case "image/gif":
		return "image/gif", ".gif", true
	case "image/webp":
		return "image/webp", ".webp", true
	default:
		return "", "", false
	}
}

// 将对话消息转换为协议层 requestMessage
// 无图片时保持 content 为纯字符串，含图片时按需读盘编码为 OpenAI 兼容块数组
func encodeRequestMessages(msgs []Message) ([]requestMessage, error) {
	out := make([]requestMessage, len(msgs))
	var totalEncoded int64

	for i, msg := range msgs {
		rm := requestMessage{
			Role:             msg.Role,
			Content:          msg.Content,
			ReasoningContent: msg.ReasoningContent,
			ToolCalls:        msg.ToolCalls,
			ToolCallID:       msg.ToolCallID,
		}
		if len(msg.Images) == 0 {
			out[i] = rm
			continue
		}
		if msg.Role != "user" {
			return nil, fmt.Errorf("仅 user 消息支持附带图片，当前角色为 %q", msg.Role)
		}

		blocks := make([]contentBlock, 0, len(msg.Images)+1)
		if msg.Content != "" {
			blocks = append(blocks, contentBlock{
				Type: "text",
				Text: msg.Content,
			})
		}
		for _, img := range msg.Images {
			dataURL, err := encodeImageDataURL(img)
			if err != nil {
				return nil, err
			}
			totalEncoded += int64(len(dataURL))
			if totalEncoded > MaxImageRequestBytes {
				return nil, fmt.Errorf("图片总编码体积超过请求体上限（%d MiB），请减少图片数量或体积", MaxImageRequestBytes>>20)
			}
			blocks = append(blocks, contentBlock{
				Type: "image_url",
				ImageURL: &imageURLBlock{
					URL:    dataURL,
					Detail: strings.TrimSpace(img.Detail),
				},
			})
		}
		rm.Content = blocks
		out[i] = rm
	}
	return out, nil
}

func encodeImageDataURL(img Image) (string, error) {
	path := strings.TrimSpace(img.Path)
	if path == "" {
		return "", fmt.Errorf("图片附件路径为空")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取图片附件 %q 失败: %w", displayImageName(img), err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取图片附件 %q 失败: %w", displayImageName(img), err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("图片附件 %q 为空文件", displayImageName(img))
	}
	if len(data) > MaxImageBytes {
		return "", fmt.Errorf("图片附件 %q 超过单图大小上限（%d MiB）", displayImageName(img), MaxImageBytes>>20)
	}

	mime, _, ok := DetectImageMIME(data)
	if !ok {
		return "", fmt.Errorf("图片附件 %q 格式不受支持（仅支持 JPEG/PNG/GIF/WebP）", displayImageName(img))
	}

	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func displayImageName(img Image) string {
	if name := strings.TrimSpace(img.Name); name != "" {
		return name
	}
	return img.Path
}
