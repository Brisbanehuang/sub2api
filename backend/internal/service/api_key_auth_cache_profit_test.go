package service

// 投影漏列回归（service 半程）：认证快照 build → L2 JSON 序列化
// → 反序列化 → 还原 apiKey.Group → 请求 ctx → 利润门解析，全链路保真。
// repository 半程（真实 GetByKeyForAuth 投影）见
// internal/repository/api_key_repo_profit_projection_integration_test.go。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func profitAuthTestAPIKey() *APIKey {
	groupID := int64(50)
	return &APIKey{
		ID:      82,
		UserID:  40,
		GroupID: &groupID,
		Name:    "profit-auth-roundtrip",
		Status:  StatusActive,
		User: &User{
			ID:          40,
			Email:       "profit@test.local",
			Status:      StatusActive,
			Concurrency: 5,
		},
		Group: &Group{
			ID:                   groupID,
			Name:                 "VIP-roundtrip",
			Platform:             PlatformOpenAI,
			Status:               StatusActive,
			Hydrated:             true,
			RateMultiplier:       0.06,
			SubscriptionType:     SubscriptionTypeStandard,
			PeakRateEnabled:      false,
			ProfitControlEnabled: true,
			ProfitMinMargin:      0.2,
			ProfitSafetyBuffer:   0.05,
		},
	}
}

// 快照构建 → L2 JSON 往返 → 还原 → 装门：利润字段必须全程保真，阈值与
// 计费同源（0.06 × (1−0.25) = 0.045）。
func TestAPIKeyAuthSnapshotProfitControlRoundtrip(t *testing.T) {
	svc := &APIKeyService{}
	apiKey := profitAuthTestAPIKey()

	snapshot := svc.snapshotFromAPIKey(context.Background(), apiKey)
	require.NotNil(t, snapshot)
	require.Equal(t, apiKeyAuthSnapshotVersion, snapshot.Version)
	require.Equal(t, 18, snapshot.Version, "v18 起认证快照携带利润控制字段")

	// 模拟 L2 缓存的完整 JSON 往返（与 apiKeyCache.SetAuthCache/GetAuthCache 同构）。
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	var restored APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &restored))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &restored)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.Hydrated)
	require.True(t, materialized.Group.ProfitControlEnabled)
	require.InDelta(t, 0.2, materialized.Group.ProfitMinMargin, 1e-12)
	require.InDelta(t, 0.05, materialized.Group.ProfitSafetyBuffer, 1e-12)
	require.InDelta(t, 0.06, materialized.Group.RateMultiplier, 1e-12)

	// 中间件语义：materialized.Group 进请求 ctx → 门必须按快照配置装上。
	ctx := context.WithValue(context.Background(), ctxkey.Group, materialized.Group)
	gwSvc := &OpenAIGatewayService{}
	gate := gwSvc.resolveOpenAIProfitControlGate(ctx, materialized.GroupID)
	require.NotNil(t, gate, "还原后的认证分组必须能装门（投影漏列时本断言最先失败）")
	require.InDelta(t, 0.06*(1-0.25), gate.threshold, 1e-12)
}

// 旧版本快照（v16 及更早，无利润字段保真保证）必须被淘汰回源，不得复用。
func TestAPIKeyAuthSnapshotOldVersionEvicted(t *testing.T) {
	svc := &APIKeyService{}
	snapshot := svc.snapshotFromAPIKey(context.Background(), profitAuthTestAPIKey())
	require.NotNil(t, snapshot)
	snapshot.Version = 16

	materialized, used, err := svc.applyAuthCacheEntry("sk-old", &APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	require.False(t, used, "版本不匹配的缓存条目必须淘汰并回源重建")
	require.Nil(t, materialized)
}

// 官方 v0.1.170 与本站旧生产都使用 v18，但旧生产快照多出已经停用的
// profit_exclude_unpriceable 字段。切换时会定向清缓存；本测试再证明残留条目
// 即使被读到，也会按 JSON 向前兼容语义忽略死字段，并保留现行利润门字段。
func TestAPIKeyAuthSnapshotAcceptsLegacyV18ExtraProfitField(t *testing.T) {
	svc := &APIKeyService{}
	snapshot := svc.snapshotFromAPIKey(context.Background(), profitAuthTestAPIKey())
	require.NotNil(t, snapshot)

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(payload, &raw))
	rawSnapshot := raw["snapshot"].(map[string]any)
	rawGroup := rawSnapshot["group"].(map[string]any)
	rawGroup["profit_exclude_unpriceable"] = true
	legacyPayload, err := json.Marshal(raw)
	require.NoError(t, err)

	var restored APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(legacyPayload, &restored))
	require.NotNil(t, restored.Snapshot)
	require.Equal(t, apiKeyAuthSnapshotVersion, restored.Snapshot.Version)
	require.NotNil(t, restored.Snapshot.Group)
	require.True(t, restored.Snapshot.Group.ProfitControlEnabled)
	require.InDelta(t, 0.2, restored.Snapshot.Group.ProfitMinMargin, 1e-12)
	require.InDelta(t, 0.05, restored.Snapshot.Group.ProfitSafetyBuffer, 1e-12)

	materialized, used, err := svc.applyAuthCacheEntry("sk-legacy-v18", &restored)
	require.NoError(t, err)
	require.True(t, used)
	ctx := context.WithValue(context.Background(), ctxkey.Group, materialized.Group)
	gate := (&OpenAIGatewayService{}).resolveOpenAIProfitControlGate(ctx, materialized.GroupID)
	require.NotNil(t, gate, "旧 v18 的额外死字段不得阻止现行利润门安装")
	require.InDelta(t, 0.06*(1-0.25), gate.threshold, 1e-12)
}

// 回滚方向同样兼容：旧 v0.1.169 结构读取官方新 v18 JSON 时，缺失的死字段
// 自然落为 false，三个仍在使用的利润字段保持不变。旧 V2 运行时也已停止读取
// ProfitExcludeUnpriceable，因此该零值不会改变准入语义。
func TestAPIKeyAuthSnapshotLegacyReaderAcceptsOfficialV18(t *testing.T) {
	type legacyProfitGroup struct {
		ProfitControlEnabled     bool    `json:"profit_control_enabled"`
		ProfitMinMargin          float64 `json:"profit_min_margin"`
		ProfitSafetyBuffer       float64 `json:"profit_safety_buffer"`
		ProfitExcludeUnpriceable bool    `json:"profit_exclude_unpriceable"`
	}
	type legacySnapshot struct {
		Version int                `json:"version"`
		Group   *legacyProfitGroup `json:"group,omitempty"`
	}
	type legacyEntry struct {
		Snapshot *legacySnapshot `json:"snapshot,omitempty"`
	}

	snapshot := (&APIKeyService{}).snapshotFromAPIKey(context.Background(), profitAuthTestAPIKey())
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)

	var restored legacyEntry
	require.NoError(t, json.Unmarshal(payload, &restored))
	require.NotNil(t, restored.Snapshot)
	require.Equal(t, 18, restored.Snapshot.Version)
	require.NotNil(t, restored.Snapshot.Group)
	require.True(t, restored.Snapshot.Group.ProfitControlEnabled)
	require.InDelta(t, 0.2, restored.Snapshot.Group.ProfitMinMargin, 1e-12)
	require.InDelta(t, 0.05, restored.Snapshot.Group.ProfitSafetyBuffer, 1e-12)
	require.False(t, restored.Snapshot.Group.ProfitExcludeUnpriceable, "官方新 v18 缺少的旧死字段应安全落为 false")
}
