//go:build unit

package handler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

const expectedPaymentOrderCreateIdempotencyScope = "user.payment.orders.create"

func TestPaymentHandlerCreateOrderReplaysSuccessfulRequestWithoutRepeatingSideEffects(t *testing.T) {
	fixture := newPaymentHandlerIdempotencyFixture(t)
	start := time.Now()
	body := fixture.orderBody(t, fixture.plan.ID)

	first := fixture.createOrder(body, "payment-order-replay-key")
	second := fixture.createOrder(body, "payment-order-replay-key")

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, first.Body.String(), second.Body.String())
	fixture.requireSinglePurchase(t)

	fixture.idempotencyRepo.mu.Lock()
	require.Len(t, fixture.idempotencyRepo.data, 1)
	var record *service.IdempotencyRecord
	for _, stored := range fixture.idempotencyRepo.data {
		record = fixture.idempotencyRepo.clone(stored)
	}
	fixture.idempotencyRepo.mu.Unlock()
	require.NotNil(t, record)
	require.Equal(t, expectedPaymentOrderCreateIdempotencyScope, record.Scope)
	require.WithinDuration(t, start.Add(fixture.idempotencyTTL), record.ExpiresAt, 2*time.Second)
}

func TestPaymentHandlerCreateOrderRejectsSameKeyWithDifferentPayload(t *testing.T) {
	fixture := newPaymentHandlerIdempotencyFixture(t)

	first := fixture.createOrder(fixture.orderBody(t, fixture.plan.ID), "payment-order-conflict-key")
	conflict := fixture.createOrder(fixture.orderBody(t, fixture.plan.ID+1), "payment-order-conflict-key")

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	var body struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(conflict.Body.Bytes(), &body))
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", body.Reason)
	fixture.requireSinglePurchase(t)
}

func TestPaymentHandlerCreateOrderReplaysDurableBalancePayAfterCoordinatorRecordIsLost(t *testing.T) {
	fixture := newPaymentHandlerIdempotencyFixture(t)
	body := fixture.orderBody(t, fixture.plan.ID)

	first := fixture.createOrder(body, "  durable-handler-recovery-key  ")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	fixture.idempotencyRepo.mu.Lock()
	fixture.idempotencyRepo.data = make(map[string]*service.IdempotencyRecord)
	fixture.idempotencyRepo.mu.Unlock()
	second := fixture.createOrder(body, "durable-handler-recovery-key")

	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.JSONEq(t, first.Body.String(), second.Body.String())
	fixture.requireSinglePurchase(t)
}

func TestPaymentHandlerCreateOrderRejectsChangedDurablePayloadAfterCoordinatorRecordIsLost(t *testing.T) {
	fixture := newPaymentHandlerIdempotencyFixture(t)
	firstBody, err := json.Marshal(CreateOrderRequest{
		Amount:        0,
		PaymentType:   payment.TypeBalancePay,
		ReturnURL:     "https://example.com/initial",
		PaymentSource: "checkout",
		OrderType:     payment.OrderTypeSubscription,
		PlanID:        fixture.plan.ID,
		IsMobile:      func() *bool { value := false; return &value }(),
	})
	require.NoError(t, err)
	changedBody, err := json.Marshal(CreateOrderRequest{
		Amount:        0,
		PaymentType:   payment.TypeBalancePay,
		ReturnURL:     "https://example.com/changed",
		PaymentSource: "checkout",
		OrderType:     payment.OrderTypeSubscription,
		PlanID:        fixture.plan.ID,
		IsMobile:      func() *bool { value := false; return &value }(),
	})
	require.NoError(t, err)

	first := fixture.createOrder(firstBody, "durable-handler-payload-conflict-key")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	fixture.idempotencyRepo.mu.Lock()
	fixture.idempotencyRepo.data = make(map[string]*service.IdempotencyRecord)
	fixture.idempotencyRepo.mu.Unlock()
	conflict := fixture.createOrder(changedBody, "durable-handler-payload-conflict-key")

	require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
	var errorBody struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(conflict.Body.Bytes(), &errorBody))
	require.Equal(t, "IDEMPOTENCY_KEY_CONFLICT", errorBody.Reason)
	fixture.requireSinglePurchase(t)
}

func TestPaymentHandlerCreateOrderUsesCanonicalPayloadForDurableReplay(t *testing.T) {
	fixture := newPaymentHandlerIdempotencyFixture(t)

	firstBody := fixture.orderBody(t, fixture.plan.ID)
	firstBody = bytes.Replace(firstBody, []byte(`"payment_type":"balance_pay"`), []byte(`"payment_type":" balance_pay "`), 1)
	first := fixture.createOrder(firstBody, "durable-handler-canonical-key")
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	coordinatorConflict := fixture.createOrder(fixture.orderBody(t, fixture.plan.ID), "durable-handler-canonical-key")
	require.Equal(t, http.StatusOK, coordinatorConflict.Code, coordinatorConflict.Body.String())
	require.Equal(t, "true", coordinatorConflict.Header().Get("X-Idempotency-Replayed"))

	fixture.idempotencyRepo.mu.Lock()
	fixture.idempotencyRepo.data = make(map[string]*service.IdempotencyRecord)
	fixture.idempotencyRepo.mu.Unlock()

	second := fixture.createOrder(fixture.orderBody(t, fixture.plan.ID), "durable-handler-canonical-key")
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.JSONEq(t, first.Body.String(), second.Body.String())
	fixture.requireSinglePurchase(t)
}

func TestPaymentHandlerCreateOrderExternalPaymentsBypassIdempotencyCoordinator(t *testing.T) {
	methods := []string{
		string(payment.TypeStripe),
		string(payment.TypeAirwallex),
		string(payment.TypeWxpay),
		string(payment.TypeAlipay),
		string(payment.TypeUSDT),
		string(payment.TypeEasyPay),
		"custom_card",
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			fixture := newPaymentHandlerIdempotencyFixture(t)
			body, err := json.Marshal(CreateOrderRequest{
				Amount:      0,
				PaymentType: method,
				OrderType:   payment.OrderTypeBalance,
			})
			require.NoError(t, err)

			result := fixture.createOrder(body, strings.Repeat("x", 129))

			require.Equal(t, http.StatusBadRequest, result.Code, result.Body.String())
			var errorBody struct {
				Reason string `json:"reason"`
			}
			require.NoError(t, json.Unmarshal(result.Body.Bytes(), &errorBody))
			require.Equal(t, "INVALID_AMOUNT", errorBody.Reason)
			fixture.idempotencyRepo.mu.Lock()
			require.Empty(t, fixture.idempotencyRepo.data, "external payment must not claim an idempotency record")
			fixture.idempotencyRepo.mu.Unlock()
		})
	}
}

func TestPaymentHandlerCreateOrderScopesBalancePayKeyByAuthenticatedUser(t *testing.T) {
	fixture := newPaymentHandlerIdempotencyFixture(t)
	ctx := context.Background()
	secondUser, err := fixture.client.User.Create().
		SetEmail(fmt.Sprintf("payment-idempotency-second-%d@example.com", time.Now().UnixNano())).
		SetPasswordHash("hash").
		SetUsername("Second Payment Idempotency User").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		SetBalance(100).
		Save(ctx)
	require.NoError(t, err)
	body := fixture.orderBody(t, fixture.plan.ID)

	first := fixture.createOrderForUser(body, "shared-balance-pay-key", fixture.user.ID, context.Background())
	second := fixture.createOrderForUser(body, "shared-balance-pay-key", secondUser.ID, context.Background())

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	firstUser, err := fixture.client.User.Get(ctx, fixture.user.ID)
	require.NoError(t, err)
	secondUser, err = fixture.client.User.Get(ctx, secondUser.ID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, firstUser.Balance, 0.0001)
	require.InDelta(t, 64.10, secondUser.Balance, 0.0001)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, orderCount)
	subscriptionCount, err := fixture.client.UserSubscription.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, subscriptionCount)
}

func TestPaymentHandlerCreateOrderCanceledRequestStillCompletesAndReplaysBalancePay(t *testing.T) {
	fixture := newPaymentHandlerIdempotencyFixture(t)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	repo := &cancelAfterIdempotencyClaimRepo{
		userMemoryIdempotencyRepoStub: fixture.idempotencyRepo,
		cancel:                        cancelRequest,
	}
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	cfg.DefaultTTL = fixture.idempotencyTTL
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(repo, cfg))
	body := fixture.orderBody(t, fixture.plan.ID)

	first := fixture.createOrderForUser(body, "canceled-request-key", fixture.user.ID, requestCtx)
	second := fixture.createOrder(body, "canceled-request-key")

	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	require.Equal(t, "true", second.Header().Get("X-Idempotency-Replayed"))
	fixture.requireSinglePurchase(t)
}

func TestPaymentHandlerCreateOrderBalancePayExecutorErrorReclaimsSameKeyAfterBackoff(t *testing.T) {
	fixture := newPaymentHandlerIdempotencyFixture(t)
	ctx := context.Background()
	_, err := fixture.client.User.UpdateOneID(fixture.user.ID).SetBalance(0).Save(ctx)
	require.NoError(t, err)
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	cfg.DefaultTTL = fixture.idempotencyTTL
	failedRetryBackoff := 250 * time.Millisecond
	cfg.FailedRetryBackoff = failedRetryBackoff
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(fixture.idempotencyRepo, cfg))
	body := fixture.orderBody(t, fixture.plan.ID)

	first := fixture.createOrder(body, "failed-balance-pay-key")
	require.Equal(t, http.StatusBadRequest, first.Code, first.Body.String())
	duringBackoff := fixture.createOrder(body, "failed-balance-pay-key")
	require.Equal(t, http.StatusConflict, duringBackoff.Code, duringBackoff.Body.String())
	var errorBody struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(duringBackoff.Body.Bytes(), &errorBody))
	require.Equal(t, "IDEMPOTENCY_RETRY_BACKOFF", errorBody.Reason)

	_, err = fixture.client.User.UpdateOneID(fixture.user.ID).SetBalance(100).Save(ctx)
	require.NoError(t, err)
	time.Sleep(failedRetryBackoff + 50*time.Millisecond)
	recovered := fixture.createOrder(body, "failed-balance-pay-key")
	require.Equal(t, http.StatusOK, recovered.Code, recovered.Body.String())
	require.Empty(t, recovered.Header().Get("X-Idempotency-Replayed"))

	replayed := fixture.createOrder(body, "failed-balance-pay-key")
	require.Equal(t, http.StatusOK, replayed.Code, replayed.Body.String())
	require.Equal(t, "true", replayed.Header().Get("X-Idempotency-Replayed"))
	require.JSONEq(t, recovered.Body.String(), replayed.Body.String())
	fixture.requireSinglePurchase(t)
}

func TestPaymentHandlerCreateOrderBalancePayRequiresKeyInObserveOnlyMode(t *testing.T) {
	fixture := newPaymentHandlerIdempotencyFixture(t)
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = true
	cfg.DefaultTTL = fixture.idempotencyTTL
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(fixture.idempotencyRepo, cfg))

	result := fixture.createOrder(fixture.orderBody(t, fixture.plan.ID), "")

	require.Equal(t, http.StatusBadRequest, result.Code, result.Body.String())
	var errorBody struct {
		Reason string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(result.Body.Bytes(), &errorBody))
	require.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", errorBody.Reason)
	ctx := context.Background()
	user, err := fixture.client.User.Get(ctx, fixture.user.ID)
	require.NoError(t, err)
	require.InDelta(t, 100, user.Balance, 0.0001)
	orderCount, err := fixture.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, orderCount)
}

type cancelAfterIdempotencyClaimRepo struct {
	*userMemoryIdempotencyRepoStub
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelAfterIdempotencyClaimRepo) CreateProcessing(ctx context.Context, record *service.IdempotencyRecord) (bool, error) {
	owner, err := r.userMemoryIdempotencyRepoStub.CreateProcessing(ctx, record)
	if owner && err == nil {
		r.once.Do(r.cancel)
	}
	return owner, err
}

func (r *cancelAfterIdempotencyClaimRepo) MarkSucceeded(ctx context.Context, id int64, responseStatus int, responseBody string, expiresAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return fmt.Errorf("balance-pay finalization context must be bounded")
	}
	return r.userMemoryIdempotencyRepoStub.MarkSucceeded(ctx, id, responseStatus, responseBody, expiresAt)
}

type paymentHandlerIdempotencyFixture struct {
	client          *dbent.Client
	handler         *PaymentHandler
	router          *gin.Engine
	user            *dbent.User
	plan            *dbent.SubscriptionPlan
	idempotencyRepo *userMemoryIdempotencyRepoStub
	idempotencyTTL  time.Duration
}

func newPaymentHandlerIdempotencyFixture(t *testing.T) *paymentHandlerIdempotencyFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dbName := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared&_fk=1",
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()),
	)
	db, err := sql.Open("sqlite", dbName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	ctx := context.Background()
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("payment-idempotency-%d@example.com", time.Now().UnixNano())).
		SetPasswordHash("hash").
		SetUsername("Payment Idempotency User").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		SetBalance(100).
		Save(ctx)
	require.NoError(t, err)

	group, err := client.Group.Create().
		SetName(fmt.Sprintf("Payment Idempotency Group %d", time.Now().UnixNano())).
		SetPlatform(service.PlatformAnthropic).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeSubscription).
		Save(ctx)
	require.NoError(t, err)

	plan, err := client.SubscriptionPlan.Create().
		SetGroupID(group.ID).
		SetName("Monthly").
		SetPrice(35.90).
		SetValidityDays(30).
		SetValidityUnit("day").
		SetForSale(true).
		Save(ctx)
	require.NoError(t, err)

	settingRepo := &checkoutInfoSettingRepoStub{values: map[string]string{
		service.SettingPaymentEnabled:      "true",
		service.SettingBalanceRechargeMult: "1",
	}}
	configSvc := service.NewPaymentConfigService(client, settingRepo, nil)
	userRepo := repository.NewUserRepository(client, db)
	groupRepo := repository.NewGroupRepository(client, db)
	subscriptionSvc := service.NewSubscriptionService(
		groupRepo,
		repository.NewUserSubscriptionRepository(client),
		nil,
		client,
		nil,
	)
	t.Cleanup(subscriptionSvc.Stop)
	paymentSvc := service.NewPaymentService(
		client,
		payment.NewRegistry(),
		nil,
		nil,
		subscriptionSvc,
		configSvc,
		userRepo,
		groupRepo,
		nil,
	)
	handler := NewPaymentHandler(paymentSvc, configSvc)

	idempotencyRepo := newUserMemoryIdempotencyRepoStub()
	idempotencyConfig := service.DefaultIdempotencyConfig()
	idempotencyConfig.ObserveOnly = false
	idempotencyConfig.DefaultTTL = 45 * time.Minute
	previousCoordinator := service.DefaultIdempotencyCoordinator()
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(idempotencyRepo, idempotencyConfig))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(previousCoordinator) })

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: user.ID})
		c.Next()
	})
	router.POST("/api/v1/payment/orders", handler.CreateOrder)

	return &paymentHandlerIdempotencyFixture{
		client:          client,
		handler:         handler,
		router:          router,
		user:            user,
		plan:            plan,
		idempotencyRepo: idempotencyRepo,
		idempotencyTTL:  idempotencyConfig.DefaultTTL,
	}
}

func (f *paymentHandlerIdempotencyFixture) orderBody(t *testing.T, planID int64) []byte {
	t.Helper()
	body, err := json.Marshal(CreateOrderRequest{
		PaymentType: payment.TypeBalancePay,
		OrderType:   payment.OrderTypeSubscription,
		PlanID:      planID,
	})
	require.NoError(t, err)
	return body
}

func (f *paymentHandlerIdempotencyFixture) createOrder(body []byte, idempotencyKey string) *httptest.ResponseRecorder {
	return f.createOrderForUser(body, idempotencyKey, f.user.ID, context.Background())
}

func (f *paymentHandlerIdempotencyFixture) createOrderForUser(body []byte, idempotencyKey string, userID int64, ctx context.Context) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/payment/orders", bytes.NewReader(body))
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	recorder := httptest.NewRecorder()
	if userID == f.user.ID {
		f.router.ServeHTTP(recorder, req)
		return recorder
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
		c.Next()
	})
	router.POST("/api/v1/payment/orders", f.handler.CreateOrder)
	router.ServeHTTP(recorder, req)
	return recorder
}

func (f *paymentHandlerIdempotencyFixture) requireSinglePurchase(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	user, err := f.client.User.Get(ctx, f.user.ID)
	require.NoError(t, err)
	require.InDelta(t, 64.10, user.Balance, 0.0001)

	orderCount, err := f.client.PaymentOrder.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, orderCount)

	subscriptionCount, err := f.client.UserSubscription.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, subscriptionCount)
}
