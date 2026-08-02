//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func TestPaymentServiceCreateOrderBalancePaySubscriptionCompletesAndDeductsBalance(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)

	resp, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "balance-pay-completes-key",
	})

	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, resp.Status)
	require.Equal(t, string(payment.TypeBalancePay), resp.PaymentType)
	require.InEpsilon(t, 35.90, resp.PayAmount, 0.0001)

	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InEpsilon(t, 64.10, userAfter.Balance, 0.0001)

	order := fixture.onlyOrder(t, ctx, resp.OrderID)
	require.Equal(t, string(payment.TypeBalancePay), order.PaymentType)
	require.Equal(t, payment.OrderTypeSubscription, order.OrderType)
	require.Equal(t, OrderStatusCompleted, order.Status)
	require.InEpsilon(t, 35.90, order.Amount, 0.0001)
	require.InEpsilon(t, 35.90, order.PayAmount, 0.0001)
	require.Zero(t, order.FeeRate)

	activeSub, err := fixture.client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userEnt.ID),
			usersubscription.GroupIDEQ(groupEnt.ID),
			usersubscription.StatusEQ(SubscriptionStatusActive),
		).
		Only(ctx)
	require.NoError(t, err)
	require.True(t, activeSub.ExpiresAt.After(time.Now()))
}

func TestPaymentServiceCreateOrderBalancePayRequiresDurableIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)

	response, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:      userEnt.ID,
		PaymentType: payment.TypeBalancePay,
		OrderType:   payment.OrderTypeSubscription,
		PlanID:      plan.ID,
	})

	require.Nil(t, response)
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", infraerrors.FromError(err).Reason)
	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.Equal(t, 100.0, userAfter.Balance)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, orderCount)
}

func TestPaymentServiceCreateOrderBalancePayRequiresDurableKeyBeforePlanValidation(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)

	response, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:      userEnt.ID,
		PaymentType: payment.TypeBalancePay,
		OrderType:   payment.OrderTypeSubscription,
		PlanID:      999999,
	})

	require.Nil(t, response)
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", infraerrors.FromError(err).Reason)
}

func TestPaymentServiceCreateOrderBalancePayReplaysDurableOrderWithoutRepeatingPurchase(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	initialExpiresAt := time.Now().UTC().Truncate(time.Second).Add(10 * 24 * time.Hour)
	require.NoError(t, fixture.paymentSvc.subscriptionSvc.userSubRepo.Create(ctx, &UserSubscription{
		UserID:     userEnt.ID,
		GroupID:    groupEnt.ID,
		StartsAt:   time.Now().Add(-24 * time.Hour),
		ExpiresAt:  initialExpiresAt,
		Status:     SubscriptionStatusActive,
		AssignedAt: time.Now().Add(-24 * time.Hour),
		Notes:      "existing subscription",
	}))
	req := CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "durable-balance-pay-replay-key",
	}

	first, err := fixture.paymentSvc.CreateOrder(ctx, req)
	require.NoError(t, err)
	second, err := fixture.paymentSvc.CreateOrder(ctx, req)
	require.NoError(t, err)

	require.Equal(t, first, second)
	require.Len(t, first.OutTradeNo, 64)
	require.Regexp(t, `^[0-9a-f]{64}$`, first.OutTradeNo)
	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, userAfter.Balance, 0.0001)
	orders, err := fixture.client.PaymentOrder.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, orders, 1)
	subscription, err := fixture.client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userEnt.ID), usersubscription.GroupIDEQ(groupEnt.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.WithinDuration(t, initialExpiresAt.AddDate(0, 0, 30), subscription.ExpiresAt, time.Second)
}

func TestPaymentServiceCreateOrderBalancePayRejectsDurableKeyReusedForDifferentPlan(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	firstPlan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	secondPlan := fixture.createPlan(t, ctx, groupEnt.ID, 20)
	request := CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         firstPlan.ID,
		IdempotencyKey: "durable-balance-pay-conflict-key",
	}

	first, err := fixture.paymentSvc.CreateOrder(ctx, request)
	require.NoError(t, err)
	request.PlanID = secondPlan.ID
	conflict, err := fixture.paymentSvc.CreateOrder(ctx, request)

	require.Nil(t, conflict)
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", infraerrors.FromError(err).Reason)
	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, userAfter.Balance, 0.0001)
	orders, err := fixture.client.PaymentOrder.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, first.OrderID, orders[0].ID)
	subscription, err := fixture.client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userEnt.ID), usersubscription.GroupIDEQ(groupEnt.ID)).
		Only(ctx)
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().AddDate(0, 0, 30), subscription.ExpiresAt, 2*time.Second)
}

func TestPaymentServiceCreateOrderBalancePayRejectsDurableKeyReusedForDifferentCanonicalPayload(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CreateOrderRequest)
	}{
		{name: "amount", mutate: func(req *CreateOrderRequest) { req.Amount = 12.34 }},
		{name: "openid", mutate: func(req *CreateOrderRequest) { req.OpenID = "different-openid" }},
		{name: "return_url", mutate: func(req *CreateOrderRequest) { req.ReturnURL = "https://example.com/other" }},
		{name: "payment_source", mutate: func(req *CreateOrderRequest) { req.PaymentSource = "different-source" }},
		{name: "is_mobile", mutate: func(req *CreateOrderRequest) { req.IsMobile = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newBalancePayFixture(t, 1)
			userEnt := fixture.createUser(t, ctx, 100)
			groupEnt := fixture.createSubscriptionGroup(t, ctx)
			plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
			request := CreateOrderRequest{
				UserID:         userEnt.ID,
				Amount:         0,
				PaymentType:    payment.TypeBalancePay,
				OpenID:         "initial-openid",
				IsMobile:       false,
				ReturnURL:      "https://example.com/initial",
				PaymentSource:  "initial-source",
				OrderType:      payment.OrderTypeSubscription,
				PlanID:         plan.ID,
				IdempotencyKey: "durable-balance-pay-canonical-conflict-key",
			}

			first, err := fixture.paymentSvc.CreateOrder(ctx, request)
			require.NoError(t, err)
			tt.mutate(&request)
			conflict, err := fixture.paymentSvc.CreateOrder(ctx, request)

			require.Nil(t, conflict)
			require.Error(t, err)
			require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", infraerrors.FromError(err).Reason)
			userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
			require.NoError(t, err)
			require.InDelta(t, 64.10, userAfter.Balance, 0.0001)
			orders, err := fixture.client.PaymentOrder.Query().All(ctx)
			require.NoError(t, err)
			require.Len(t, orders, 1)
			require.Equal(t, first.OrderID, orders[0].ID)
			subscriptionCount, err := fixture.client.UserSubscription.Query().
				Where(usersubscription.UserIDEQ(userEnt.ID), usersubscription.GroupIDEQ(groupEnt.ID)).
				Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, subscriptionCount)
		})
	}
}

func TestPaymentServiceCreateOrderBalancePayRecoversDurableReplayAfterUniqueInsertConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	request := CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		ReturnURL:      "https://example.com/complete",
		PaymentSource:  "checkout",
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "durable-balance-pay-unique-recovery-key",
	}
	existing := seedCompletedBalancePayOrder(t, ctx, fixture, userEnt, groupEnt.ID, plan, request)
	hideNextPaymentOrderQueries(fixture.client, 2)

	replayed, err := fixture.paymentSvc.CreateOrder(ctx, request)

	require.NoError(t, err)
	require.NotNil(t, replayed)
	require.Equal(t, existing.ID, replayed.OrderID)
	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 100, userAfter.Balance, 0.0001)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, orderCount)
	subscriptionCount, err := fixture.client.UserSubscription.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, subscriptionCount)
	auditCount, err := fixture.client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, auditCount)
}

func TestPaymentServiceCreateOrderBalancePayRecoversDurableConflictAfterUniqueInsertConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	originalRequest := CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		ReturnURL:      "https://example.com/complete",
		PaymentSource:  "checkout",
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "durable-balance-pay-unique-conflict-key",
	}
	seedCompletedBalancePayOrder(t, ctx, fixture, userEnt, groupEnt.ID, plan, originalRequest)
	hideNextPaymentOrderQueries(fixture.client, 2)
	conflictingRequest := originalRequest
	conflictingRequest.ReturnURL = "https://example.com/other"

	response, err := fixture.paymentSvc.CreateOrder(ctx, conflictingRequest)

	require.Nil(t, response)
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", infraerrors.Reason(err))
	require.NotContains(t, strings.ToLower(err.Error()), "unique constraint")
	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 100, userAfter.Balance, 0.0001)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, orderCount)
	subscriptionCount, err := fixture.client.UserSubscription.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, subscriptionCount)
	auditCount, err := fixture.client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, auditCount)
}

func seedCompletedBalancePayOrder(
	t *testing.T,
	ctx context.Context,
	fixture *balancePayFixture,
	user *dbent.User,
	groupID int64,
	plan *dbent.SubscriptionPlan,
	request CreateOrderRequest,
) *dbent.PaymentOrder {
	t.Helper()
	requestFingerprint, err := buildBalancePayRequestFingerprint(request)
	require.NoError(t, err)
	now := time.Now()
	order, err := fixture.client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(plan.Price).
		SetPayAmount(plan.Price).
		SetFeeRate(0).
		SetRechargeCode("PAY-RECOVERED").
		SetOutTradeNo(deriveBalancePayOutTradeNo(user.ID, request.IdempotencyKey)).
		SetPaymentType(payment.TypeBalancePay).
		SetPaymentTradeNo("balance").
		SetOrderType(payment.OrderTypeSubscription).
		SetPlanID(plan.ID).
		SetSubscriptionGroupID(groupID).
		SetSubscriptionDays(30).
		SetProviderSnapshot(map[string]any{
			balancePayRequestSchemaSnapshotKey:      1,
			balancePayRequestFingerprintSnapshotKey: requestFingerprint,
		}).
		SetStatus(OrderStatusCompleted).
		SetPaidAt(now).
		SetCompletedAt(now).
		SetExpiresAt(now.Add(30 * time.Minute)).
		SetClientIP("").
		SetSrcHost("").
		Save(ctx)
	require.NoError(t, err)
	return order
}

func hideNextPaymentOrderQueries(client *dbent.Client, count int64) {
	var hiddenQueries atomic.Int64
	client.PaymentOrder.Intercept(dbent.InterceptFunc(func(next dbent.Querier) dbent.Querier {
		return dbent.QuerierFunc(func(ctx context.Context, query dbent.Query) (dbent.Value, error) {
			if _, ok := query.(*dbent.PaymentOrderQuery); ok && hiddenQueries.Add(1) <= count {
				return nil, &dbent.NotFoundError{}
			}
			return next.Query(ctx, query)
		})
	}))
}

func TestPaymentServiceCreateOrderBalancePayConcurrentSameKeySamePayloadReplaysDeterministically(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	request := CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		ReturnURL:      "https://example.com/complete",
		PaymentSource:  "checkout",
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "concurrent-durable-same-payload-key",
	}

	start := make(chan struct{})
	results := make(chan struct {
		response *CreateOrderResponse
		err      error
	}, 2)
	for range 2 {
		go func() {
			<-start
			response, err := fixture.paymentSvc.CreateOrder(ctx, request)
			results <- struct {
				response *CreateOrderResponse
				err      error
			}{response: response, err: err}
		}()
	}
	close(start)

	responses := make([]*CreateOrderResponse, 0, 2)
	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		require.NotNil(t, result.response)
		responses = append(responses, result.response)
	}
	require.Equal(t, responses[0], responses[1])

	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, userAfter.Balance, 0.0001)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, orderCount)
	subscriptionCount, err := fixture.client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userEnt.ID), usersubscription.GroupIDEQ(groupEnt.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, subscriptionCount)
	auditCount, err := fixture.client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, auditCount)
}

func TestPaymentServiceCreateOrderBalancePayConcurrentSameKeyDifferentPayloadReturnsConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	firstRequest := CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		ReturnURL:      "https://example.com/complete",
		PaymentSource:  "checkout",
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "concurrent-durable-different-payload-key",
	}
	secondRequest := firstRequest
	secondRequest.ReturnURL = "https://example.com/other"

	start := make(chan struct{})
	results := make(chan struct {
		response *CreateOrderResponse
		err      error
	}, 2)
	for _, request := range []CreateOrderRequest{firstRequest, secondRequest} {
		request := request
		go func() {
			<-start
			response, err := fixture.paymentSvc.CreateOrder(ctx, request)
			results <- struct {
				response *CreateOrderResponse
				err      error
			}{response: response, err: err}
		}()
	}
	close(start)

	var succeeded, conflicts int
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
			require.NotNil(t, result.response)
		case infraerrors.Reason(result.err) == "IDEMPOTENCY_KEY_CONFLICT":
			conflicts++
			require.Nil(t, result.response)
		default:
			require.NoError(t, result.err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, conflicts)

	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, userAfter.Balance, 0.0001)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, orderCount)
	subscriptionCount, err := fixture.client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userEnt.ID), usersubscription.GroupIDEQ(groupEnt.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, subscriptionCount)
	auditCount, err := fixture.client.PaymentAuditLog.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, auditCount)
}

func TestPaymentServiceCreateOrderBalancePayReplaysCompletedOrderAfterPlanIsUnavailable(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	request := CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "durable-balance-pay-unavailable-plan-key",
	}

	first, err := fixture.paymentSvc.CreateOrder(ctx, request)
	require.NoError(t, err)
	_, err = fixture.client.SubscriptionPlan.UpdateOneID(plan.ID).SetForSale(false).Save(ctx)
	require.NoError(t, err)
	replayed, err := fixture.paymentSvc.CreateOrder(ctx, request)

	require.NoError(t, err)
	require.Equal(t, first, replayed)
	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, userAfter.Balance, 0.0001)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, orderCount)
}

func TestPaymentServiceCreateOrderBalancePayRejectsDurableKeyBeforeDifferentPlanValidation(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	request := CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "durable-balance-pay-invalid-plan-conflict-key",
	}

	_, err := fixture.paymentSvc.CreateOrder(ctx, request)
	require.NoError(t, err)
	request.PlanID = 999999
	response, err := fixture.paymentSvc.CreateOrder(ctx, request)

	require.Nil(t, response)
	require.Error(t, err)
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", infraerrors.FromError(err).Reason)
	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, userAfter.Balance, 0.0001)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, orderCount)
}

func TestPaymentServiceCreateOrderBalancePayRoundsRequiredBalanceUpToOneCent(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 0.01)
	userEnt := fixture.createUser(t, ctx, 1)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 0.49)

	response, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "durable-balance-pay-round-up-key",
	})

	require.NoError(t, err)
	require.Equal(t, 0.01, response.PayAmount)
	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.Equal(t, 0.99, userAfter.Balance)
	order := fixture.onlyOrder(t, ctx, response.OrderID)
	require.Equal(t, 0.01, order.PayAmount)
}

func TestPaymentServiceCreateOrderBalancePayRejectsNonPositiveRequiredBalance(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 1)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 0)

	response, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "durable-balance-pay-zero-amount-key",
	})

	require.Nil(t, response)
	require.Error(t, err)
	require.Equal(t, "INVALID_AMOUNT", infraerrors.FromError(err).Reason)
	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.Equal(t, 1.0, userAfter.Balance)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, orderCount)
}

func TestPaymentServiceCreateOrderBalancePayConcurrentDailyLimitAllowsOne(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	settings := fixture.paymentSvc.configService.settingRepo.(*balancePaySettingRepo)
	settings.values[SettingDailyRechargeLimit] = "35.90"
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	releaseUserLock := holdPaymentUserLock(t, userEnt.ID)

	type createResult struct {
		response *CreateOrderResponse
		err      error
	}
	start := make(chan struct{})
	results := make(chan createResult, 2)
	for _, key := range []string{"balance-pay-daily-race-a", "balance-pay-daily-race-b"} {
		key := key
		go func() {
			<-start
			response, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
				UserID:         userEnt.ID,
				PaymentType:    payment.TypeBalancePay,
				OrderType:      payment.OrderTypeSubscription,
				PlanID:         plan.ID,
				IdempotencyKey: key,
			})
			results <- createResult{response: response, err: err}
		}()
	}
	close(start)
	requirePaymentUserLockRefs(t, userEnt.ID, 3)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, orderCount)
	releaseUserLock()

	var succeeded, limited int
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
			require.NotNil(t, result.response)
		case infraerrors.Reason(result.err) == "DAILY_LIMIT_EXCEEDED":
			limited++
		default:
			require.NoError(t, result.err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, limited)

	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, userAfter.Balance, 0.0001)
	orderCount, err = fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, orderCount)
	subscriptionCount, err := fixture.client.UserSubscription.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, subscriptionCount)
}

func TestPaymentServiceCreateOrderConcurrentExternalPendingLimitAllowsOne(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	user, err := fixture.paymentSvc.userRepo.GetByID(ctx, userEnt.ID)
	require.NoError(t, err)
	req := CreateOrderRequest{
		UserID:      userEnt.ID,
		Amount:      10,
		PaymentType: payment.TypeStripe,
		OrderType:   payment.OrderTypeBalance,
	}
	cfg := &PaymentConfig{MaxPendingOrders: 1, OrderTimeoutMin: 30}
	releaseUserLock := holdPaymentUserLock(t, userEnt.ID)

	type createResult struct {
		order *dbent.PaymentOrder
		err   error
	}
	start := make(chan struct{})
	results := make(chan createResult, 2)
	for range 2 {
		go func() {
			<-start
			order, createErr := fixture.paymentSvc.createOrderInTx(ctx, req, user, nil, cfg, 10, 10, 0, 10, nil)
			results <- createResult{order: order, err: createErr}
		}()
	}
	close(start)
	requirePaymentUserLockRefs(t, userEnt.ID, 3)
	pendingCount, err := fixture.client.PaymentOrder.Query().
		Where(paymentorder.UserIDEQ(userEnt.ID), paymentorder.StatusEQ(OrderStatusPending)).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, pendingCount)
	releaseUserLock()

	var succeeded, limited int
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
			require.NotNil(t, result.order)
		case infraerrors.Reason(result.err) == "TOO_MANY_PENDING":
			limited++
		default:
			require.NoError(t, result.err)
		}
	}
	require.Equal(t, 1, succeeded)
	require.Equal(t, 1, limited)

	pendingCount, err = fixture.client.PaymentOrder.Query().
		Where(paymentorder.UserIDEQ(userEnt.ID), paymentorder.StatusEQ(OrderStatusPending)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, pendingCount)
}

func TestPaymentServiceCreateOrderBalancePayKeepsCommittedPurchaseWhenCacheInvalidationFails(t *testing.T) {
	ctx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)

	initialExpiresAt := time.Now().UTC().Truncate(time.Second).Add(10 * 24 * time.Hour)
	require.NoError(t, fixture.paymentSvc.subscriptionSvc.userSubRepo.Create(ctx, &UserSubscription{
		UserID:     userEnt.ID,
		GroupID:    groupEnt.ID,
		StartsAt:   time.Now().Add(-24 * time.Hour),
		ExpiresAt:  initialExpiresAt,
		Status:     SubscriptionStatusActive,
		AssignedAt: time.Now().Add(-24 * time.Hour),
		Notes:      "existing subscription",
	}))

	cache := newBlockingFailingBalancePayBillingCache()
	defer cache.releaseBalanceOnce()
	defer cache.releaseOnce()
	fixture.paymentSvc.subscriptionSvc.billingCacheService = &BillingCacheService{cache: cache}

	type createResult struct {
		response *CreateOrderResponse
		err      error
	}
	resultCh := make(chan createResult, 1)
	go func() {
		response, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
			UserID:         userEnt.ID,
			PaymentType:    payment.TypeBalancePay,
			OrderType:      payment.OrderTypeSubscription,
			PlanID:         plan.ID,
			IdempotencyKey: "balance-pay-cache-failure-key",
		})
		resultCh <- createResult{response: response, err: err}
	}()

	select {
	case balanceUserID := <-cache.balanceEntered:
		require.Equal(t, userEnt.ID, balanceUserID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for balance cache invalidation")
	}

	select {
	case result := <-resultCh:
		cache.releaseBalanceOnce()
		t.Fatalf("CreateOrder returned before post-commit balance cache invalidation completed: response=%v err=%v", result.response, result.err)
	case <-time.After(100 * time.Millisecond):
	}

	orderID := assertBalancePayCommittedPurchase(
		t,
		context.Background(),
		fixture,
		userEnt.ID,
		groupEnt.ID,
		initialExpiresAt,
	)
	cancelRequest()
	cache.releaseBalanceOnce()

	var invalidation balancePayCacheInvalidation
	select {
	case invalidation = <-cache.entered:
		require.Equal(t, userEnt.ID, invalidation.userID)
		require.Equal(t, groupEnt.ID, invalidation.groupID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscription cache invalidation")
	}

	select {
	case result := <-resultCh:
		cache.releaseOnce()
		t.Fatalf("CreateOrder returned before post-commit cache invalidation completed: response=%v err=%v", result.response, result.err)
	case <-time.After(100 * time.Millisecond):
	}

	cache.releaseOnce()
	var result createResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for balance_pay order result")
	}

	require.NoError(t, result.err)
	require.NotNil(t, result.response)
	require.Equal(t, orderID, result.response.OrderID)
	require.Equal(t, OrderStatusCompleted, result.response.Status)
	require.Equal(t, int64(1), cache.balanceCalls.Load())
	require.Equal(t, userEnt.ID, cache.balanceUserID.Load())
	require.Equal(t, int64(1), cache.calls.Load())
}

func assertBalancePayCommittedPurchase(
	t *testing.T,
	ctx context.Context,
	fixture *balancePayFixture,
	userID int64,
	groupID int64,
	initialExpiresAt time.Time,
) int64 {
	t.Helper()

	userAfter, err := fixture.client.User.Get(ctx, userID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, userAfter.Balance, 0.0001)

	orders, err := fixture.client.PaymentOrder.Query().All(ctx)
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, OrderStatusCompleted, orders[0].Status)
	require.NotNil(t, orders[0].CompletedAt)

	subscriptions, err := fixture.client.UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.GroupIDEQ(groupID)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, subscriptions, 1)
	require.WithinDuration(t, initialExpiresAt.AddDate(0, 0, 30), subscriptions[0].ExpiresAt, time.Second)

	auditLogs, err := fixture.paymentSvc.GetOrderAuditLogs(ctx, orders[0].ID)
	require.NoError(t, err)
	require.Len(t, auditLogs, 4)
	actionCounts := make(map[string]int, len(auditLogs))
	for _, auditLog := range auditLogs {
		actionCounts[auditLog.Action]++
	}
	for _, action := range []string{
		"BALANCE_PAY_DEDUCTED",
		"SUBSCRIPTION_ASSIGNED",
		"AFFILIATE_REBATE_SKIPPED",
		"SUBSCRIPTION_SUCCESS",
	} {
		require.Equal(t, 1, actionCounts[action], "unexpected audit count for %s", action)
	}
	return orders[0].ID
}

func TestPaymentServiceCreateOrderBalancePaySubscriptionUsesRechargeMultiplier(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 2)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)

	resp, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "balance-pay-multiplier-key",
	})

	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, resp.Status)
	require.InEpsilon(t, 71.80, resp.PayAmount, 0.0001)

	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InEpsilon(t, 28.20, userAfter.Balance, 0.0001)

	order := fixture.onlyOrder(t, ctx, resp.OrderID)
	require.InEpsilon(t, 35.90, order.Amount, 0.0001)
	require.InEpsilon(t, 71.80, order.PayAmount, 0.0001)
}

func TestPaymentServiceCreateOrderBalancePaySubscriptionRejectsInsufficientBalance(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 10)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)

	resp, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "balance-pay-insufficient-key",
	})

	require.Nil(t, resp)
	require.Error(t, err)
	appErr := infraerrors.FromError(err)
	require.Equal(t, "INSUFFICIENT_BALANCE", appErr.Reason)
	require.Equal(t, map[string]string{
		"current_balance":  "10.00",
		"required_balance": "35.90",
		"deficit":          "25.90",
	}, appErr.Metadata)

	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InEpsilon(t, 10.00, userAfter.Balance, 0.0001)

	activeCount, err := fixture.client.UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userEnt.ID),
			usersubscription.GroupIDEQ(groupEnt.ID),
			usersubscription.StatusEQ(SubscriptionStatusActive),
		).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, activeCount)

	orders, err := fixture.client.PaymentOrder.Query().All(ctx)
	require.NoError(t, err)
	for _, order := range orders {
		require.NotEqual(t, OrderStatusCompleted, order.Status)
	}
}

func TestPaymentServiceCreateOrderBalancePayRollsBackWhenSubscriptionAssignmentFails(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)
	fixture.paymentSvc.subscriptionSvc.userSubRepo = failingBalancePayUserSubscriptionRepo{err: errors.New("subscription write failed")}

	resp, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "balance-pay-rollback-key",
	})

	require.Nil(t, resp)
	require.ErrorContains(t, err, "subscription write failed")

	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InEpsilon(t, 100.00, userAfter.Balance, 0.0001)

	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, orderCount)

	subCount, err := fixture.client.UserSubscription.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, subCount)
}

func TestPaymentServiceCreateOrderBalancePaySecondClickAfterBalanceSpentFails(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 35.90)
	groupEnt := fixture.createSubscriptionGroup(t, ctx)
	plan := fixture.createPlan(t, ctx, groupEnt.ID, 35.90)

	first, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "balance-pay-first-independent-key",
	})
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, first.Status)

	second, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeSubscription,
		PlanID:         plan.ID,
		IdempotencyKey: "balance-pay-second-independent-key",
	})
	require.Nil(t, second)
	require.Error(t, err)
	appErr := infraerrors.FromError(err)
	require.Equal(t, "INSUFFICIENT_BALANCE", appErr.Reason)

	userAfter, err := fixture.client.User.Get(ctx, userEnt.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.0, userAfter.Balance, 0.0001)

	completedOrders, err := fixture.client.PaymentOrder.Query().
		Where(paymentorder.StatusEQ(OrderStatusCompleted)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, completedOrders)
}

func TestPaymentServiceCreateOrderBalancePayRejectsBalanceOrderType(t *testing.T) {
	ctx := context.Background()
	fixture := newBalancePayFixture(t, 1)
	userEnt := fixture.createUser(t, ctx, 100)

	resp, err := fixture.paymentSvc.CreateOrder(ctx, CreateOrderRequest{
		UserID:         userEnt.ID,
		PaymentType:    payment.TypeBalancePay,
		OrderType:      payment.OrderTypeBalance,
		Amount:         10,
		IdempotencyKey: "balance-pay-invalid-order-type-key",
	})

	require.Nil(t, resp)
	require.Error(t, err)
	appErr := infraerrors.FromError(err)
	require.Equal(t, "INVALID_ORDER_TYPE", appErr.Reason)
}

type failingBalancePayUserSubscriptionRepo struct {
	UserSubscriptionRepository
	err error
}

type balancePayCacheInvalidation struct {
	userID  int64
	groupID int64
}

type blockingFailingBalancePayBillingCache struct {
	BillingCache
	balanceEntered     chan int64
	balanceRelease     chan struct{}
	balanceReleaseGate sync.Once
	entered            chan balancePayCacheInvalidation
	release            chan struct{}
	releaseGate        sync.Once
	balanceCalls       atomic.Int64
	balanceUserID      atomic.Int64
	calls              atomic.Int64
}

func newBlockingFailingBalancePayBillingCache() *blockingFailingBalancePayBillingCache {
	return &blockingFailingBalancePayBillingCache{
		balanceEntered: make(chan int64, 1),
		balanceRelease: make(chan struct{}),
		entered:        make(chan balancePayCacheInvalidation, 1),
		release:        make(chan struct{}),
	}
}

func (c *blockingFailingBalancePayBillingCache) InvalidateUserBalance(_ context.Context, userID int64) error {
	c.balanceCalls.Add(1)
	c.balanceUserID.Store(userID)
	c.balanceEntered <- userID
	<-c.balanceRelease
	return errors.New("balance cache unavailable")
}

func (c *blockingFailingBalancePayBillingCache) InvalidateSubscriptionCache(_ context.Context, userID, groupID int64) error {
	c.calls.Add(1)
	c.entered <- balancePayCacheInvalidation{userID: userID, groupID: groupID}
	<-c.release
	return errors.New("subscription cache unavailable")
}

func (c *blockingFailingBalancePayBillingCache) releaseOnce() {
	c.releaseGate.Do(func() { close(c.release) })
}

func (c *blockingFailingBalancePayBillingCache) releaseBalanceOnce() {
	c.balanceReleaseGate.Do(func() { close(c.balanceRelease) })
}

func (r failingBalancePayUserSubscriptionRepo) GetByUserIDAndGroupID(context.Context, int64, int64) (*UserSubscription, error) {
	return nil, ErrSubscriptionNotFound
}

func (r failingBalancePayUserSubscriptionRepo) Create(context.Context, *UserSubscription) error {
	return r.err
}

type balancePayFixture struct {
	client     *dbent.Client
	paymentSvc *PaymentService
}

func newBalancePayFixture(t *testing.T, multiplier float64) *balancePayFixture {
	t.Helper()

	dbName := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared&_fk=1"
	db, err := sql.Open("sqlite", dbName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	settingRepo := &balancePaySettingRepo{values: map[string]string{
		SettingPaymentEnabled:      "true",
		SettingBalanceRechargeMult: fmt.Sprintf("%g", multiplier),
	}}
	configSvc := NewPaymentConfigService(client, settingRepo, nil)
	userRepo := &balancePayUserRepo{client: client}
	groupRepo := &balancePayGroupRepo{client: client}
	userSubRepo := &balancePayUserSubscriptionRepo{client: client}
	subSvc := NewSubscriptionService(groupRepo, userSubRepo, nil, client, nil)
	paymentSvc := NewPaymentService(client, payment.NewRegistry(), nil, nil, subSvc, configSvc, userRepo, groupRepo, nil)

	return &balancePayFixture{client: client, paymentSvc: paymentSvc}
}

func (f *balancePayFixture) createUser(t *testing.T, ctx context.Context, balance float64) *dbent.User {
	t.Helper()

	u, err := f.client.User.Create().
		SetEmail(fmt.Sprintf("balance-pay-%d@example.com", time.Now().UnixNano())).
		SetPasswordHash("hash").
		SetUsername("Balance Pay User").
		SetRole(RoleUser).
		SetStatus(StatusActive).
		SetBalance(balance).
		Save(ctx)
	require.NoError(t, err)
	return u
}

func (f *balancePayFixture) createSubscriptionGroup(t *testing.T, ctx context.Context) *dbent.Group {
	t.Helper()

	g, err := f.client.Group.Create().
		SetName(fmt.Sprintf("Balance Pay Group %d", time.Now().UnixNano())).
		SetPlatform(PlatformAnthropic).
		SetStatus(StatusActive).
		SetSubscriptionType(SubscriptionTypeSubscription).
		Save(ctx)
	require.NoError(t, err)
	return g
}

func (f *balancePayFixture) createPlan(t *testing.T, ctx context.Context, groupID int64, price float64) *dbent.SubscriptionPlan {
	t.Helper()

	plan, err := f.client.SubscriptionPlan.Create().
		SetGroupID(groupID).
		SetName("Monthly").
		SetPrice(price).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		Save(ctx)
	require.NoError(t, err)
	return plan
}

func (f *balancePayFixture) onlyOrder(t *testing.T, ctx context.Context, orderID int64) *dbent.PaymentOrder {
	t.Helper()

	order, err := f.client.PaymentOrder.Query().
		Where(paymentorder.IDEQ(orderID)).
		Only(ctx)
	require.NoError(t, err)
	return order
}

type balancePaySettingRepo struct {
	SettingRepository
	values map[string]string
}

func (r *balancePaySettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = r.values[key]
	}
	return out, nil
}

type balancePayUserRepo struct {
	UserRepository
	client *dbent.Client
}

func (r *balancePayUserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	u, err := r.client.User.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &User{
		ID:           u.ID,
		Email:        u.Email,
		Username:     u.Username,
		Notes:        u.Notes,
		PasswordHash: u.PasswordHash,
		Role:         u.Role,
		Balance:      u.Balance,
		Concurrency:  u.Concurrency,
		Status:       u.Status,
		CreatedAt:    u.CreatedAt,
		UpdatedAt:    u.UpdatedAt,
	}, nil
}

type balancePayGroupRepo struct {
	GroupRepository
	client *dbent.Client
}

func (r *balancePayGroupRepo) GetByID(ctx context.Context, id int64) (*Group, error) {
	g, err := r.client.Group.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return balancePayGroupFromEnt(g), nil
}

func balancePayGroupFromEnt(g *dbent.Group) *Group {
	return &Group{
		ID:                  g.ID,
		Name:                g.Name,
		Platform:            g.Platform,
		RateMultiplier:      g.RateMultiplier,
		IsExclusive:         g.IsExclusive,
		Status:              g.Status,
		Hydrated:            true,
		SubscriptionType:    g.SubscriptionType,
		DailyLimitUSD:       g.DailyLimitUsd,
		WeeklyLimitUSD:      g.WeeklyLimitUsd,
		MonthlyLimitUSD:     g.MonthlyLimitUsd,
		DefaultValidityDays: g.DefaultValidityDays,
		CreatedAt:           g.CreatedAt,
		UpdatedAt:           g.UpdatedAt,
	}
}

type balancePayUserSubscriptionRepo struct {
	UserSubscriptionRepository
	client *dbent.Client
}

func (r *balancePayUserSubscriptionRepo) entClient(ctx context.Context) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return r.client
}

func (r *balancePayUserSubscriptionRepo) Create(ctx context.Context, sub *UserSubscription) error {
	created, err := r.entClient(ctx).UserSubscription.Create().
		SetUserID(sub.UserID).
		SetGroupID(sub.GroupID).
		SetStartsAt(sub.StartsAt).
		SetExpiresAt(sub.ExpiresAt).
		SetStatus(sub.Status).
		SetAssignedAt(sub.AssignedAt).
		SetNotes(sub.Notes).
		Save(ctx)
	if err != nil {
		return err
	}
	sub.ID = created.ID
	sub.CreatedAt = created.CreatedAt
	sub.UpdatedAt = created.UpdatedAt
	return nil
}

func (r *balancePayUserSubscriptionRepo) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	sub, err := r.entClient(ctx).UserSubscription.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return balancePayUserSubFromEnt(sub), nil
}

func (r *balancePayUserSubscriptionRepo) GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*UserSubscription, error) {
	sub, err := r.entClient(ctx).UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.GroupIDEQ(groupID)).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return balancePayUserSubFromEnt(sub), nil
}

func (r *balancePayUserSubscriptionRepo) GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*UserSubscription, error) {
	sub, err := r.entClient(ctx).UserSubscription.Query().
		Where(
			usersubscription.UserIDEQ(userID),
			usersubscription.GroupIDEQ(groupID),
			usersubscription.StatusEQ(SubscriptionStatusActive),
			usersubscription.ExpiresAtGT(time.Now()),
		).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	return balancePayUserSubFromEnt(sub), nil
}

func (r *balancePayUserSubscriptionRepo) Update(ctx context.Context, sub *UserSubscription) error {
	upd := r.entClient(ctx).UserSubscription.UpdateOneID(sub.ID).
		SetStartsAt(sub.StartsAt).
		SetExpiresAt(sub.ExpiresAt).
		SetStatus(sub.Status).
		SetAssignedAt(sub.AssignedAt).
		SetNotes(sub.Notes)
	if sub.DailyWindowStart != nil {
		upd.SetDailyWindowStart(*sub.DailyWindowStart)
	}
	if sub.WeeklyWindowStart != nil {
		upd.SetWeeklyWindowStart(*sub.WeeklyWindowStart)
	}
	if sub.MonthlyWindowStart != nil {
		upd.SetMonthlyWindowStart(*sub.MonthlyWindowStart)
	}
	return upd.Exec(ctx)
}

func (r *balancePayUserSubscriptionRepo) ExistsByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (bool, error) {
	return r.entClient(ctx).UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.GroupIDEQ(groupID)).
		Exist(ctx)
}

func (r *balancePayUserSubscriptionRepo) ExtendExpiry(ctx context.Context, subscriptionID int64, newExpiresAt time.Time) error {
	return r.entClient(ctx).UserSubscription.UpdateOneID(subscriptionID).
		SetExpiresAt(newExpiresAt).
		Exec(ctx)
}

func (r *balancePayUserSubscriptionRepo) UpdateStatus(ctx context.Context, subscriptionID int64, status string) error {
	return r.entClient(ctx).UserSubscription.UpdateOneID(subscriptionID).
		SetStatus(status).
		Exec(ctx)
}

func (r *balancePayUserSubscriptionRepo) UpdateNotes(ctx context.Context, subscriptionID int64, notes string) error {
	return r.entClient(ctx).UserSubscription.UpdateOneID(subscriptionID).
		SetNotes(notes).
		Exec(ctx)
}

func (r *balancePayUserSubscriptionRepo) ListActiveByUserID(ctx context.Context, userID int64) ([]UserSubscription, error) {
	subs, err := r.entClient(ctx).UserSubscription.Query().
		Where(usersubscription.UserIDEQ(userID), usersubscription.StatusEQ(SubscriptionStatusActive), usersubscription.ExpiresAtGT(time.Now())).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]UserSubscription, 0, len(subs))
	for _, sub := range subs {
		out = append(out, *balancePayUserSubFromEnt(sub))
	}
	return out, nil
}

func (r *balancePayUserSubscriptionRepo) List(ctx context.Context, _ pagination.PaginationParams, userID, groupID *int64, status, _, _, _ string) ([]UserSubscription, *pagination.PaginationResult, error) {
	q := r.entClient(ctx).UserSubscription.Query()
	if userID != nil {
		q.Where(usersubscription.UserIDEQ(*userID))
	}
	if groupID != nil {
		q.Where(usersubscription.GroupIDEQ(*groupID))
	}
	if status != "" {
		q.Where(usersubscription.StatusEQ(status))
	}
	subs, err := q.All(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]UserSubscription, 0, len(subs))
	for _, sub := range subs {
		out = append(out, *balancePayUserSubFromEnt(sub))
	}
	return out, &pagination.PaginationResult{Total: int64(len(out)), Page: 1, PageSize: len(out), Pages: 1}, nil
}

func balancePayUserSubFromEnt(sub *dbent.UserSubscription) *UserSubscription {
	notes := ""
	if sub.Notes != nil {
		notes = *sub.Notes
	}
	return &UserSubscription{
		ID:                 sub.ID,
		UserID:             sub.UserID,
		GroupID:            sub.GroupID,
		StartsAt:           sub.StartsAt,
		ExpiresAt:          sub.ExpiresAt,
		Status:             sub.Status,
		DailyWindowStart:   sub.DailyWindowStart,
		WeeklyWindowStart:  sub.WeeklyWindowStart,
		MonthlyWindowStart: sub.MonthlyWindowStart,
		DailyUsageUSD:      sub.DailyUsageUsd,
		WeeklyUsageUSD:     sub.WeeklyUsageUsd,
		MonthlyUsageUSD:    sub.MonthlyUsageUsd,
		AssignedBy:         sub.AssignedBy,
		AssignedAt:         sub.AssignedAt,
		Notes:              notes,
		CreatedAt:          sub.CreatedAt,
		UpdatedAt:          sub.UpdatedAt,
	}
}
