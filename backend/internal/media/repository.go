package media

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound            = errors.New("media record not found")
	ErrQuoteConsumed       = errors.New("media quote already consumed")
	ErrQuoteExpired        = errors.New("media quote expired")
	ErrIdempotencyConflict = errors.New("media idempotency key conflict")
	ErrArtifactNotFound    = errors.New("media artifact not found")
	ErrArtifactNotReady    = errors.New("media artifact not ready")
	ErrArtifactAuth        = errors.New("media artifact authorization is invalid")
	errorsDisabled         = errors.New("media platform is disabled")
)

type Quote struct {
	ID            string
	UserID        int64
	APIKeyID      int64
	GroupID       int64
	Operation     string
	Request       json.RawMessage
	GateToken     string
	GateResponse  json.RawMessage
	Amount        string
	Currency      string
	ExpiresAt     time.Time
	ConsumedOrder sql.NullString
}

type Order struct {
	ID                   string
	UserID               int64
	APIKeyID             int64
	GroupID              int64
	QuoteID              string
	ClientIdempotencyKey string
	GateIdempotencyKey   string
	GateExecutionID      sql.NullString
	Operation            string
	Request              json.RawMessage
	Amount               string
	Currency             string
	SubmissionState      string
	SettlementState      string
	Projection           json.RawMessage
	GateResponse         json.RawMessage
	ErrorCode            sql.NullString
	ErrorMessage         sql.NullString
	CreatedAt            time.Time
	UpdatedAt            time.Time
	TerminalAt           sql.NullTime
}

func (o *Order) Identity() CustomerIdentity {
	return CustomerIdentity{UserID: o.UserID, APIKeyID: o.APIKeyID}
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateQuote(ctx context.Context, quote *Quote) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO media_quotes (
			quote_id, user_id, api_key_id, group_id, operation, request_json,
			gate_quote_token, gate_response_json, amount, currency, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8::jsonb, $9::numeric, $10, $11)
	`, quote.ID, quote.UserID, quote.APIKeyID, quote.GroupID, quote.Operation, []byte(quote.Request),
		quote.GateToken, []byte(quote.GateResponse), quote.Amount, quote.Currency, quote.ExpiresAt)
	return err
}

func (r *Repository) GetQuote(ctx context.Context, quoteID string, identity CustomerIdentity) (*Quote, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT quote_id, user_id, api_key_id, group_id, operation, request_json,
			gate_quote_token, gate_response_json, amount::text, currency, expires_at, consumed_order_id
		FROM media_quotes WHERE quote_id = $1 AND user_id = $2 AND api_key_id = $3
	`, quoteID, identity.UserID, identity.APIKeyID)
	return scanQuote(row)
}

func (r *Repository) CreateOrder(ctx context.Context, order *Order) (*Order, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := getOrderByIdempotency(ctx, tx, order.APIKeyID, order.ClientIdempotencyKey)
	if err == nil {
		if existing.QuoteID != order.QuoteID {
			return nil, false, ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}

	var consumed sql.NullString
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx, `
		SELECT consumed_order_id, expires_at FROM media_quotes
		WHERE quote_id = $1 AND user_id = $2 AND api_key_id = $3
		FOR UPDATE
	`, order.QuoteID, order.UserID, order.APIKeyID).Scan(&consumed, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if consumed.Valid {
		return nil, false, ErrQuoteConsumed
	}
	if !expiresAt.After(time.Now()) {
		return nil, false, ErrQuoteExpired
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO media_orders (
			order_id, user_id, api_key_id, group_id, quote_id, client_idempotency_key,
			gate_idempotency_key, operation, request_json, amount, currency
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::numeric, $11)
	`, order.ID, order.UserID, order.APIKeyID, order.GroupID, order.QuoteID,
		order.ClientIdempotencyKey, order.GateIdempotencyKey, order.Operation,
		[]byte(order.Request), order.Amount, order.Currency)
	if err != nil {
		return nil, false, err
	}
	_, err = tx.ExecContext(ctx,
		"UPDATE media_quotes SET consumed_order_id = $1 WHERE quote_id = $2 AND consumed_order_id IS NULL",
		order.ID, order.QuoteID)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	created, err := r.GetOrder(ctx, order.ID, order.Identity())
	return created, false, err
}

func (r *Repository) GetOrder(ctx context.Context, orderID string, identity CustomerIdentity) (*Order, error) {
	return scanOrder(r.db.QueryRowContext(ctx, orderSelect+`
		WHERE order_id = $1 AND user_id = $2 AND api_key_id = $3
	`, orderID, identity.UserID, identity.APIKeyID))
}

func (r *Repository) GetOrderInternal(ctx context.Context, orderID string) (*Order, error) {
	return scanOrder(r.db.QueryRowContext(ctx, orderSelect+" WHERE order_id = $1", orderID))
}

func (r *Repository) MarkHeld(ctx context.Context, orderID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE media_orders SET settlement_state = 'held', submission_state = 'submitting',
			next_reconcile_at = NOW(), updated_at = NOW()
		WHERE order_id = $1 AND settlement_state = 'unreserved'
	`, orderID)
	return err
}

func (r *Repository) MarkReservationRejected(ctx context.Context, orderID, code, message string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE media_orders SET submission_state = 'rejected', settlement_state = 'released',
			error_code = NULLIF($2, ''), error_message = NULLIF($3, ''), terminal_at = NOW(),
			next_reconcile_at = NOW() + INTERVAL '100 years', updated_at = NOW()
		WHERE order_id = $1 AND settlement_state = 'unreserved'
	`, orderID, code, truncate(message, 1000))
	return err
}

func (r *Repository) MarkSubmissionUnknown(ctx context.Context, orderID, code, message string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE media_orders SET submission_state = 'unknown', error_code = NULLIF($2, ''),
			error_message = NULLIF($3, ''), reconcile_attempts = reconcile_attempts + 1,
			next_reconcile_at = NOW() + INTERVAL '5 seconds', updated_at = NOW()
		WHERE order_id = $1
	`, orderID, code, truncate(message, 1000))
	return err
}

func (r *Repository) MarkManualReview(ctx context.Context, orderID, code, message string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE media_orders SET settlement_state = 'held', error_code = NULLIF($2, ''),
			error_message = NULLIF($3, ''), next_reconcile_at = NOW() + INTERVAL '30 seconds',
			updated_at = NOW()
		WHERE order_id = $1 AND settlement_state NOT IN ('captured', 'released')
	`, orderID, code, truncate(message, 1000))
	return err
}

func (r *Repository) MarkAccepted(ctx context.Context, orderID string, response json.RawMessage) error {
	var envelope struct {
		ExecutionID string          `json:"execution_id"`
		Projection  json.RawMessage `json:"projection"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return err
	}
	if envelope.ExecutionID == "" {
		return errors.New("gate execution response omitted execution_id")
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE media_orders SET submission_state = 'accepted', gate_execution_id = $2,
			projection_json = NULLIF($3::text, '')::jsonb, gate_response_json = $4::jsonb,
			error_code = NULL, error_message = NULL, next_reconcile_at = NOW() + INTERVAL '3 seconds',
			updated_at = NOW()
		WHERE order_id = $1
	`, orderID, envelope.ExecutionID, string(envelope.Projection), []byte(response))
	return err
}

func (r *Repository) UpdateProjection(ctx context.Context, orderID string, response json.RawMessage) error {
	var envelope struct {
		ExecutionID string          `json:"execution_id"`
		Projection  json.RawMessage `json:"projection"`
		Error       *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return err
	}
	var code, message string
	if envelope.Error != nil {
		code, message = envelope.Error.Code, envelope.Error.Message
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE media_orders SET gate_execution_id = COALESCE(NULLIF($2, ''), gate_execution_id),
			submission_state = 'accepted', projection_json = $3::jsonb, gate_response_json = $4::jsonb,
			error_code = NULLIF($5, ''), error_message = NULLIF($6, ''),
			reconcile_attempts = reconcile_attempts + 1,
			next_reconcile_at = NOW() + INTERVAL '3 seconds', updated_at = NOW()
		WHERE order_id = $1
	`, orderID, envelope.ExecutionID, []byte(envelope.Projection), []byte(response), code, truncate(message, 1000))
	return err
}

func (r *Repository) MarkSettlementPending(ctx context.Context, orderID, state string) error {
	if state != "capture_pending" && state != "release_pending" {
		return fmt.Errorf("invalid pending settlement state %q", state)
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE media_orders SET settlement_state = $2, next_reconcile_at = NOW(),
			updated_at = NOW() WHERE order_id = $1 AND settlement_state NOT IN ('captured', 'released')
	`, orderID, state)
	return err
}

func (r *Repository) MarkSettled(ctx context.Context, orderID, state string) error {
	if state != "captured" && state != "released" {
		return fmt.Errorf("invalid settlement state %q", state)
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE media_orders SET settlement_state = $2, terminal_at = NOW(),
			next_reconcile_at = NOW() + INTERVAL '100 years', updated_at = NOW()
		WHERE order_id = $1
	`, orderID, state)
	return err
}

func (r *Repository) ListDueOrders(ctx context.Context, limit int) ([]*Order, error) {
	rows, err := r.db.QueryContext(ctx, orderSelect+`
		WHERE settlement_state IN ('unreserved', 'held', 'capture_pending', 'release_pending')
			AND next_reconcile_at <= NOW()
		ORDER BY next_reconcile_at ASC LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var orders []*Order
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

const orderSelect = `
	SELECT order_id, user_id, api_key_id, group_id, quote_id, client_idempotency_key,
		gate_idempotency_key, gate_execution_id, operation, request_json, amount::text,
		currency, submission_state, settlement_state, projection_json, gate_response_json,
		error_code, error_message, created_at, updated_at, terminal_at
	FROM media_orders
`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanQuote(row rowScanner) (*Quote, error) {
	var quote Quote
	var request, response []byte
	err := row.Scan(&quote.ID, &quote.UserID, &quote.APIKeyID, &quote.GroupID, &quote.Operation,
		&request, &quote.GateToken, &response, &quote.Amount, &quote.Currency,
		&quote.ExpiresAt, &quote.ConsumedOrder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	quote.Request, quote.GateResponse = request, response
	return &quote, nil
}

func scanOrder(row rowScanner) (*Order, error) {
	var order Order
	var request, projection, response []byte
	err := row.Scan(&order.ID, &order.UserID, &order.APIKeyID, &order.GroupID, &order.QuoteID,
		&order.ClientIdempotencyKey, &order.GateIdempotencyKey, &order.GateExecutionID,
		&order.Operation, &request, &order.Amount, &order.Currency, &order.SubmissionState,
		&order.SettlementState, &projection, &response, &order.ErrorCode, &order.ErrorMessage,
		&order.CreatedAt, &order.UpdatedAt, &order.TerminalAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	order.Request, order.Projection, order.GateResponse = request, projection, response
	return &order, nil
}

func getOrderByIdempotency(ctx context.Context, tx *sql.Tx, apiKeyID int64, key string) (*Order, error) {
	return scanOrder(tx.QueryRowContext(ctx, orderSelect+`
		WHERE api_key_id = $1 AND client_idempotency_key = $2
	`, apiKeyID, key))
}

func truncate(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
