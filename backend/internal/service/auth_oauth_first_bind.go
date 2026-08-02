package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"

	entsql "entgo.io/ent/dialect/sql"
)

type ProviderDefaultCacheInvalidationPlan struct {
	UserID               int64
	InvalidateBalance    bool
	SubscriptionGroupIDs []int64
}

func (p ProviderDefaultCacheInvalidationPlan) Empty() bool {
	return !p.InvalidateBalance && len(p.SubscriptionGroupIDs) == 0
}

func (s *AuthService) ApplyProviderDefaultSettingsOnFirstBind(ctx context.Context, userID int64, providerType string) error {
	if s == nil || s.entClient == nil || s.settingService == nil || userID <= 0 {
		return nil
	}
	if dbent.TxFromContext(ctx) != nil {
		return fmt.Errorf("first bind defaults in an outer transaction require deferred cache invalidation")
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin first bind defaults transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	plan, err := s.ApplyProviderDefaultSettingsOnFirstBindDeferred(dbent.NewTxContext(ctx, tx), userID, providerType)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.InvalidateProviderDefaultSettingsCaches(plan)
	return nil
}

// ApplyProviderDefaultSettingsOnFirstBindDeferred applies the grant inside the
// caller's transaction. Its immutable plan must run only after that transaction commits.
func (s *AuthService) ApplyProviderDefaultSettingsOnFirstBindDeferred(
	ctx context.Context,
	userID int64,
	providerType string,
) (ProviderDefaultCacheInvalidationPlan, error) {
	plan := ProviderDefaultCacheInvalidationPlan{UserID: userID}
	if s == nil || s.entClient == nil || s.settingService == nil || userID <= 0 {
		return plan, nil
	}
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return plan, fmt.Errorf("deferred first bind defaults require a transaction")
	}

	providerDefaults, enabled, err := s.settingService.ResolveAuthSourceGrantSettings(ctx, providerType, true)
	if err != nil {
		return plan, fmt.Errorf("load auth source defaults: %w", err)
	}
	if !enabled {
		return plan, nil
	}

	var deferred providerDefaultDeferredAssigner
	if len(providerDefaults.Subscriptions) > 0 {
		deferred, _ = s.defaultSubAssigner.(providerDefaultDeferredAssigner)
		if deferred == nil {
			return plan, fmt.Errorf("first bind subscription defaults require deferred subscription assignment")
		}
	}

	client := tx.Client()
	var result entsql.Result
	if err := client.Driver().Exec(
		ctx,
		`INSERT INTO user_provider_default_grants (user_id, provider_type, grant_reason)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, provider_type, grant_reason) DO NOTHING`,
		[]any{userID, strings.TrimSpace(providerType), "first_bind"},
		&result,
	); err != nil {
		return plan, fmt.Errorf("record first bind provider grant: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return plan, fmt.Errorf("read first bind provider grant result: %w", err)
	}
	if affected == 0 {
		return plan, nil
	}

	if providerDefaults.Balance != 0 {
		if err := client.User.UpdateOneID(userID).AddBalance(providerDefaults.Balance).Exec(ctx); err != nil {
			return plan, fmt.Errorf("apply first bind balance default: %w", err)
		}
		plan.InvalidateBalance = true
	}
	if providerDefaults.Concurrency != 0 {
		if err := client.User.UpdateOneID(userID).AddConcurrency(providerDefaults.Concurrency).Exec(ctx); err != nil {
			return plan, fmt.Errorf("apply first bind concurrency default: %w", err)
		}
	}
	for _, item := range providerDefaults.Subscriptions {
		if _, _, err := deferred.AssignOrExtendSubscriptionDeferred(ctx, &AssignSubscriptionInput{
			UserID:       userID,
			GroupID:      item.GroupID,
			ValidityDays: item.ValidityDays,
			Notes:        "auto assigned by first bind defaults",
		}); err != nil {
			return plan, fmt.Errorf("apply first bind subscription default: %w", err)
		}
		plan.SubscriptionGroupIDs = appendUniqueGroupID(plan.SubscriptionGroupIDs, item.GroupID)
	}
	return plan, nil
}

func appendUniqueGroupID(groupIDs []int64, groupID int64) []int64 {
	for _, existing := range groupIDs {
		if existing == groupID {
			return groupIDs
		}
	}
	return append(groupIDs, groupID)
}

type providerDefaultDeferredAssigner interface {
	AssignOrExtendSubscriptionDeferred(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error)
	InvalidateProviderDefaultBalanceCache(ctx context.Context, userID int64) error
	InvalidateProviderDefaultSubscriptionCache(ctx context.Context, userID, groupID int64) error
}

func (s *AuthService) InvalidateProviderDefaultSettingsCaches(plan ProviderDefaultCacheInvalidationPlan) {
	if s == nil || s.defaultSubAssigner == nil || plan.UserID <= 0 || plan.Empty() {
		return
	}
	manager, ok := s.defaultSubAssigner.(providerDefaultDeferredAssigner)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if plan.InvalidateBalance {
		if err := manager.InvalidateProviderDefaultBalanceCache(ctx, plan.UserID); err != nil {
			logger.LegacyPrintf("service.auth", "first bind balance cache invalidation failed: user_id=%d err=%v", plan.UserID, err)
		}
	}
	for _, groupID := range plan.SubscriptionGroupIDs {
		if err := manager.InvalidateProviderDefaultSubscriptionCache(ctx, plan.UserID, groupID); err != nil {
			logger.LegacyPrintf("service.auth", "first bind subscription cache invalidation failed: user_id=%d group_id=%d err=%v", plan.UserID, groupID, err)
		}
	}
}
