package apis

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

type ToolCall struct {
	Id       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name string `json:"name"`
	Args string `json:"arguments"`
}

type toolCallDelta struct {
	ToolCall
	Index int `json:"index"`
}

type toolCallBuffer map[int]ToolCall

// 将 toolCallDelta 块聚合
func (b toolCallBuffer) merge(deltas []toolCallDelta) {
	for _, delta := range deltas {
		curCall := b[delta.Index]

		if delta.Id != "" {
			curCall.Id = delta.Id
		}
		if delta.Type != "" {
			curCall.Type = delta.Type
		}
		if delta.Function.Name != "" {
			curCall.Function.Name = delta.Function.Name
		}

		curCall.Function.Args += delta.Function.Args
		b[delta.Index] = curCall
	}
}

func (b toolCallBuffer) getCalls() []ToolCall {
	calls := make([]ToolCall, 0, len(b))
	for _, index := range slices.Sorted(maps.Keys(b)) {
		calls = append(calls, b[index])
	}

	return calls
}

func checkToolCalls(calls []ToolCall) error {
	if len(calls) == 0 {
		return fmt.Errorf("模型请求调用工具，但调用列表为空")
	}

	checkedIds := make(map[string]struct{}, len(calls))
	for i, call := range calls {
		if strings.TrimSpace(call.Id) == "" {
			return fmt.Errorf("第 %d 工具缺少ID", i+1)
		}
		if _, ok := checkedIds[call.Id]; ok {
			return fmt.Errorf("工具ID %s 重复", call.Id)
		}

		checkedIds[call.Id] = struct{}{}

		if call.Type != "function" {
			return fmt.Errorf("工具调用 %s 的类型无效：%q", call.Id, call.Type)
		}
		if strings.TrimSpace(call.Function.Name) == "" {
			return fmt.Errorf("工具调用 %s 缺少函数名", call.Id)
		}
	}

	return nil
}
