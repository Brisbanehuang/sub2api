//go:build unit

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func TestGetCheckoutInfoIncludesVirtualBalancePay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	client := newPaymentHandlerTestClient(t)

	_, err := client.PaymentProviderInstance.Create().
		SetProviderKey(payment.TypeStripe).
		SetName("Stripe").
		SetConfig(`{"currency":"USD"}`).
		SetSupportedTypes("card,link").
		SetEnabled(true).
		Save(ctx)
	require.NoError(t, err)

	configSvc := service.NewPaymentConfigService(client, &checkoutInfoSettingRepoStub{values: map[string]string{}}, nil)
	handler := NewPaymentHandler(nil, configSvc)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/payment/checkout-info", nil)

	handler.GetCheckoutInfo(ginCtx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Methods map[string]struct {
				PaymentType string  `json:"payment_type"`
				Currency    string  `json:"currency"`
				FeeRate     float64 `json:"fee_rate"`
				DailyLimit  float64 `json:"daily_limit"`
				SingleMin   float64 `json:"single_min"`
				SingleMax   float64 `json:"single_max"`
				Available   bool    `json:"available"`
			} `json:"methods"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Contains(t, resp.Data.Methods, string(payment.TypeStripe))
	require.Contains(t, resp.Data.Methods, string(payment.TypeBalancePay))

	balancePay := resp.Data.Methods[string(payment.TypeBalancePay)]
	require.Equal(t, string(payment.TypeBalancePay), balancePay.PaymentType)
	require.True(t, balancePay.Available)
	require.Equal(t, payment.DefaultPaymentCurrency, balancePay.Currency)
	require.Zero(t, balancePay.FeeRate)
	require.Zero(t, balancePay.DailyLimit)
	require.Zero(t, balancePay.SingleMin)
	require.Zero(t, balancePay.SingleMax)
}

func newPaymentHandlerTestClient(t *testing.T) *dbent.Client {
	t.Helper()

	dbName := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared",
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
	return client
}

type checkoutInfoSettingRepoStub struct {
	values map[string]string
}

func (s *checkoutInfoSettingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	return nil, nil
}

func (s *checkoutInfoSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *checkoutInfoSettingRepoStub) Set(context.Context, string, string) error {
	return nil
}

func (s *checkoutInfoSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = s.values[key]
	}
	return out, nil
}

func (s *checkoutInfoSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (s *checkoutInfoSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}

func (s *checkoutInfoSettingRepoStub) Delete(context.Context, string) error {
	return nil
}
