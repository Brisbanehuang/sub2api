//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type generationAwareBillingCache interface {
	service.BillingCache
	GetUserBalanceGeneration(ctx context.Context, userID int64) (int64, error)
	SetUserBalanceIfGeneration(ctx context.Context, userID int64, balance float64, generation int64) (bool, error)
	GetSubscriptionCacheGeneration(ctx context.Context, userID, groupID int64) (int64, error)
	SetSubscriptionCacheIfGeneration(ctx context.Context, userID, groupID int64, data *service.SubscriptionCacheData, generation int64) (bool, error)
}

type BillingCacheGenerationSuite struct {
	IntegrationRedisSuite
}

func (s *BillingCacheGenerationSuite) TestBalanceGenerationRejectsStaleFill() {
	ctx := context.Background()
	cache, ok := NewBillingCache(testRedis(s.T())).(generationAwareBillingCache)
	require.True(s.T(), ok, "billing cache must expose generation-aware balance writes")

	generation, err := cache.GetUserBalanceGeneration(ctx, 501)
	require.NoError(s.T(), err)
	require.Zero(s.T(), generation)

	written, err := cache.SetUserBalanceIfGeneration(ctx, 501, 12.34, generation)
	require.NoError(s.T(), err)
	require.True(s.T(), written)
	require.NoError(s.T(), cache.InvalidateUserBalance(ctx, 501))

	currentGeneration, err := cache.GetUserBalanceGeneration(ctx, 501)
	require.NoError(s.T(), err)
	require.Equal(s.T(), generation+1, currentGeneration)

	written, err = cache.SetUserBalanceIfGeneration(ctx, 501, 12.34, generation)
	require.NoError(s.T(), err)
	require.False(s.T(), written)
	_, err = cache.GetUserBalance(ctx, 501)
	require.ErrorIs(s.T(), err, redis.Nil)

	written, err = cache.SetUserBalanceIfGeneration(ctx, 501, 56.78, currentGeneration)
	require.NoError(s.T(), err)
	require.True(s.T(), written)
	balance, err := cache.GetUserBalance(ctx, 501)
	require.NoError(s.T(), err)
	require.Equal(s.T(), 56.78, balance)
}

func (s *BillingCacheGenerationSuite) TestSubscriptionGenerationRejectsStaleFill() {
	ctx := context.Background()
	cache, ok := NewBillingCache(testRedis(s.T())).(generationAwareBillingCache)
	require.True(s.T(), ok, "billing cache must expose generation-aware subscription writes")

	generation, err := cache.GetSubscriptionCacheGeneration(ctx, 502, 601)
	require.NoError(s.T(), err)
	require.Zero(s.T(), generation)
	oldData := &service.SubscriptionCacheData{
		Status:    "active",
		ExpiresAt: time.Now().Add(time.Hour),
		Version:   1,
	}

	written, err := cache.SetSubscriptionCacheIfGeneration(ctx, 502, 601, oldData, generation)
	require.NoError(s.T(), err)
	require.True(s.T(), written)
	require.NoError(s.T(), cache.InvalidateSubscriptionCache(ctx, 502, 601))

	currentGeneration, err := cache.GetSubscriptionCacheGeneration(ctx, 502, 601)
	require.NoError(s.T(), err)
	require.Equal(s.T(), generation+1, currentGeneration)

	written, err = cache.SetSubscriptionCacheIfGeneration(ctx, 502, 601, oldData, generation)
	require.NoError(s.T(), err)
	require.False(s.T(), written)
	_, err = cache.GetSubscriptionCache(ctx, 502, 601)
	require.ErrorIs(s.T(), err, redis.Nil)

	newData := &service.SubscriptionCacheData{
		Status:    "active",
		ExpiresAt: time.Now().Add(48 * time.Hour),
		Version:   2,
	}
	written, err = cache.SetSubscriptionCacheIfGeneration(ctx, 502, 601, newData, currentGeneration)
	require.NoError(s.T(), err)
	require.True(s.T(), written)
	loaded, err := cache.GetSubscriptionCache(ctx, 502, 601)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(2), loaded.Version)
}

func TestBillingCacheGenerationSuite(t *testing.T) {
	suite.Run(t, new(BillingCacheGenerationSuite))
}
