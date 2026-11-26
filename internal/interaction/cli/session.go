package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var ErrInvalidSession = errors.New("无效的会话 ID")

// SelectSession 提示用户输入 new 或正整数会话 ID
// 输入非法时返回 ErrInvalidSession，输入结束时返回 io.EOF
func (c *CLI) SelectSession(ctx context.Context) (id int64, isNew bool, err error) {
	if _, err := fmt.Fprintln(c.out, "输入 new 新建会话，或输入 sessionID 来加载会话"); err != nil {
		return 0, false, err
	}
	input, err := c.ReadInput(ctx)
	if err != nil {
		return 0, false, err
	}

	trimmed := strings.TrimSpace(input)
	if trimmed == "new" {
		return 0, true, nil
	}

	sessionID, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || sessionID <= 0 {
		return 0, false, ErrInvalidSession
	}
	return sessionID, false, nil
}
