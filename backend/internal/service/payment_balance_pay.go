package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	balancePayRequestFingerprintDomain      = "sub2api:balance-pay:request-fingerprint:v1"
	balancePayRequestFingerprintSnapshotKey = "_balance_pay_request_fingerprint"
	balancePayRequestSchemaSnapshotKey      = "_balance_pay_request_schema"
)

func (s *PaymentService) createBalancePaySubscriptionOrder(ctx context.Context, req CreateOrderRequest, user *User, plan *dbent.SubscriptionPlan, cfg *PaymentConfig) (*CreateOrderResponse, error) {
	if req.OrderType != payment.OrderTypeSubscription {
		return nil, infraerrors.BadRequest("INVALID_ORDER_TYPE", "balance_pay only supports subscription orders")
	}
	if plan == nil || plan.ID <= 0 || plan.GroupID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "balance_pay subscription order requires a valid plan")
	}
	idempotencyKey, err := NormalizeIdempotencyKey(req.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if idempotencyKey == "" {
		return nil, ErrIdempotencyKeyRequired
	}
	requestFingerprint, err := buildBalancePayRequestFingerprint(req)
	if err != nil {
		return nil, err
	}

	requiredBalance := calculateBalancePayRequiredBalance(plan.Price, cfg.BalanceRechargeMultiplier)
	if requiredBalance <= 0 {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "balance_pay required balance must be positive")
	}
	outTradeNo := deriveBalancePayOutTradeNo(req.UserID, idempotencyKey)
	releaseUserLock, err := acquirePaymentUserLock(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	defer releaseUserLock()
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockPaymentUserRow(ctx, tx, req.UserID); err != nil {
		return nil, err
	}
	if response, found, queryErr := findDurableBalancePayOrder(ctx, tx.PaymentOrder.Query(), outTradeNo, req, requestFingerprint); queryErr != nil {
		return nil, queryErr
	} else if found {
		return response, nil
	}

	if err := s.checkPendingLimit(ctx, tx, req.UserID, cfg.MaxPendingOrders); err != nil {
		return nil, err
	}
	if err := s.checkDailyLimit(ctx, tx, req.UserID, plan.Price, cfg.DailyLimit); err != nil {
		return nil, err
	}

	updated, err := tx.User.Update().
		Where(dbuser.IDEQ(req.UserID), dbuser.BalanceGTE(requiredBalance)).
		AddBalance(-requiredBalance).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("deduct balance: %w", err)
	}
	if updated == 0 {
		currentUser, getErr := tx.User.Get(ctx, req.UserID)
		if getErr != nil {
			return nil, fmt.Errorf("get current balance: %w", getErr)
		}
		return nil, insufficientBalanceError(currentUser.Balance, requiredBalance)
	}

	tm := cfg.OrderTimeoutMin
	if tm <= 0 {
		tm = defaultOrderTimeoutMin
	}
	now := time.Now()
	order, err := tx.PaymentOrder.Create().
		SetUserID(req.UserID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetNillableUserNotes(psNilIfEmpty(user.Notes)).
		SetAmount(plan.Price).
		SetPayAmount(requiredBalance).
		SetFeeRate(0).
		SetRechargeCode("").
		SetOutTradeNo(outTradeNo).
		SetPaymentType(payment.TypeBalancePay).
		SetPaymentTradeNo("balance").
		SetOrderType(payment.OrderTypeSubscription).
		SetStatus(OrderStatusPaid).
		SetPaidAt(now).
		SetExpiresAt(now.Add(time.Duration(tm) * time.Minute)).
		SetClientIP(req.ClientIP).
		SetSrcHost(req.SrcHost).
		SetPlanID(plan.ID).
		SetSubscriptionGroupID(plan.GroupID).
		SetSubscriptionDays(psComputeValidityDays(plan.ValidityDays, plan.ValidityUnit)).
		SetProviderSnapshot(map[string]any{
			balancePayRequestSchemaSnapshotKey:      1,
			balancePayRequestFingerprintSnapshotKey: requestFingerprint,
		}).
		Save(ctx)
	if err != nil {
		if isBalancePayOutTradeNoConflict(err) {
			_ = tx.Rollback()
			if response, found, queryErr := findDurableBalancePayOrder(ctx, s.entClient.PaymentOrder.Query(), outTradeNo, req, requestFingerprint); queryErr != nil {
				return nil, queryErr
			} else if found {
				return response, nil
			}
		}
		return nil, fmt.Errorf("create balance_pay order: %w", err)
	}
	code := fmt.Sprintf("PAY-%d-%d", order.ID, time.Now().UnixNano()%100000)
	order, err = tx.PaymentOrder.UpdateOneID(order.ID).SetRechargeCode(code).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("set recharge code: %w", err)
	}
	if err := s.fulfillBalancePaySubscriptionOrder(ctx, tx, order, req.UserID, plan.GroupID, requiredBalance, plan.Price); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit balance_pay order transaction: %w", err)
	}
	releaseUserLock()
	response := buildBalancePayCreateOrderResponse(order)
	s.invalidateBalancePayCachesAfterCommit(order.ID, req.UserID, plan.GroupID)
	return response, nil
}

func isBalancePayOutTradeNoConflict(err error) bool {
	if err == nil || !dbent.IsConstraintError(err) {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "out_trade_no") ||
		strings.Contains(message, "paymentorder_out_trade_no")
}

func findDurableBalancePayOrder(ctx context.Context, query *dbent.PaymentOrderQuery, outTradeNo string, req CreateOrderRequest, requestFingerprint string) (*CreateOrderResponse, bool, error) {
	order, err := query.Where(paymentorder.OutTradeNo(outTradeNo)).Only(ctx)
	if dbent.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("query durable balance_pay order: %w", err)
	}
	response, err := replayBalancePayOrder(order, req, requestFingerprint)
	return response, true, err
}

func replayBalancePayOrder(order *dbent.PaymentOrder, req CreateOrderRequest, requestFingerprint string) (*CreateOrderResponse, error) {
	storedFingerprint := psSnapshotStringValue(order.ProviderSnapshot[balancePayRequestFingerprintSnapshotKey])
	if order.UserID != req.UserID ||
		order.PaymentType != payment.TypeBalancePay ||
		order.OrderType != payment.OrderTypeSubscription ||
		order.PlanID == nil || *order.PlanID != req.PlanID ||
		storedFingerprint == "" || storedFingerprint != requestFingerprint ||
		order.Status != OrderStatusCompleted {
		return nil, ErrIdempotencyKeyConflict
	}
	return buildBalancePayCreateOrderResponse(order), nil
}

func buildBalancePayRequestFingerprint(req CreateOrderRequest) (string, error) {
	payload := canonicalBalancePayPayload(req)
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", ErrIdempotencyInvalidPayload.WithCause(err)
	}
	sum := sha256.Sum256([]byte(balancePayRequestFingerprintDomain + "\x00" + string(raw)))
	return hex.EncodeToString(sum[:]), nil
}

func canonicalBalancePayPayload(req CreateOrderRequest) BalancePayCanonicalPayload {
	if req.BalancePayCanonicalPayload != nil {
		return CanonicalizeBalancePayPayload(req.BalancePayCanonicalPayload)
	}

	isMobile := req.IsMobile
	return BalancePayCanonicalPayload{
		Amount:        req.Amount,
		PaymentType:   normalizedBalancePayFingerprintPaymentType(req.PaymentType),
		OpenID:        req.OpenID,
		ReturnURL:     req.ReturnURL,
		PaymentSource: req.PaymentSource,
		OrderType:     req.OrderType,
		PlanID:        req.PlanID,
		IsMobile:      &isMobile,
	}
}

// CanonicalizeBalancePayPayload returns the stable, coordinator-safe form of
// the business payload used by both the handler idempotency layer and durable
// payment replay. It excludes request-environment fields by construction.
func CanonicalizeBalancePayPayload(input *BalancePayCanonicalPayload) BalancePayCanonicalPayload {
	if input == nil {
		return BalancePayCanonicalPayload{OrderType: payment.OrderTypeBalance}
	}
	payload := *input
	if payload.IsMobile != nil {
		isMobile := *payload.IsMobile
		payload.IsMobile = &isMobile
	}
	payload.PaymentType = normalizedBalancePayFingerprintPaymentType(payload.PaymentType)
	if payload.OrderType == "" {
		payload.OrderType = payment.OrderTypeBalance
	}
	return payload
}

func normalizedBalancePayFingerprintPaymentType(paymentType string) string {
	if normalized := NormalizeVisibleMethod(paymentType); normalized != "" {
		return normalized
	}
	return paymentType
}

func buildBalancePayCreateOrderResponse(order *dbent.PaymentOrder) *CreateOrderResponse {
	return &CreateOrderResponse{
		OrderID:     order.ID,
		Amount:      order.Amount,
		PayAmount:   order.PayAmount,
		FeeRate:     order.FeeRate,
		Status:      OrderStatusCompleted,
		ResultType:  payment.CreatePaymentResultOrderCreated,
		PaymentType: order.PaymentType,
		OutTradeNo:  order.OutTradeNo,
		ExpiresAt:   order.ExpiresAt,
	}
}

func (s *PaymentService) fulfillBalancePaySubscriptionOrder(ctx context.Context, tx *dbent.Tx, order *dbent.PaymentOrder, userID, groupID int64, requiredBalance, planPrice float64) error {
	txCtx := dbent.NewTxContext(ctx, tx)
	days := psComputeValidityDaysPtr(order.SubscriptionDays)
	if days <= 0 {
		return infraerrors.BadRequest("INVALID_STATUS", "missing subscription info")
	}
	if s.groupRepo == nil || s.subscriptionSvc == nil {
		return infraerrors.ServiceUnavailable("SUBSCRIPTION_SERVICE_UNAVAILABLE", "subscription service unavailable")
	}
	group, err := s.groupRepo.GetByID(txCtx, groupID)
	if err != nil || group == nil || group.Status != payment.EntityStatusActive {
		return infraerrors.NotFound("GROUP_NOT_FOUND", "subscription group is no longer available")
	}
	orderNote := fmt.Sprintf("payment order %d", order.ID)
	if _, _, err := s.subscriptionSvc.assignOrExtendSubscription(txCtx, &AssignSubscriptionInput{
		UserID:       userID,
		GroupID:      groupID,
		ValidityDays: days,
		AssignedBy:   0,
		Notes:        orderNote,
	}, true); err != nil {
		return fmt.Errorf("assign balance_pay subscription: %w", err)
	}

	now := time.Now()
	if _, err := tx.PaymentOrder.Update().
		Where(paymentorder.IDEQ(order.ID), paymentorder.StatusEQ(OrderStatusPaid)).
		SetStatus(OrderStatusCompleted).
		SetCompletedAt(now).
		Save(txCtx); err != nil {
		return fmt.Errorf("mark balance_pay order completed: %w", err)
	}
	if err := createBalancePayAuditLog(txCtx, tx, order.ID, "BALANCE_PAY_DEDUCTED", fmt.Sprintf("user:%d", userID), map[string]any{
		"deductedBalance": requiredBalance,
		"planPrice":       planPrice,
	}); err != nil {
		return err
	}
	if err := createBalancePayAuditLog(txCtx, tx, order.ID, "SUBSCRIPTION_ASSIGNED", "system", map[string]any{
		"groupID":      groupID,
		"validityDays": days,
	}); err != nil {
		return err
	}
	if err := createBalancePayAuditLog(txCtx, tx, order.ID, "AFFILIATE_REBATE_SKIPPED", "system", map[string]any{
		"baseAmount": 0,
		"reason":     "balance_pay subscription orders do not trigger affiliate rebate",
	}); err != nil {
		return err
	}
	return createBalancePayAuditLog(txCtx, tx, order.ID, "SUBSCRIPTION_SUCCESS", "system", map[string]any{
		"rechargeCode":   order.RechargeCode,
		"creditedAmount": order.Amount,
		"payAmount":      order.PayAmount,
	})
}

func (s *PaymentService) invalidateBalancePayCachesAfterCommit(orderID, userID, groupID int64) {
	if s == nil || s.subscriptionSvc == nil {
		return
	}
	if billingCacheService := s.subscriptionSvc.billingCacheService; billingCacheService != nil {
		cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := billingCacheService.InvalidateUserBalance(cacheCtx, userID)
		cancel()
		if err != nil {
			slog.Warn("balance_pay balance cache invalidation failed after commit",
				"orderID", orderID,
				"userID", userID,
				"error", err,
			)
		}
	}
	if err := s.subscriptionSvc.invalidateSubscriptionCaches(userID, groupID); err != nil {
		slog.Warn("balance_pay subscription cache invalidation failed after commit",
			"orderID", orderID,
			"userID", userID,
			"groupID", groupID,
			"error", err,
		)
	}
}

func psComputeValidityDaysPtr(days *int) int {
	if days == nil {
		return 0
	}
	return *days
}

func createBalancePayAuditLog(ctx context.Context, tx *dbent.Tx, orderID int64, action, operator string, detail map[string]any) error {
	detailJSON, _ := json.Marshal(detail)
	if _, err := tx.PaymentAuditLog.Create().
		SetOrderID(strconv.FormatInt(orderID, 10)).
		SetAction(action).
		SetDetail(string(detailJSON)).
		SetOperator(operator).
		Save(ctx); err != nil {
		return fmt.Errorf("create balance_pay audit log %s: %w", action, err)
	}
	return nil
}

func insufficientBalanceError(currentBalance, requiredBalance float64) error {
	deficit := math.Max(0, requiredBalance-currentBalance)
	return infraerrors.BadRequest("INSUFFICIENT_BALANCE", "insufficient balance").
		WithMetadata(map[string]string{
			"current_balance":  fmt.Sprintf("%.2f", currentBalance),
			"required_balance": fmt.Sprintf("%.2f", requiredBalance),
			"deficit":          fmt.Sprintf("%.2f", deficit),
		})
}
