package apis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	filesAPIPath = "/v1/files"

	// DeepSeek Files API 支持的 purpose 取值
	FilePurposeUserData              = "user_data"
	FilePurposeTextRecognition       = "text_recognition"
	FilePurposeRichLayoutRecognition = "rich_layout_recognition"

	// 云端文件自动过期范围：1 小时 ~ 30 天（默认 30 天）
	MinFileExpiresSeconds     = 3600
	MaxFileExpiresSeconds     = 2592000
	DefaultFileExpiresSeconds = MaxFileExpiresSeconds

	// 按文件类别划分的单文件体积上限
	MaxDocumentFileBytes = 100 << 20 // PDF / Word / PPT: 100 MiB
	MaxImageFileBytes    = 64 << 20  // Files API 图片: 64 MiB
	MaxTextFileBytes     = 32 << 20  // 纯文本 / 代码 / Excel / 电子书: 32 MiB

	fileDocUploadTimeout  = 200 * time.Second
	fileDataUploadTimeout = 10 * time.Minute
	fileDeleteTimeout     = 15 * time.Second
	fileSniffBytes        = 8192
)

// FileUploadSpec 描述推导出的 Files API 上传规格与体积上限
type FileUploadSpec struct {
	Purpose    string
	MaxBytes   int64
	IsDocument bool // PDF / Word / PPT 等需要文档解析的类型
	IsImage    bool // PNG / JPEG / GIF / WebP 图片类型
}

// UploadFileRequest 描述向 DeepSeek Files API 上传文件的参数
// Data 与 Path 二选一；传入 Path 时通过流式管道上传，避免大文件二次拷贝占内存
type UploadFileRequest struct {
	Filename            string
	Purpose             string
	Path                string
	Data                []byte
	ExpiresAfterSeconds int // 0 表示不传过期参数；非 0 须在 [3600, 2592000] 内
}

// UploadedFile 表示 DeepSeek Files API 返回的云端文件对象
type UploadedFile struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
}

// ResolveFileUploadSpec 根据文件名与文件头部内容（建议前 8 KiB）推导 Files API 的 purpose 与大小上限
// 依次按「图片魔数 → 需文档解析的办公格式 → 支持的非文本容器 → UTF-8 文本嗅探」判定，避免枚举源码扩展名
func ResolveFileUploadSpec(filename string, head []byte, richLayout bool) (FileUploadSpec, error) {
	if _, _, ok := DetectImageMIME(head); ok {
		return FileUploadSpec{
			Purpose:  FilePurposeUserData,
			MaxBytes: MaxImageFileBytes,
			IsImage:  true,
		}, nil
	}

	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(filename)))
	switch ext {
	case ".pdf", ".doc", ".docx", ".ppt", ".pptx":
		purpose := FilePurposeTextRecognition
		if richLayout {
			purpose = FilePurposeRichLayoutRecognition
		}
		return FileUploadSpec{
			Purpose:    purpose,
			MaxBytes:   MaxDocumentFileBytes,
			IsDocument: true,
		}, nil
	case ".xls", ".xlsx", ".epub", ".mobi":
		return FileUploadSpec{
			Purpose:  FilePurposeUserData,
			MaxBytes: MaxTextFileBytes,
		}, nil
	}

	if isPlainTextSample(head) {
		return FileUploadSpec{
			Purpose:  FilePurposeUserData,
			MaxBytes: MaxTextFileBytes,
		}, nil
	}

	if ext != "" {
		return FileUploadSpec{}, fmt.Errorf("暂不支持上传该二进制文件（%s）", ext)
	}
	return FileUploadSpec{}, errors.New("暂不支持上传该二进制文件")
}

// 参照 Git 的二进制嗅探规则判断文件头是否为纯文本/源码
// 拒绝 NUL 字节、非法 UTF-8 及非常规控制字符，并容忍采样边界截断的多字节中文字符
func isPlainTextSample(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if len(data) > fileSniffBytes {
		data = data[:fileSniffBytes]
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	if !utf8.Valid(data) {
		trimmed := trimIncompleteTrailingUTF8(data)
		if len(trimmed) == 0 || !utf8.Valid(trimmed) {
			return false
		}
		data = trimmed
	}
	for _, r := range string(data) {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\f' && r != '\v' && r != 0x1b {
			return false
		}
	}
	return true
}

// 截去采样末尾被 8 KiB 边界切断的半个 UTF-8 多字节字符，避免误判合法中文文本为二进制
func trimIncompleteTrailingUTF8(b []byte) []byte {
	limit := min(len(b), utf8.UTFMax-1)
	for cut := 1; cut <= limit; cut++ {
		lead := b[len(b)-cut]
		if lead&0xc0 != 0x80 {
			if !utf8.FullRune(b[len(b)-cut:]) {
				return b[:len(b)-cut]
			}
			break
		}
	}
	return b
}

// UploadFile 上传文件至 DeepSeek Files API
// 严格遵循协议要求将 purpose 字段写入 file 之前，并按文件类型自动选择超时
func (c *Client) UploadFile(ctx context.Context, req UploadFileRequest) (UploadedFile, error) {
	if err := ctx.Err(); err != nil {
		return UploadedFile{}, err
	}
	if c == nil || strings.TrimSpace(c.APIKey) == "" {
		return UploadedFile{}, errors.New("未配置 APIKey，请先在设置中填写")
	}

	purpose := strings.TrimSpace(req.Purpose)
	switch purpose {
	case FilePurposeUserData, FilePurposeTextRecognition, FilePurposeRichLayoutRecognition:
	default:
		return UploadedFile{}, fmt.Errorf("无效的文件 purpose: %q", req.Purpose)
	}

	if req.ExpiresAfterSeconds != 0 &&
		(req.ExpiresAfterSeconds < MinFileExpiresSeconds || req.ExpiresAfterSeconds > MaxFileExpiresSeconds) {
		return UploadedFile{}, fmt.Errorf("文件过期时间必须在 %d 到 %d 秒之间", MinFileExpiresSeconds, MaxFileExpiresSeconds)
	}

	path := strings.TrimSpace(req.Path)
	if path == "" && len(req.Data) == 0 {
		return UploadedFile{}, errors.New("上传文件内容为空")
	}
	filename := strings.TrimSpace(req.Filename)
	if filename == "" && path != "" {
		filename = filepath.Base(strings.ReplaceAll(path, "\\", "/"))
	}
	if filename == "" {
		return UploadedFile{}, errors.New("上传文件名不能为空")
	}

	// 提前校验磁盘文件可读、非目录且非空，避免建立无意义 HTTP 连接
	var srcFile *os.File
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return UploadedFile{}, fmt.Errorf("打开上传文件 %q 失败: %w", filename, err)
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return UploadedFile{}, fmt.Errorf("读取上传文件信息失败: %w", err)
		}
		if info.IsDir() {
			f.Close()
			return UploadedFile{}, fmt.Errorf("%q 是目录，不能作为文件上传", filename)
		}
		if info.Size() == 0 {
			f.Close()
			return UploadedFile{}, fmt.Errorf("文件 %q 为空文件", filename)
		}
		srcFile = f
		defer srcFile.Close()
	}

	timeout := fileDataUploadTimeout
	if purpose == FilePurposeTextRecognition || purpose == FilePurposeRichLayoutRecognition {
		timeout = fileDocUploadTimeout
	}
	return c.uploadFileWithTimeout(ctx, timeout, purpose, filename, req.ExpiresAfterSeconds, srcFile, req.Data)
}

func (c *Client) uploadFileWithTimeout(
	ctx context.Context,
	timeout time.Duration,
	purpose, filename string,
	expiresAfterSeconds int,
	srcFile *os.File,
	data []byte,
) (UploadedFile, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()

	go func() {
		var writeErr error
		defer func() {
			if closeErr := mw.Close(); writeErr == nil {
				writeErr = closeErr
			}
			pw.CloseWithError(writeErr)
		}()

		// DeepSeek Files API 要求 purpose 必须位于 file 字段之前
		if writeErr = mw.WriteField("purpose", purpose); writeErr != nil {
			return
		}
		if expiresAfterSeconds > 0 {
			if writeErr = mw.WriteField("expires_after[anchor]", "created_at"); writeErr != nil {
				return
			}
			if writeErr = mw.WriteField("expires_after[seconds]", strconv.Itoa(expiresAfterSeconds)); writeErr != nil {
				return
			}
		}

		part, err := mw.CreateFormFile("file", filename)
		if err != nil {
			writeErr = err
			return
		}
		if srcFile != nil {
			_, writeErr = io.Copy(part, srcFile)
		} else {
			_, writeErr = io.Copy(part, bytes.NewReader(data))
		}
	}()

	httpReq, err := http.NewRequestWithContext(execCtx, http.MethodPost, c.toolEndpoint(filesAPIPath), pr)
	if err != nil {
		pr.Close()
		return UploadedFile{}, fmt.Errorf("创建上传请求失败: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		pr.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return UploadedFile{}, ctxErr
		}
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return UploadedFile{}, fmt.Errorf("上传或解析超时（超过 %s）: %w", timeout, execCtx.Err())
		}
		return UploadedFile{}, fmt.Errorf("上传文件网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return UploadedFile{}, formatToolHTTPError(resp.StatusCode, resp.Body)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxToolResponseBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return UploadedFile{}, ctxErr
		}
		return UploadedFile{}, fmt.Errorf("读取上传响应失败: %w", err)
	}
	if len(body) > maxToolResponseBytes {
		return UploadedFile{}, fmt.Errorf("上传响应体过大（超过 %d 字节）", maxToolResponseBytes)
	}

	var uploaded UploadedFile
	if err := json.Unmarshal(body, &uploaded); err != nil {
		return UploadedFile{}, fmt.Errorf("解析上传响应失败: %w", err)
	}
	if strings.TrimSpace(uploaded.ID) == "" {
		return UploadedFile{}, errors.New("上传响应缺少 file_id")
	}
	return uploaded, nil
}

// DeleteFile 删除 DeepSeek 云端已上传文件（404 视为已过期/已删除并按幂等成功处理）
func (c *Client) DeleteFile(ctx context.Context, fileID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || strings.TrimSpace(c.APIKey) == "" {
		return errors.New("未配置 APIKey")
	}
	id := strings.TrimSpace(fileID)
	if id == "" {
		return nil
	}

	execCtx, cancel := context.WithTimeout(ctx, fileDeleteTimeout)
	defer cancel()

	endpoint := c.toolEndpoint(filesAPIPath + "/" + url.PathEscape(id))
	req, err := http.NewRequestWithContext(execCtx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("创建删除文件请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.APIKey))
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("删除云端文件失败: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		return formatToolHTTPError(resp.StatusCode, resp.Body)
	}
}

func displayFileName(f File) string {
	if name := strings.TrimSpace(f.Name); name != "" {
		return name
	}
	if id := strings.TrimSpace(f.FileID); id != "" {
		return id
	}
	return f.Path
}
