package handler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// service 单测验证绑定语义；本契约测试钉住两个 handler 的真实调用守卫，避免
// 后续只保留 helper 而再次漏掉 Responses/Chat 的 WaitPlan 等待出口。
func TestGatewayWaitPlanStickyBindingGuards(t *testing.T) {
	for _, tt := range []struct {
		file     string
		function string
	}{
		{file: "gateway_handler_chat_completions.go", function: "ChatCompletions"},
		{file: "gateway_handler_responses.go", function: "Responses"},
	} {
		t.Run(tt.file, func(t *testing.T) {
			source := stripGoComments(goFunctionSource(t, tt.file, tt.function))
			require.Contains(t, source, "selection.ProfitGateActive() || !selection.Acquired")
			guardIndex := strings.Index(source, "selection.ProfitGateActive() || !selection.Acquired")
			bindIndex := strings.Index(source, "BindStickySessionAfterProfitAdmission(")
			require.Greater(t, bindIndex, guardIndex, "准入后绑定必须受 WaitPlan/利润门守卫控制")
		})
	}
}
