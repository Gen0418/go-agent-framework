package apis

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
)

const (
	dataPrefix   = "data:"
	doneSentinel = "[DONE]"
)

// 流式对话入口，返回iter.Seq2回调函数用于range
func (c *Client) StreamChat(ctx context.Context, msgs []Message, opts StreamOptions) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		reqBody, err := json.Marshal(c.newChatRequest(msgs, opts))
		if err != nil {
			yield(StreamEvent{}, err)
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(reqBody))
		if err != nil {
			yield(StreamEvent{}, err)
			return
		}
		c.setStreamHeaders(req)

		resp, err := c.httpClient().Do(req)
		if err != nil {
			yield(StreamEvent{}, err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errMsg, _ := io.ReadAll(io.LimitReader(resp.Body, 512)) // 错误时读取API报错内容
			yield(StreamEvent{}, fmt.Errorf("HTTP %d 请求失败: %s", resp.StatusCode, bytes.TrimSpace(errMsg)))
			return
		}

		if err := handleStream(resp.Body, yield); err != nil {
			yield(StreamEvent{}, err)
		}
	}

}

// 流式解析SSE
func handleStream(body io.Reader, yield func(StreamEvent, error) bool) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	callsBuffer := make(toolCallBuffer)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), dataPrefix)
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)

		if data == doneSentinel { // 流结束
			yield(StreamEvent{Kind: EventDone}, nil)
			return nil
		}

		chunk, err := parseStreamChunk(data)
		if err != nil {
			return fmt.Errorf("解析 chunk 失败: %w", err)
		}
		// 首块只带 role、末块只带 finish_reason，content 均为空
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]

		if choice.Delta.ReasoningContent != "" { // thinking块
			if !yield(StreamEvent{
				Kind: EventReasoning,
				Text: choice.Delta.ReasoningContent,
			}, nil) {
				return nil
			}
		}
		if choice.Delta.Content != "" { // 输出块
			if !yield(StreamEvent{
				Kind: EventContent,
				Text: choice.Delta.Content,
			}, nil) {
				return nil
			}
		}
		if len(choice.Delta.ToolCalls) > 0 { // 工具调用块
			callsBuffer.merge(choice.Delta.ToolCalls)
		}
		if choice.FinishReason != "" { // 结束块
			var calls []ToolCall
			if choice.FinishReason == "tool_calls" {
				calls = callsBuffer.getCalls()
				if err := checkToolCalls(calls); err != nil {
					return err
				}
			}
			if !yield(StreamEvent{
				Kind:         EventFinish,
				FinishReason: choice.FinishReason,
				ToolCalls:    calls,
			}, nil) {
				return nil
			}
		}

	}
	return scanner.Err()
}

// 解析 chatResponseChunk
func parseStreamChunk(raw string) (chatResponseChunk, error) {
	var chunk chatResponseChunk

	err := json.Unmarshal([]byte(raw), &chunk)
	return chunk, err
}
