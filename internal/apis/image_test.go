package apis

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTestJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTestGIF(t *testing.T) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDetectImageMIME(t *testing.T) {
	// 最小合法 WebP 头（RIFF + 长度 + WEBPVP8 ）
	webpBytes := []byte("RIFF\x1a\x00\x00\x00WEBPVP8 \x0e\x00\x00\x00\x30\x01\x00\x9d\x01\x2a\x01\x00\x01\x00")

	cases := []struct {
		name     string
		data     []byte
		wantMIME string
		wantExt  string
		wantOK   bool
	}{
		{"PNG", makeTestPNG(t), "image/png", ".png", true},
		{"JPEG", makeTestJPEG(t), "image/jpeg", ".jpg", true},
		{"GIF", makeTestGIF(t), "image/gif", ".gif", true},
		{"WebP", webpBytes, "image/webp", ".webp", true},
		{"纯文本伪装拒绝", []byte("hello world"), "", "", false},
		{"空数据拒绝", nil, "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mime, ext, ok := DetectImageMIME(tc.data)
			if ok != tc.wantOK || mime != tc.wantMIME || ext != tc.wantExt {
				t.Fatalf("DetectImageMIME = (%q, %q, %v)，预期 (%q, %q, %v)", mime, ext, ok, tc.wantMIME, tc.wantExt, tc.wantOK)
			}
		})
	}
}

func TestEncodeRequestMessagesAndStreamChatWithImages(t *testing.T) {
	pngData := makeTestPNG(t)
	imgPath := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(imgPath, pngData, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("无图消息保持字符串，有图消息编码为块数组并发送", func(t *testing.T) {
		var capturedBody []byte
		client := &Client{
			Endpoint: "https://api.deepseek.com/chat/completions",
			APIKey:   "sk-test",
			Model:    "deepseek-flash",
			HTTP: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					capturedBody, _ = io.ReadAll(r.Body)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body:       io.NopCloser(strings.NewReader(chunkLine("看到了图片") + sseLine("", "", "stop") + "data: [DONE]\n\n")),
					}, nil
				}),
			},
		}

		msgs := []Message{
			{Role: "user", Content: "纯文本消息"},
			{Role: "assistant", Content: "收到"},
			{
				Role:    "user",
				Content: "请看这张截图",
				Images:  []Image{{Path: imgPath, MIME: "image/png", Name: "shot.png", Detail: "low"}},
			},
		}

		var reply strings.Builder
		for ev, err := range client.StreamChat(t.Context(), msgs, StreamOptions{}) {
			if err != nil {
				t.Fatalf("StreamChat 失败: %v", err)
			}
			if ev.Kind == EventContent {
				reply.WriteString(ev.Text)
			}
		}
		if reply.String() != "看到了图片" {
			t.Fatalf("回复 = %q，预期 看到了图片", reply.String())
		}

		var raw struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(capturedBody, &raw); err != nil {
			t.Fatalf("解析发送请求体失败: %v", err)
		}
		if len(raw.Messages) != 3 {
			t.Fatalf("消息数量 = %d，预期 3", len(raw.Messages))
		}

		// 第 1 条无图消息应仍为普通 JSON 字符串
		var plain string
		if err := json.Unmarshal(raw.Messages[0].Content, &plain); err != nil || plain != "纯文本消息" {
			t.Fatalf("纯文本消息 content = %s，预期 JSON 字符串", raw.Messages[0].Content)
		}

		// 第 3 条带图消息应为 [text, image_url] 块数组
		var blocks []contentBlock
		if err := json.Unmarshal(raw.Messages[2].Content, &blocks); err != nil {
			t.Fatalf("带图消息 content 应为块数组，实际: %s, err=%v", raw.Messages[2].Content, err)
		}
		wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)
		if len(blocks) != 2 ||
			blocks[0].Type != "text" || blocks[0].Text != "请看这张截图" ||
			blocks[1].Type != "image_url" || blocks[1].ImageURL == nil ||
			blocks[1].ImageURL.URL != wantURL || blocks[1].ImageURL.Detail != "low" {
			t.Fatalf("编码后的块数组异常: %+v", blocks)
		}
	})

	t.Run("纯图不带文本时仅输出 image_url 块", func(t *testing.T) {
		reqMsgs, err := encodeRequestMessages([]Message{
			{Role: "user", Images: []Image{{Path: imgPath, MIME: "image/png"}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		blocks, ok := reqMsgs[0].Content.([]contentBlock)
		if !ok || len(blocks) != 1 || blocks[0].Type != "image_url" {
			t.Fatalf("纯图消息编码异常: %#v", reqMsgs[0].Content)
		}
	})

	t.Run("边界与异常场景拦截", func(t *testing.T) {
		badFile := filepath.Join(t.TempDir(), "fake.png")
		if err := os.WriteFile(badFile, []byte("not an image"), 0o600); err != nil {
			t.Fatal(err)
		}

		if _, err := encodeRequestMessages([]Message{
			{Role: "assistant", Images: []Image{{Path: imgPath}}},
		}); err == nil || !strings.Contains(err.Error(), "仅 user 消息") {
			t.Fatalf("非 user 角色带图应报错，实际: %v", err)
		}

		if _, err := encodeRequestMessages([]Message{
			{Role: "user", Images: []Image{{Path: filepath.Join(t.TempDir(), "missing.png"), Name: "missing.png"}}},
		}); err == nil || !strings.Contains(err.Error(), "missing.png") {
			t.Fatalf("缺失文件应返回含文件名的错误，实际: %v", err)
		}

		if _, err := encodeRequestMessages([]Message{
			{Role: "user", Images: []Image{{Path: badFile, Name: "fake.png"}}},
		}); err == nil || !strings.Contains(err.Error(), "格式不受支持") {
			t.Fatalf("非法格式图片应报错，实际: %v", err)
		}
	})
}
