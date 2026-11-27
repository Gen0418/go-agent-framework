package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"go-agent-framework/internal/agent"
	"go-agent-framework/internal/apis"
)

const (
	ansiReasoning = "\x1b[3;90m" // 斜体、灰色
	ansiTool      = "\x1b[36m"   // 青色
	ansiReset     = "\x1b[0m"    // 恢复默认
)

type CLI struct {
	scanner     *bufio.Scanner
	out         io.Writer
	inReasoning bool
}

func New(in io.Reader, out io.Writer) *CLI {
	return &CLI{
		scanner: bufio.NewScanner(in),
		out:     out,
	}
}

func (c *CLI) ReadInput(ctx context.Context) (string, error) {
	for c.scanner.Scan() {
		text := c.scanner.Text()
		if strings.TrimSpace(text) == "" {
			continue
		}
		return text, nil
	}

	if err := c.scanner.Err(); err != nil {
		return "", err
	}
	return "", io.EOF
}

func (c *CLI) WriteOutput(ctx context.Context, event agent.OutputEvent) error {
	switch event.Kind {
	case agent.OutputReasoning:
		c.inReasoning = true
		_, err := fmt.Fprint(c.out, ansiReasoning, event.Text, ansiReset)
		return err

	case agent.OutputContent:
		prefix := ""
		if c.inReasoning {
			prefix = "\n"
			c.inReasoning = false
		}
		_, err := fmt.Fprint(c.out, prefix, event.Text)
		return err

	case agent.OutputToolCall:
		c.inReasoning = false
		if event.ToolCall == nil {
			return nil
		}
		_, err := fmt.Fprintf(c.out, "\n[工具调用] %s %s\n", event.ToolCall.Function.Name, event.ToolCall.Function.Args)
		return err

	case agent.OutputToolResult:
		c.inReasoning = false
		text := event.Text
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if _, err := fmt.Fprint(c.out, ansiTool, "[工具输出] ", ansiReset); err != nil {
			return err
		}
		_, err := fmt.Fprint(c.out, text)
		return err

	default:
		_, err := fmt.Fprint(c.out, event.Text)
		return err
	}
}

func (c *CLI) Confirm(ctx context.Context, dir, command string) (bool, error) {
	_, err := fmt.Fprintf(c.out, "\n目标目录: %s\n命令: %s\n是否执行?[y/n]", dir, command)
	if err != nil {
		return false, err
	}

	for ctx.Err() == nil { // 循环读取 y/n
		ans, err := c.ReadInput(ctx)
		if err != nil {
			return false, err
		}

		switch strings.ToLower(strings.TrimSpace(ans)) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			if _, err := fmt.Fprint(c.out, "请输入 y 或 n:"); err != nil {
				return false, err
			}
		}
	}
	return false, ctx.Err()
}

func (c *CLI) PrintHistory(ctx context.Context, msgs []apis.Message) error {
	defer func() { c.inReasoning = false }()

	for _, msg := range msgs {
		if msg.Role == "tool" {
			if err := c.WriteOutput(ctx, agent.OutputEvent{
				Kind:       agent.OutputToolResult,
				Text:       msg.Content,
				ToolCallID: msg.ToolCallID,
			}); err != nil {
				return err
			}
			continue
		}
		if strings.TrimSpace(msg.ReasoningContent) != "" {
			if err := c.WriteOutput(ctx, agent.OutputEvent{
				Kind: agent.OutputReasoning,
				Text: "\n" + msg.ReasoningContent,
			}); err != nil {
				return err
			}
		}
		if len(msg.Images) > 0 {
			for _, img := range msg.Images {
				label := "[图片]"
				if name := strings.TrimSpace(img.Name); name != "" {
					label = fmt.Sprintf("[图片: %s]", name)
				}
				if err := c.WriteOutput(ctx, agent.OutputEvent{
					Kind: agent.OutputContent,
					Text: "\n" + label,
				}); err != nil {
					return err
				}
			}
		}
		if len(msg.Files) > 0 {
			for _, file := range msg.Files {
				label := "[文件]"
				if name := strings.TrimSpace(file.Name); name != "" {
					label = fmt.Sprintf("[文件: %s]", name)
				}
				if err := c.WriteOutput(ctx, agent.OutputEvent{
					Kind: agent.OutputContent,
					Text: "\n" + label,
				}); err != nil {
					return err
				}
			}
		}
		if strings.TrimSpace(msg.Content) != "" {
			c.inReasoning = false
			if err := c.WriteOutput(ctx, agent.OutputEvent{
				Kind: agent.OutputContent,
				Text: "\n" + msg.Content,
			}); err != nil {
				return err
			}
		}
		for i := range msg.ToolCalls {
			call := msg.ToolCalls[i]
			if err := c.WriteOutput(ctx, agent.OutputEvent{
				Kind:     agent.OutputToolCall,
				ToolCall: &call,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
