//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type generationBarrierCacheStub struct {
	BillingCache

	mu sync.Mutex

	balanceGeneration      int64
	balanceCached          bool
	balance                float64
	balanceWriteStarted    chan struct{}
	balanceWriteRelease    chan struct{}
	balanceWriteDone       chan struct{}
	balanceWriteStartedOne sync.Once
	balanceWriteDoneOne    sync.Once

	subscriptionGeneration      int64
	subscriptionCached          bool
	subscription                *SubscriptionCacheData
	subscriptionWriteStarted    chan struct{}
	subscriptionWriteRelease    chan struct{}
	subscriptionWriteDone       chan struct{}
	subscriptionWriteStartedOne sync.Once
	subscriptionWriteDoneOne    sync.Once
}

func newGenerationBarrierCacheStub() *generationBarrierCacheStub {
	return &generationBarrierCacheStub{
		balanceWriteStarted:      make(chan struct{}),
		balanceWriteRelease:      make(chan struct{}),
		balanceWriteDone:         make(chan struct{}),
		subscriptionWriteStarted: make(chan struct{}),
		subscriptionWriteRelease: make(chan struct{}),
		subscriptionWriteDone:    make(chan struct{}),
	}
}

func (c *generationBarrierCacheStub) GetUserBalance(context.Context, int64) (float64, error) {
	return 0, errors.New("cache miss")
}

func (c *generationBarrierCacheStub) SetUserBalance(_ context.Context, _ int64, balance float64) error {
	c.balanceWriteStartedOne.Do(func() { close(c.balanceWriteStarted) })
	<-c.balanceWriteRelease
	c.mu.Lock()
	c.balance = balance
	c.balanceCached = true
	c.mu.Unlock()
	c.balanceWriteDoneOne.Do(func() { close(c.balanceWriteDone) })
	return nil
}

func (c *generationBarrierCacheStub) GetUserBalanceGeneration(context.Context, int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.balanceGeneration, nil
}

func (c *generationBarrierCacheStub) SetUserBalanceIfGeneration(_ context.Context, _ int64, balance float64, generation int64) (bool, error) {
	c.balanceWriteStartedOne.Do(func() { close(c.balanceWriteStarted) })
	<-c.balanceWriteRelease
	c.mu.Lock()
	written := c.balanceGeneration == generation
	if written {
		c.balance = balance
		c.balanceCached = true
	}
	c.mu.Unlock()
	c.balanceWriteDoneOne.Do(func() { close(c.balanceWriteDone) })
	return written, nil
}

func (c *generationBarrierCacheStub) InvalidateUserBalance(context.Context, int64) error {
	c.mu.Lock()
	c.balanceGeneration++
	c.balanceCached = false
	c.mu.Unlock()
	return nil
}

func (c *generationBarrierCacheStub) GetSubscriptionCache(context.Context, int64, int64) (*SubscriptionCacheData, error) {
	return nil, errors.New("cache miss")
}

func (c *generationBarrierCacheStub) SetSubscriptionCache(_ context.Context, _, _ int64, data *SubscriptionCacheData) error {
	c.subscriptionWriteStartedOne.Do(func() { close(c.subscriptionWriteStarted) })
	<-c.subscriptionWriteRelease
	c.mu.Lock()
	c.subscription = data
	c.subscriptionCached = true
	c.mu.Unlock()
	c.subscriptionWriteDoneOne.Do(func() { close(c.subscriptionWriteDone) })
	return nil
}

func (c *generationBarrierCacheStub) GetSubscriptionCacheGeneration(context.Context, int64, int64) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subscriptionGeneration, nil
}

func (c *generationBarrierCacheStub) SetSubscriptionCacheIfGeneration(_ context.Context, _, _ int64, data *SubscriptionCacheData, generation int64) (bool, error) {
	c.subscriptionWriteStartedOne.Do(func() { close(c.subscriptionWriteStarted) })
	<-c.subscriptionWriteRelease
	c.mu.Lock()
	written := c.subscriptionGeneration == generation
	if written {
		c.subscription = data
		c.subscriptionCached = true
	}
	c.mu.Unlock()
	c.subscriptionWriteDoneOne.Do(func() { close(c.subscriptionWriteDone) })
	return written, nil
}

func (c *generationBarrierCacheStub) InvalidateSubscriptionCache(context.Context, int64, int64) error {
	c.mu.Lock()
	c.subscriptionGeneration++
	c.subscriptionCached = false
	c.mu.Unlock()
	return nil
}

func (c *generationBarrierCacheStub) hasBalance() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.balanceCached
}

func (c *generationBarrierCacheStub) hasSubscription() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.subscriptionCached
}

type generationBarrierUserRepo struct {
	UserRepository
	balance float64
}

func (r *generationBarrierUserRepo) GetByID(context.Context, int64) (*User, error) {
	return &User{ID: 42, Balance: r.balance}, nil
}

type generationBarrierSubscriptionRepo struct {
	UserSubscriptionRepository
	subscription *UserSubscription
}

func (r *generationBarrierSubscriptionRepo) GetActiveByUserIDAndGroupID(context.Context, int64, int64) (*UserSubscription, error) {
	return r.subscription, nil
}

func TestBillingCacheServiceBalanceFillDoesNotOverwriteLaterInvalidation(t *testing.T) {
	cache := newGenerationBarrierCacheStub()
	svc := NewBillingCacheService(cache, &generationBarrierUserRepo{balance: 12.34}, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)

	balance, err := svc.GetUserBalance(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, 12.34, balance)
	requireReceive(t, cache.balanceWriteStarted)

	require.NoError(t, svc.InvalidateUserBalance(context.Background(), 42))
	close(cache.balanceWriteRelease)
	requireReceive(t, cache.balanceWriteDone)
	require.False(t, cache.hasBalance(), "an invalidated balance must not be repopulated by an older queued fill")
}

func TestBillingCacheServiceSubscriptionFillDoesNotOverwriteLaterInvalidation(t *testing.T) {
	cache := newGenerationBarrierCacheStub()
	sub := &UserSubscription{
		UserID:    42,
		GroupID:   7,
		Status:    SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	svc := NewBillingCacheService(cache, nil, &generationBarrierSubscriptionRepo{subscription: sub}, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)

	loaded, err := svc.GetSubscriptionStatus(context.Background(), 42, 7)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusActive, loaded.Status)
	requireReceive(t, cache.subscriptionWriteStarted)

	require.NoError(t, svc.InvalidateSubscription(context.Background(), 42, 7))
	close(cache.subscriptionWriteRelease)
	requireReceive(t, cache.subscriptionWriteDone)
	require.False(t, cache.hasSubscription(), "an invalidated subscription must not be repopulated by an older queued fill")
}

func TestBillingCacheServiceLegacyBalanceCacheSkipsReadThroughFill(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	svc := NewBillingCacheService(cache, &generationBarrierUserRepo{balance: 12.34}, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)

	balance, err := svc.GetUserBalance(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, 12.34, balance)
	require.Never(t, func() bool {
		return atomic.LoadInt64(&cache.balanceUpdates) != 0
	}, 100*time.Millisecond, 10*time.Millisecond)
}

func TestBillingCacheServiceLegacySubscriptionCacheSkipsReadThroughFill(t *testing.T) {
	cache := &billingCacheWorkerStub{}
	sub := &UserSubscription{
		UserID:    42,
		GroupID:   7,
		Status:    SubscriptionStatusActive,
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	svc := NewBillingCacheService(cache, nil, &generationBarrierSubscriptionRepo{subscription: sub}, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)

	loaded, err := svc.GetSubscriptionStatus(context.Background(), 42, 7)
	require.NoError(t, err)
	require.Equal(t, SubscriptionStatusActive, loaded.Status)
	require.Never(t, func() bool {
		return atomic.LoadInt64(&cache.subscriptionUpdates) != 0
	}, 100*time.Millisecond, 10*time.Millisecond)
}

func requireReceive(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cache worker")
	}
}
