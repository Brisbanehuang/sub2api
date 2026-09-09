package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// 这些用例锁定 OpenAI 系调度栈（OpenAIGatewayService）对分组模型路由的处理。
// 通用网关 GatewayService 早就读取 Group.ModelRouting，但 openai/grok/kimi/zhipu/
// deepseek 分组的 Responses / Chat / Messages 全部由 OpenAIGatewayService 承接，
// 那套调度此前从不查表，规则存了也不生效。
//
// 语义与 Anthropic 侧对齐：路由是"优先账号集合"而非硬限定——路由命中时普通会话
// 粘性和优先级都不得压过它，但路由账号全不可用时仍回落普通调度。

const (
	openAIRoutingTestModel       = "gpt-5.6-sol"
	openAIRoutingTestGroupID     = int64(93001)
	openAIRoutingTestRoutedID    = int64(84001)
	openAIRoutingTestPreferredID = int64(84002)
)

// openAIRoutingTestGroupRepo 只覆盖调度取分组用到的两个方法，其余走嵌入接口。
type openAIRoutingTestGroupRepo struct {
	GroupRepository
	group *Group
}

func (r openAIRoutingTestGroupRepo) GetByID(ctx context.Context, id int64) (*Group, error) {
	if r.group != nil && r.group.ID == id {
		return r.group, nil
	}
	return nil, ErrGroupNotFound
}

func (r openAIRoutingTestGroupRepo) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return r.GetByID(ctx, id)
}

// newOpenAIModelRoutingTestService 构造一个带分组快照的 OpenAI 调度服务。
// advanced=true 走 OpenAIAccountScheduler.Select，false 走 legacy 负载感知路径。
func newOpenAIModelRoutingTestService(accounts []Account, group *Group, advanced bool) *OpenAIGatewayService {
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.LoadBatchEnabled = false
	snapshotAccounts := make([]*Account, 0, len(accounts))
	accountsByID := make(map[int64]*Account, len(accounts))
	for i := range accounts {
		snapshotAccounts = append(snapshotAccounts, &accounts[i])
		accountsByID[accounts[i].ID] = &accounts[i]
	}
	svc := &OpenAIGatewayService{
		accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
		cache:              &schedulerTestGatewayCache{},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		// 账号走快照（与生产一致），分组走 groupRepo。
		schedulerSnapshot: &SchedulerSnapshotService{
			cache: &openAISnapshotCacheStub{
				snapshotAccounts: snapshotAccounts,
				accountsByID:     accountsByID,
			},
			accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts},
			groupRepo:   openAIRoutingTestGroupRepo{group: group},
		},
	}
	if advanced {
		svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
	}
	return svc
}

// openAIRoutingTestAccounts 返回两个账号：
// routed 优先级更差（Priority 更大），preferred 是普通调度的天然赢家。
// 只有路由生效时 routed 才会被选中，因此断言不依赖对排序实现的猜测。
func openAIRoutingTestAccounts() []Account {
	return []Account{
		{
			ID:          openAIRoutingTestRoutedID,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    50,
			GroupIDs:    []int64{openAIRoutingTestGroupID},
			Extra:       map[string]any{"openai_responses_supported": true},
		},
		{
			ID:          openAIRoutingTestPreferredID,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 5,
			Priority:    0,
			GroupIDs:    []int64{openAIRoutingTestGroupID},
			Extra:       map[string]any{"openai_responses_supported": true},
		},
	}
}

func openAIRoutingTestGroup(enabled bool, routing map[string][]int64) *Group {
	return &Group{
		ID:                  openAIRoutingTestGroupID,
		Platform:            PlatformOpenAI,
		Status:              StatusActive,
		ModelRoutingEnabled: enabled,
		ModelRouting:        routing,
	}
}

func selectOpenAIRoutingTestAccount(
	t *testing.T,
	svc *OpenAIGatewayService,
	ctx context.Context,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
) (*AccountSelectionResult, error) {
	t.Helper()
	groupID := openAIRoutingTestGroupID
	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx,
		&groupID,
		"",
		sessionHash,
		requestedModel,
		excludedIDs,
		OpenAIUpstreamTransportAny,
		OpenAIEndpointCapabilityResponses,
		false,
		false,
		false,
	)
	return selection, err
}

// openAIRoutingStableSession 固定会话锚点：advanced 调度是加权随机，
// 只有 SessionHash/PreviousResponseID 非空时种子才确定（否则故意注入时间熵），
// 用例必须锚定它才不 flaky。这里的会话本身没有粘性绑定，除非用例显式建立。
// 取值经探测选定：该锚点下 legacy 与 advanced 的无路由基线都落在 preferred 账号上，
// 因此"路由生效"用例才有区分度。requireRoutedOverridesBaseline 会前置校验这一点，
// 加权算法若变动会明确失败而不是静默失效。
const openAIRoutingStableSession = "routing-fixed-session"

// selectRoutingAccountID 跑一次调度并返回选中账号 ID。
func selectRoutingAccountID(
	t *testing.T,
	accounts []Account,
	group *Group,
	advanced bool,
	ctx context.Context,
	sessionHash string,
	requestedModel string,
	excludedIDs map[int64]struct{},
) int64 {
	t.Helper()
	svc := newOpenAIModelRoutingTestService(accounts, group, advanced)
	selection, err := selectOpenAIRoutingTestAccount(t, svc, ctx, sessionHash, requestedModel, excludedIDs)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	return selection.Account.ID
}

// requireBaselinePicksPreferred 前置校验：无路由规则时选中的是 preferred 账号。
// 用例据此才能用"选中了 routed"证明路由确实生效。
func requireBaselinePicksPreferred(t *testing.T, advanced bool, requestedModel string, excludedIDs map[int64]struct{}) {
	t.Helper()
	baseline := selectRoutingAccountID(t, openAIRoutingTestAccounts(), openAIRoutingTestGroup(false, nil), advanced,
		context.Background(), openAIRoutingStableSession, requestedModel, excludedIDs)
	require.Equal(t, openAIRoutingTestPreferredID, baseline,
		"用例前提失效：无路由时本应选 preferred 账号，当前锚点已失去区分度")
}

// requireSameAsNoRouting 断言"路由不生效"：同样的会话锚点下，带规则与不带规则
// 选出同一个账号。直接断言具体 ID 会依赖加权随机的取值，对比法只依赖种子一致。
func requireSameAsNoRouting(t *testing.T, advanced bool, group *Group, requestedModel string) {
	t.Helper()
	withRules := selectRoutingAccountID(t, openAIRoutingTestAccounts(), group, advanced,
		context.Background(), openAIRoutingStableSession, requestedModel, nil)
	without := selectRoutingAccountID(t, openAIRoutingTestAccounts(), openAIRoutingTestGroup(false, nil), advanced,
		context.Background(), openAIRoutingStableSession, requestedModel, nil)
	require.Equal(t, without, withRules, "路由未生效时选号必须与无规则时一致")
}

func forEachOpenAIScheduler(t *testing.T, run func(t *testing.T, advanced bool)) {
	t.Helper()
	for _, advanced := range []bool{false, true} {
		name := "legacy_scheduler"
		if advanced {
			name = "advanced_scheduler"
		}
		t.Run(name, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			run(t, advanced)
		})
	}
}

// 基线：没有路由规则时两种调度都能正常选号，且同一会话锚点下结果可复现。
// 具体选中谁由各自的排序/加权规则决定，用例不锁定。
func TestOpenAIModelRouting_BaselineWithoutRoutingIsStable(t *testing.T) {
	forEachOpenAIScheduler(t, func(t *testing.T, advanced bool) {
		first := selectRoutingAccountID(t, openAIRoutingTestAccounts(), openAIRoutingTestGroup(false, nil), advanced,
			context.Background(), openAIRoutingStableSession, openAIRoutingTestModel, nil)
		second := selectRoutingAccountID(t, openAIRoutingTestAccounts(), openAIRoutingTestGroup(false, nil), advanced,
			context.Background(), openAIRoutingStableSession, openAIRoutingTestModel, nil)
		require.Equal(t, first, second, "固定会话锚点下选号必须可复现")
		require.Contains(t, []int64{openAIRoutingTestRoutedID, openAIRoutingTestPreferredID}, first)
	})
}

// 路由指向低优先级账号时，必须压过普通优先级排序。
func TestOpenAIModelRouting_RoutedAccountBeatsPriority(t *testing.T) {
	forEachOpenAIScheduler(t, func(t *testing.T, advanced bool) {
		svc := newOpenAIModelRoutingTestService(
			openAIRoutingTestAccounts(),
			openAIRoutingTestGroup(true, map[string][]int64{
				openAIRoutingTestModel: {openAIRoutingTestRoutedID},
			}),
			advanced,
		)
		requireBaselinePicksPreferred(t, advanced, openAIRoutingTestModel, nil)
		selection, err := selectOpenAIRoutingTestAccount(t, svc, context.Background(), openAIRoutingStableSession, openAIRoutingTestModel, nil)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIRoutingTestRoutedID, selection.Account.ID,
			"路由命中时必须选路由账号，而不是普通调度的赢家")
	})
}

// 普通会话粘性不得压过有效路由：会话粘在路由集合之外的账号上时，
// 该粘性绑定必须让位。previous_response_id / guardian 绑定不在此列，另行覆盖。
func TestOpenAIModelRouting_RoutedAccountBeatsSessionSticky(t *testing.T) {
	forEachOpenAIScheduler(t, func(t *testing.T, advanced bool) {
		svc := newOpenAIModelRoutingTestService(
			openAIRoutingTestAccounts(),
			openAIRoutingTestGroup(true, map[string][]int64{
				openAIRoutingTestModel: {openAIRoutingTestRoutedID},
			}),
			advanced,
		)
		sessionHash := openAIRoutingStableSession
		cache := svc.cache.(*schedulerTestGatewayCache)
		require.NoError(t, cache.SetSessionAccountID(context.Background(), openAIRoutingTestGroupID, sessionHash, openAIRoutingTestPreferredID, 0))

		selection, err := selectOpenAIRoutingTestAccount(t, svc, context.Background(), sessionHash, openAIRoutingTestModel, nil)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIRoutingTestRoutedID, selection.Account.ID,
			"粘性绑定落在路由集合之外时不得压过路由")
	})
}

// 软优先语义：路由账号全部不可用时回落普通调度，而不是报无可用账号。
func TestOpenAIModelRouting_FallsBackWhenRoutedAccountsUnavailable(t *testing.T) {
	forEachOpenAIScheduler(t, func(t *testing.T, advanced bool) {
		svc := newOpenAIModelRoutingTestService(
			openAIRoutingTestAccounts(),
			openAIRoutingTestGroup(true, map[string][]int64{
				openAIRoutingTestModel: {openAIRoutingTestRoutedID},
			}),
			advanced,
		)
		excluded := map[int64]struct{}{openAIRoutingTestRoutedID: {}}
		selection, err := selectOpenAIRoutingTestAccount(t, svc, context.Background(), openAIRoutingStableSession, openAIRoutingTestModel, excluded)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIRoutingTestPreferredID, selection.Account.ID,
			"路由账号不可用必须回落普通调度")
	})
}

// 开关关闭时规则存在也不得生效。
func TestOpenAIModelRouting_DisabledRoutingKeepsNormalSelection(t *testing.T) {
	forEachOpenAIScheduler(t, func(t *testing.T, advanced bool) {
		requireSameAsNoRouting(t, advanced, openAIRoutingTestGroup(false, map[string][]int64{
			openAIRoutingTestModel: {openAIRoutingTestRoutedID},
		}), openAIRoutingTestModel)
	})
}

// 规则未命中当前模型时行为不变。
func TestOpenAIModelRouting_UnmatchedPatternKeepsNormalSelection(t *testing.T) {
	forEachOpenAIScheduler(t, func(t *testing.T, advanced bool) {
		requireSameAsNoRouting(t, advanced, openAIRoutingTestGroup(true, map[string][]int64{
			"some-other-model": {openAIRoutingTestRoutedID},
		}), openAIRoutingTestModel)
	})
}

// 末尾通配符规则同样适用于 OpenAI 系模型名。
func TestOpenAIModelRouting_WildcardPatternMatches(t *testing.T) {
	forEachOpenAIScheduler(t, func(t *testing.T, advanced bool) {
		svc := newOpenAIModelRoutingTestService(
			openAIRoutingTestAccounts(),
			openAIRoutingTestGroup(true, map[string][]int64{
				"gpt-5.6-*": {openAIRoutingTestRoutedID},
			}),
			advanced,
		)
		requireBaselinePicksPreferred(t, advanced, openAIRoutingTestModel, nil)
		selection, err := selectOpenAIRoutingTestAccount(t, svc, context.Background(), openAIRoutingStableSession, openAIRoutingTestModel, nil)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIRoutingTestRoutedID, selection.Account.ID)
	})
}

// 匹配阶段：管理员按客户端书写的公开别名配置规则，但 OpenAI 系 handler 传给调度的
// 是渠道映射后的 forwardModel。规则必须仍按公开别名命中，否则同一份配置在 Anthropic
// 分组（传原始 reqModel）和 OpenAI 分组上行为不一致。
func TestOpenAIModelRouting_MatchesPublicAliasNotChannelMappedModel(t *testing.T) {
	forEachOpenAIScheduler(t, func(t *testing.T, advanced bool) {
		svc := newOpenAIModelRoutingTestService(
			openAIRoutingTestAccounts(),
			openAIRoutingTestGroup(true, map[string][]int64{
				openAIRoutingTestModel: {openAIRoutingTestRoutedID},
			}),
			advanced,
		)
		// 渠道映射把公开别名改写成了另一个上游模型名，调度收到的是后者。
		ctx := WithCompositeRouteDecision(context.Background(), CompositeRouteDecision{
			Matched:        true,
			PublicModel:    openAIRoutingTestModel,
			TargetPlatform: PlatformOpenAI,
			UpstreamModel:  "gpt-5.1-upstream",
		})
		requireBaselinePicksPreferred(t, advanced, "gpt-5.1-upstream", nil)
		selection, err := selectOpenAIRoutingTestAccount(t, svc, ctx, openAIRoutingStableSession, "gpt-5.1-upstream", nil)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIRoutingTestRoutedID, selection.Account.ID,
			"路由规则必须按公开别名匹配，而不是渠道映射后的模型名")
	})
}

// 非 composite 请求同样按公开别名匹配：OpenAI 系 handler 在做渠道映射之前用
// WithRequestedPublicModel 记录客户端书写的模型名，调度收到的则是映射后的上游
// 模型名。少了这一步，配了渠道映射的分组会出现"规则写了却从不命中"。
func TestOpenAIModelRouting_MatchesPublicAliasFromPlainChannelMapping(t *testing.T) {
	forEachOpenAIScheduler(t, func(t *testing.T, advanced bool) {
		svc := newOpenAIModelRoutingTestService(
			openAIRoutingTestAccounts(),
			openAIRoutingTestGroup(true, map[string][]int64{
				openAIRoutingTestModel: {openAIRoutingTestRoutedID},
			}),
			advanced,
		)
		requireBaselinePicksPreferred(t, advanced, "gpt-5.1-upstream", nil)
		ctx := WithRequestedPublicModel(context.Background(), openAIRoutingTestModel)
		selection, err := selectOpenAIRoutingTestAccount(t, svc, ctx, openAIRoutingStableSession, "gpt-5.1-upstream", nil)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIRoutingTestRoutedID, selection.Account.ID,
			"渠道映射前记录的公开别名必须作为路由匹配依据")
	})
}

// StickyWeighted 模式下普通粘性不再走 selectBySessionHash，而是由加权顺序把绑定
// 账号提到候选队首。候选池本身已按路由收敛，绑定账号落在集合外时自然排不进来。
// 该模式下绕开候选过滤的兜底路径由 WeightedStickyFallbackRespectsRouting 单独覆盖。
func TestOpenAIModelRouting_RoutedAccountBeatsWeightedSticky(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	svc := newOpenAIModelRoutingTestService(
		openAIRoutingTestAccounts(),
		openAIRoutingTestGroup(true, map[string][]int64{
			openAIRoutingTestModel: {openAIRoutingTestRoutedID},
		}),
		true,
	)
	svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true", "true")

	cache := svc.cache.(*schedulerTestGatewayCache)
	require.NoError(t, cache.SetSessionAccountID(context.Background(), openAIRoutingTestGroupID,
		openAIRoutingStableSession, openAIRoutingTestPreferredID, 0))

	selection, err := selectOpenAIRoutingTestAccount(t, svc, context.Background(),
		openAIRoutingStableSession, openAIRoutingTestModel, nil)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.NotNil(t, selection.Account)
	require.Equal(t, openAIRoutingTestRoutedID, selection.Account.ID,
		"加权粘性模式下路由同样不得被普通粘性压过")
}

// tryFallbackToWeightedSticky 是负载均衡把候选全部试完仍未拿到槽位后的兜底：它直接
// 按绑定取账号，绕开候选过滤，因此必须自行遵守路由。这里直接驱动该函数，因为经完整
// 调度很难稳定构造"候选全部占满"的前置条件。
func TestOpenAIModelRouting_WeightedStickyFallbackRespectsRouting(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	svc := newOpenAIModelRoutingTestService(
		openAIRoutingTestAccounts(),
		openAIRoutingTestGroup(true, map[string][]int64{
			openAIRoutingTestModel: {openAIRoutingTestRoutedID},
		}),
		true,
	)
	scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
	groupID := openAIRoutingTestGroupID
	baseReq := OpenAIAccountScheduleRequest{
		GroupID:            &groupID,
		Platform:           PlatformOpenAI,
		SessionHash:        openAIRoutingStableSession,
		StickyWeighted:     true,
		RequestedModel:     openAIRoutingTestModel,
		RequiredTransport:  OpenAIUpstreamTransportAny,
		RequiredCapability: OpenAIEndpointCapabilityResponses,
		RoutedAccountIDs:   []int64{openAIRoutingTestRoutedID},
	}

	t.Run("plain sticky outside the routed set is skipped", func(t *testing.T) {
		req := baseReq
		req.StickyAccountID = openAIRoutingTestPreferredID
		selection, err := scheduler.tryFallbackToWeightedSticky(context.Background(), req)
		require.NoError(t, err)
		require.Nil(t, selection, "普通粘性落在路由集合之外时不得由兜底选中")
	})

	t.Run("previous_response binding stays exempt", func(t *testing.T) {
		req := baseReq
		req.StickyPreviousAccountID = openAIRoutingTestPreferredID
		selection, err := scheduler.tryFallbackToWeightedSticky(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIRoutingTestPreferredID, selection.Account.ID,
			"previous_response 绑定是续话约束，不受路由限制")
	})

	t.Run("sticky inside the routed set is still honoured", func(t *testing.T) {
		req := baseReq
		req.StickyAccountID = openAIRoutingTestRoutedID
		selection, err := scheduler.tryFallbackToWeightedSticky(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.NotNil(t, selection.Account)
		require.Equal(t, openAIRoutingTestRoutedID, selection.Account.ID)
	})
}
