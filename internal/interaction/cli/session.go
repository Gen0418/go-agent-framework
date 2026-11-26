package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func (c *CLI) SelectSession(ctx context.Context) (int64, error) {
	fmt.Println("输入 new 新建会话，或输入 sessionID 来加载会话")
	input, err := c.ReadInput(ctx)
	if err != nil {
		return -1, err
	}

	if strings.TrimSpace(input) == "new" {
		return 0, nil
	}

	sessionID, err := strconv.ParseInt(strings.TrimSpace(input), 10, 64)
	if err != nil || sessionID <= 0 {
		return -1, err
	}
	return sessionID, err
}
