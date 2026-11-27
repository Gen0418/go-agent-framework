package apis

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveFileUploadSpec(t *testing.T) {
	pngHead := makeTestPNG(t)
	// 构造末尾恰巧截断在多字节中文字符中间的采样（测试 8 KiB 边界容错）
	truncatedUTF8 := append([]byte("hello "), []byte("中")[:2]...)

	cases := []struct {
		name       string
		file       string
		head       []byte
		richLayout bool
		wantPurp   string
		wantMax    int64
		wantDoc    bool
		wantImg    bool
		wantErr    bool
	}{
		{"PDF 默认图文识别", "report.pdf", []byte("%PDF-1.7"), true, FilePurposeRichLayoutRecognition, MaxDocumentFileBytes, true, false, false},
		{"PDF 切纯文本识别", "report.PDF", []byte("%PDF-1.7"), false, FilePurposeTextRecognition, MaxDocumentFileBytes, true, false, false},
		{"DOCX 图文识别", "spec.docx", []byte("PK\x03\x04\x00"), true, FilePurposeRichLayoutRecognition, MaxDocumentFileBytes, true, false, false},
		{"PPTX 纯文本识别", "deck.pptx", []byte("PK\x03\x04\x00"), false, FilePurposeTextRecognition, MaxDocumentFileBytes, true, false, false},
		{"图片按魔数识别即使无后缀", "clipboard_raw", pngHead, true, FilePurposeUserData, MaxImageFileBytes, false, true, false},
		{"Excel 走 user_data", "data.xlsx", []byte("PK\x03\x04\x00"), true, FilePurposeUserData, MaxTextFileBytes, false, false, false},
		{"无后缀 Makefile 嗅探为文本", "Makefile", []byte("all:\n\tgo test ./...\n"), false, FilePurposeUserData, MaxTextFileBytes, false, false, false},
		{"任意源码如 .proto 嗅探为文本", "api.proto", []byte("syntax = \"proto3\";\n"), true, FilePurposeUserData, MaxTextFileBytes, false, false, false},
		{"截断在中文中间的 UTF-8 采样仍判为文本", "notes.txt", truncatedUTF8, true, FilePurposeUserData, MaxTextFileBytes, false, false, false},
		{"含 NUL 的二进制文件拒绝", "run.exe", []byte("MZ\x90\x00\x03\x00"), true, "", 0, false, false, true},
		{"无后缀随机二进制拒绝", "blob", []byte{0xff, 0xfe, 0x01, 0x02}, true, "", 0, false, false, true},
		{"空内容拒绝", "empty.txt", nil, true, "", 0, false, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ResolveFileUploadSpec(tc.file, tc.head, tc.richLayout)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if spec.Purpose != tc.wantPurp || spec.MaxBytes != tc.wantMax || spec.IsDocument != tc.wantDoc || spec.IsImage != tc.wantImg {
				t.Fatalf("spec = %+v，预期 (%q, %d, doc=%v, img=%v)", spec, tc.wantPurp, tc.wantMax, tc.wantDoc, tc.wantImg)
			}
		})
	}
}

func TestClientUploadFileMultipartOrderAndStreaming(t *testing.T) {
	pdfPath := filepath.Join(t.TempDir(), "annual-report.pdf")
	pdfContent := []byte("%PDF-1.7 mock content")
	if err := os.WriteFile(pdfPath, pdfContent, 0o600); err != nil {
		t.Fatal(err)
	}

	var gotURL, gotAuth string
	var fieldOrder []string
	var fieldValues = make(map[string]string)
	var gotFileName string
	var gotFileBytes []byte

	client := &Client{
		Endpoint: "https://proxy.example.com/ds/v1/chat/completions",
		APIKey:   "sk-upload",
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotURL = r.URL.String()
				gotAuth = r.Header.Get("Authorization")
				_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil {
					t.Fatalf("解析 Content-Type 失败: %v", err)
				}
				mr := multipart.NewReader(r.Body, params["boundary"])
				for {
					part, err := mr.NextPart()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatalf("读取 multipart 失败: %v", err)
					}
					raw, err := io.ReadAll(part)
					if err != nil {
						t.Fatalf("读取分片内容失败: %v", err)
					}
					name := part.FormName()
					fieldOrder = append(fieldOrder, name)
					if name == "file" {
						gotFileName = part.FileName()
						gotFileBytes = raw
					} else {
						fieldValues[name] = string(raw)
					}
				}
				return jsonResp(http.StatusOK, `{
					"id": "file-api-123456",
					"object": "file",
					"bytes": 21,
					"created_at": 1762790000,
					"expires_at": 1765382000,
					"filename": "annual-report.pdf",
					"purpose": "rich_layout_recognition"
				}`), nil
			}),
		},
	}

	uploaded, err := client.UploadFile(t.Context(), UploadFileRequest{
		Path:                pdfPath,
		Purpose:             FilePurposeRichLayoutRecognition,
		ExpiresAfterSeconds: DefaultFileExpiresSeconds,
	})
	if err != nil {
		t.Fatalf("UploadFile 失败: %v", err)
	}
	if gotURL != "https://proxy.example.com/ds/v1/files" || gotAuth != "Bearer sk-upload" {
		t.Fatalf("请求目标或鉴权异常: url=%q auth=%q", gotURL, gotAuth)
	}
	// 协议硬性要求：purpose 必须出现在 file 字段之前
	wantOrder := []string{"purpose", "expires_after[anchor]", "expires_after[seconds]", "file"}
	if strings.Join(fieldOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("表单字段顺序 = %v，预期 %v", fieldOrder, wantOrder)
	}
	if fieldValues["purpose"] != FilePurposeRichLayoutRecognition ||
		fieldValues["expires_after[anchor]"] != "created_at" ||
		fieldValues["expires_after[seconds]"] != "2592000" {
		t.Fatalf("表单字段值异常: %+v", fieldValues)
	}
	if gotFileName != "annual-report.pdf" || string(gotFileBytes) != string(pdfContent) {
		t.Fatalf("上传文件分片异常: name=%q data=%q", gotFileName, gotFileBytes)
	}
	if uploaded.ID != "file-api-123456" || uploaded.ExpiresAt != 1765382000 || uploaded.Purpose != FilePurposeRichLayoutRecognition {
		t.Fatalf("解析返回对象异常: %+v", uploaded)
	}
}

func TestClientUploadFileValidationErrorsAndCancel(t *testing.T) {
	client := &Client{APIKey: "sk-test"}
	if _, err := client.UploadFile(t.Context(), UploadFileRequest{Purpose: "bad", Data: []byte("a"), Filename: "a.txt"}); err == nil {
		t.Fatal("非法 purpose 应报错")
	}
	if _, err := client.UploadFile(t.Context(), UploadFileRequest{Purpose: FilePurposeUserData, Data: []byte("a"), Filename: "a.txt", ExpiresAfterSeconds: 100}); err == nil {
		t.Fatal("过期时间小于 3600 秒应报错")
	}
	if _, err := client.UploadFile(t.Context(), UploadFileRequest{Purpose: FilePurposeUserData, Filename: "empty.txt"}); err == nil {
		t.Fatal("空内容上传应报错")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancelClient := &Client{
		APIKey: "sk-test",
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				cancel()
				<-r.Context().Done()
				return nil, r.Context().Err()
			}),
		},
	}
	if _, err := cancelClient.UploadFile(ctx, UploadFileRequest{
		Filename: "doc.pdf",
		Purpose:  FilePurposeTextRecognition,
		Data:     []byte("%PDF-1.7"),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消上传返回 err = %v，预期 context.Canceled", err)
	}
}

func TestClientDeleteFile(t *testing.T) {
	var gotMethod, gotURL string
	client := &Client{
		Endpoint: "https://api.deepseek.com/chat/completions",
		APIKey:   "sk-del",
		HTTP: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotMethod = r.Method
				gotURL = r.URL.String()
				return jsonResp(http.StatusNotFound, `not found`), nil
			}),
		},
	}
	// 404 按幂等成功处理
	if err := client.DeleteFile(t.Context(), "file-api-abc"); err != nil {
		t.Fatalf("DeleteFile 404 应视为幂等成功，实际 err = %v", err)
	}
	if gotMethod != http.MethodDelete || gotURL != "https://api.deepseek.com/v1/files/file-api-abc" {
		t.Fatalf("删除请求异常: method=%q url=%q", gotMethod, gotURL)
	}
}

func TestEncodeRequestMessagesWithFiles(t *testing.T) {
	futureExp := time.Now().Add(time.Hour).Unix()
	reqMsgs, err := encodeRequestMessages([]Message{
		{
			Role:    "user",
			Content: "总结文档要点",
			Files: []File{{
				FileID:    "file-api-doc-1",
				Name:      "report.pdf",
				Purpose:   FilePurposeRichLayoutRecognition,
				ExpiresAt: futureExp,
			}},
		},
	})
	if err != nil {
		t.Fatalf("编码带文件消息失败: %v", err)
	}
	raw, err := json.Marshal(reqMsgs[0])
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if !strings.Contains(s, `"type":"file"`) || !strings.Contains(s, `"file_id":"file-api-doc-1"`) || !strings.Contains(s, `"总结文档要点"`) {
		t.Fatalf("序列化结果不符: %s", s)
	}

	// 缺少 file_id 与已过期文件均应报错
	if _, err := encodeRequestMessages([]Message{
		{Role: "user", Files: []File{{Name: "pending.pdf"}}},
	}); err == nil || !strings.Contains(err.Error(), "缺少 file_id") {
		t.Fatalf("缺少 file_id 应报错，实际: %v", err)
	}
	if _, err := encodeRequestMessages([]Message{
		{Role: "user", Files: []File{{FileID: "file-api-old", Name: "old.pdf", ExpiresAt: time.Now().Add(-time.Minute).Unix()}}},
	}); err == nil || !strings.Contains(err.Error(), "已过期") {
		t.Fatalf("已过期文件应报错，实际: %v", err)
	}
}
