package media

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestMarkReservationRejectedTerminatesUnreservedOrder(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectExec(regexp.QuoteMeta("UPDATE media_orders SET submission_state = 'rejected', settlement_state = 'released'")).
		WithArgs("media_1", "insufficient_balance", "insufficient balance").
		WillReturnResult(sqlmock.NewResult(0, 1))

	err = NewRepository(db).MarkReservationRejected(
		context.Background(), "media_1", "insufficient_balance", "insufficient balance")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListDueOrdersIncludesInterruptedReservations(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectQuery(`settlement_state IN \('unreserved', 'held', 'capture_pending', 'release_pending'\)`).
		WithArgs(50).
		WillReturnRows(sqlmock.NewRows([]string{
			"order_id", "user_id", "api_key_id", "group_id", "quote_id", "client_idempotency_key",
			"gate_idempotency_key", "gate_execution_id", "operation", "request_json", "amount",
			"currency", "submission_state", "settlement_state", "projection_json", "gate_response_json",
			"error_code", "error_message", "created_at", "updated_at", "terminal_at",
		}))

	orders, err := NewRepository(db).ListDueOrders(context.Background(), 50)
	require.NoError(t, err)
	require.Empty(t, orders)
	require.NoError(t, mock.ExpectationsWereMet())
}
