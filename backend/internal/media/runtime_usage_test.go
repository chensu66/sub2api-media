package media

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type mediaUsageBillingStub struct {
	captures int
	applies  int
}

func (s *mediaUsageBillingStub) Apply(context.Context, *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	s.applies++
	return &service.UsageBillingApplyResult{Applied: true}, nil
}

func (s *mediaUsageBillingStub) ReserveBatchImageBalance(context.Context, *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return &service.BatchImageBalanceHoldResult{Applied: true}, nil
}

func (s *mediaUsageBillingStub) CaptureBatchImageBalance(context.Context, *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	s.captures++
	return &service.BatchImageBalanceHoldResult{Applied: true}, nil
}

func (s *mediaUsageBillingStub) ReleaseBatchImageBalance(context.Context, *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return &service.BatchImageBalanceHoldResult{Applied: true}, nil
}

type mediaUsageWriterStub struct {
	err error
}

func (s *mediaUsageWriterStub) Create(context.Context, *service.UsageLog) (bool, error) {
	return false, s.err
}

func TestBuildMediaUsageLogUsesQuotedCostAndRequestModel(t *testing.T) {
	createdAt := time.Now().Add(-3 * time.Second)
	order := &Order{
		ID:        "media_test",
		UserID:    24,
		APIKeyID:  69,
		GroupID:   29,
		Request:   json.RawMessage(`{"contract_version":"media-gateway/v1","request":{"model":"gpt-image-2"}}`),
		CreatedAt: createdAt,
	}

	log := buildMediaUsageLog(order, 88, 0.02)

	require.Equal(t, "media:media_test", log.RequestID)
	require.Equal(t, int64(88), log.AccountID)
	require.Equal(t, int64(24), log.UserID)
	require.Equal(t, int64(69), log.APIKeyID)
	require.Equal(t, "gpt-image-2", log.Model)
	require.Equal(t, 0.02, log.TotalCost)
	require.Equal(t, 0.02, log.ActualCost)
	require.Equal(t, service.BillingTypeBalance, log.BillingType)
	require.Equal(t, service.RequestTypeSync, log.RequestType)
	require.Equal(t, "per_request", *log.BillingMode)
	require.Equal(t, 0, log.ImageCount)
	require.Nil(t, log.ImageSize)
	require.GreaterOrEqual(t, *log.DurationMs, 3000)
}

func TestMediaOrderModelFallsBackSafely(t *testing.T) {
	require.Equal(t, "gpt-image-2", mediaOrderModel(nil))
	require.Equal(t, "gpt-image-2", mediaOrderModel(json.RawMessage(`{"request":{}}`)))
}

func TestCaptureStopsBeforeSettlementWhenUsageLogFails(t *testing.T) {
	billing := &mediaUsageBillingStub{}
	runtime := &Runtime{
		cfg:       Config{UsageAccountID: 88},
		billing:   billing,
		usageLogs: &mediaUsageWriterStub{err: errors.New("usage database unavailable")},
	}
	order := &Order{
		ID:        "media_retry",
		UserID:    24,
		APIKeyID:  69,
		GroupID:   29,
		Amount:    "0.02",
		Request:   json.RawMessage(`{"request":{"model":"gpt-image-2"}}`),
		CreatedAt: time.Now(),
	}

	err := runtime.capture(context.Background(), order)

	require.ErrorContains(t, err, "record media usage")
	require.Equal(t, 1, billing.captures)
	require.Equal(t, 1, billing.applies)
}
