package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type Runtime struct {
	cfg          Config
	repo         *Repository
	gate         *GateClient
	billing      service.UsageBillingRepository
	usageLogs    UsageLogWriter
	authCache    service.APIKeyAuthCacheInvalidator
	billingCache *service.BillingCacheService
	stop         chan struct{}
	done         chan struct{}
	stopOnce     sync.Once
}

type gateExecutionEnvelope struct {
	SettlementAction     string `json:"settlement_action"`
	ManualReviewRequired bool   `json:"manual_review_required"`
	Projection           struct {
		QueueState     string `json:"queue_state"`
		ChargeState    string `json:"charge_state"`
		DeliveryState  string `json:"delivery_state"`
		ExecutionState string `json:"execution_state"`
	} `json:"projection"`
}

func NewRuntime(
	repo *Repository,
	billing service.UsageBillingRepository,
	authCache *service.APIKeyService,
	billingCache *service.BillingCacheService,
	usageLogs UsageLogWriter,
) (*Runtime, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	signer, err := NewAssertionSigner(cfg)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		cfg: cfg, repo: repo, gate: NewGateClient(cfg, signer), billing: billing,
		usageLogs: usageLogs, authCache: authCache, billingCache: billingCache,
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	if cfg.Enabled {
		go runtime.reconcileLoop()
	} else {
		close(runtime.done)
	}
	return runtime, nil
}

type UsageLogWriter interface {
	Create(context.Context, *service.UsageLog) (bool, error)
}

func (r *Runtime) Enabled() bool {
	return r != nil && r.cfg.Enabled
}

func (r *Runtime) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		if r.cfg.Enabled {
			close(r.stop)
			<-r.done
		}
	})
}

func (r *Runtime) Quote(ctx context.Context, apiKey *service.APIKey, request json.RawMessage) (json.RawMessage, error) {
	identity, err := validateMediaAPIKey(apiKey)
	if err != nil {
		return nil, err
	}
	response, err := r.gate.JSON(ctx, http.MethodPost, "/v1/media/quotes", "media:quotes:write", identity, request)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		QuoteID    string `json:"quote_id"`
		Operation  string `json:"operation"`
		QuoteToken string `json:"quote_token"`
		ExpiresAt  string `json:"expires_at"`
		Price      struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"price"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, fmt.Errorf("decode Gate quote: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339, envelope.ExpiresAt)
	if err != nil || envelope.QuoteID == "" || envelope.QuoteToken == "" ||
		envelope.Price.Currency != "CNY" {
		return nil, errors.New("gate returned an invalid quote contract")
	}
	amount, err := decimal.NewFromString(envelope.Price.Amount)
	if err != nil || !amount.IsPositive() {
		return nil, errors.New("gate returned an invalid quote amount")
	}
	groupID := int64(0)
	if apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	if err := r.repo.CreateQuote(ctx, &Quote{
		ID: envelope.QuoteID, UserID: identity.UserID, APIKeyID: identity.APIKeyID,
		GroupID: groupID, Operation: envelope.Operation, Request: request,
		GateToken: envelope.QuoteToken, GateResponse: response,
		Amount: amount.StringFixed(8), Currency: "CNY", ExpiresAt: expiresAt,
	}); err != nil {
		return nil, err
	}
	return removeJSONFields(response, "quote_token", "caller_id", "tenant_subject", "billing_subject")
}

func (r *Runtime) Models(ctx context.Context, apiKey *service.APIKey) (json.RawMessage, error) {
	identity, err := validateMediaAPIKey(apiKey)
	if err != nil {
		return nil, err
	}
	return r.gate.JSON(ctx, http.MethodGet, "/v1/media/models", "media:catalog:read", identity, nil)
}

func (r *Runtime) SubmitOrder(
	ctx context.Context,
	apiKey *service.APIKey,
	quoteID, clientIdempotencyKey string,
	files []*multipart.FileHeader,
) (*Order, bool, error) {
	identity, err := validateMediaAPIKey(apiKey)
	if err != nil {
		return nil, false, err
	}
	quote, err := r.repo.GetQuote(ctx, quoteID, identity)
	if err != nil {
		return nil, false, err
	}
	if quote.Operation == "image.edit" {
		if err := validateReferenceImages(quote.Request, files); err != nil {
			return nil, false, err
		}
	} else if len(files) != 0 {
		return nil, false, errors.New("reference images are valid only for image.edit quotes")
	}
	orderID := "media_" + compactUUID()
	order, replay, err := r.repo.CreateOrder(ctx, &Order{
		ID: orderID, UserID: identity.UserID, APIKeyID: identity.APIKeyID,
		GroupID: quote.GroupID, QuoteID: quote.ID, ClientIdempotencyKey: clientIdempotencyKey,
		GateIdempotencyKey: orderID, Operation: quote.Operation, Request: quote.Request,
		Amount: quote.Amount, Currency: quote.Currency,
	})
	if err != nil {
		return order, replay, err
	}
	if replay && (order.SettlementState == "captured" || order.SettlementState == "released") {
		return order, true, nil
	}
	if order.SettlementState == "unreserved" {
		if reserveErr := r.reserve(ctx, order); reserveErr != nil {
			if errors.Is(reserveErr, service.ErrBatchImageInsufficientBalance) {
				if markErr := r.repo.MarkReservationRejected(ctx, order.ID,
					"insufficient_balance", reserveErr.Error()); markErr != nil {
					return nil, false, errors.Join(reserveErr, markErr)
				}
			}
			return nil, false, reserveErr
		}
	}

	response, err := r.submitToGate(ctx, order, quote, files)
	if err != nil {
		recovered, lookupErr := r.gate.ExecutionByIdempotency(
			ctx, order.Identity(), order.GateIdempotencyKey)
		if lookupErr == nil {
			if markErr := r.repo.MarkAccepted(ctx, order.ID, recovered); markErr != nil {
				return nil, false, markErr
			}
			accepted, getErr := r.repo.GetOrderInternal(ctx, order.ID)
			return accepted, replay, getErr
		}
		var gateErr *GateError
		var lookupGateErr *GateError
		definitiveMiss := errors.As(lookupErr, &lookupGateErr) && lookupGateErr.Status == http.StatusNotFound
		if errors.As(err, &gateErr) && gateErr.Status >= 400 && gateErr.Status < 500 && definitiveMiss {
			_ = r.repo.MarkSubmissionUnknown(ctx, order.ID, gateErr.Code, string(gateErr.Body))
			_ = r.repo.MarkManualReview(ctx, order.ID, gateErr.Code, "Gate rejected the held order before exposing an execution")
			current, getErr := r.repo.GetOrderInternal(ctx, order.ID)
			return current, false, errors.Join(err, getErr)
		}
		_ = r.repo.MarkSubmissionUnknown(ctx, order.ID, "gate_submission_unknown", err.Error())
		current, getErr := r.repo.GetOrderInternal(ctx, order.ID)
		return current, false, errors.Join(err, getErr)
	}
	if err := r.repo.MarkAccepted(ctx, order.ID, response); err != nil {
		return nil, false, err
	}
	accepted, err := r.repo.GetOrderInternal(ctx, order.ID)
	if err == nil {
		_ = r.refreshAndSettle(ctx, accepted)
		accepted, err = r.repo.GetOrderInternal(ctx, order.ID)
	}
	return accepted, false, err
}

func (r *Runtime) ProxyUpload(ctx context.Context, apiKey *service.APIKey, request *http.Request, path string) (*GateProxyResponse, error) {
	if _, err := validateMediaAPIKey(apiKey); err != nil {
		return nil, err
	}
	return r.gate.ProxyUpload(ctx, request.Method, path, request.Header.Get("Authorization"),
		request.Header.Get("Content-Type"), request.ContentLength, request.Body)
}

func (r *Runtime) GetOrder(ctx context.Context, apiKey *service.APIKey, orderID string) (*Order, error) {
	identity, err := validateMediaAPIKey(apiKey)
	if err != nil {
		return nil, err
	}
	order, err := r.repo.GetOrder(ctx, orderID, identity)
	if err != nil {
		return nil, err
	}
	return order, nil
}

func (r *Runtime) AuthorizeArtifact(
	ctx context.Context,
	apiKey *service.APIKey,
	orderID, artifactID string,
) (json.RawMessage, error) {
	order, err := r.GetOrder(ctx, apiKey, orderID)
	if err != nil {
		return nil, err
	}
	if order.SettlementState != "captured" {
		return nil, ErrArtifactNotReady
	}
	if !orderHasArtifact(order, artifactID) {
		return nil, ErrArtifactNotFound
	}
	body := map[string]any{"order_id": order.ID, "action": "read", "expires_in": 900}
	response, err := r.gate.JSON(ctx, http.MethodPost,
		"/v1/media/artifacts/"+artifactID+"/authorizations",
		"media:artifacts:authorize", order.Identity(), body)
	if err != nil {
		return nil, err
	}
	var authorization struct {
		ExpiresAt  string `json:"expires_at"`
		ContentURL string `json:"content_url"`
	}
	if err := json.Unmarshal(response, &authorization); err != nil {
		return nil, err
	}
	contentURL, err := url.Parse(authorization.ContentURL)
	if err != nil || authorization.ExpiresAt == "" || contentURL.Query().Get("token") == "" {
		return nil, ErrArtifactAuth
	}
	token := contentURL.Query().Get("token")
	publicURL, err := url.Parse(r.cfg.PublicBaseURL)
	if err != nil {
		return nil, err
	}
	contentURL.Scheme = publicURL.Scheme
	contentURL.Host = publicURL.Host
	contentURL.Path = "/v1/media/orders/" + url.PathEscape(order.ID) + "/artifacts/" + url.PathEscape(artifactID) + "/content"
	contentURL.RawQuery = "token=" + url.QueryEscape(token)
	return json.Marshal(map[string]any{
		"contract_version": "sub2api-media/v1",
		"object":           "media.artifact.authorization",
		"artifact_id":      artifactID,
		"action":           "read",
		"expires_at":       authorization.ExpiresAt,
		"content_url":      contentURL.String(),
	})
}

func (r *Runtime) ProxyArtifactContent(
	ctx context.Context,
	apiKey *service.APIKey,
	orderID, artifactID string,
	request *http.Request,
) (*http.Response, error) {
	order, err := r.GetOrder(ctx, apiKey, orderID)
	if err != nil {
		return nil, err
	}
	if order.SettlementState != "captured" {
		return nil, ErrArtifactNotReady
	}
	if !orderHasArtifact(order, artifactID) {
		return nil, ErrArtifactNotFound
	}
	token := strings.TrimSpace(request.URL.Query().Get("token"))
	if token == "" {
		return nil, ErrArtifactAuth
	}
	return r.gate.ProxyArtifactContent(ctx, artifactID, token, request.Header.Get("Range"))
}

func (r *Runtime) submitToGate(
	ctx context.Context,
	order *Order,
	quote *Quote,
	files []*multipart.FileHeader,
) (json.RawMessage, error) {
	var outer struct {
		ContractVersion string         `json:"contract_version"`
		Operation       string         `json:"operation"`
		SchemaVersion   string         `json:"schema_version"`
		Request         map[string]any `json:"request"`
	}
	if err := json.Unmarshal(quote.Request, &outer); err != nil {
		return nil, err
	}
	if order.Operation == "image.edit" {
		standard, err := r.gate.SubmitEdit(ctx, order.Identity(), order.ID, quote.GateToken, outer.Request, files)
		if err != nil {
			return nil, err
		}
		var task struct {
			TaskID string `json:"task_id"`
		}
		if json.Unmarshal(standard, &task) != nil || !strings.HasPrefix(task.TaskID, "imgtask_") {
			return nil, errors.New("gate edit response omitted its task identity")
		}
		executionID := "mexec_" + strings.TrimPrefix(task.TaskID, "imgtask_")
		return json.Marshal(map[string]any{
			"contract_version": "media-gateway/v1", "object": "media.execution",
			"execution_id": executionID, "order_id": order.ID,
			"projection": map[string]any{
				"admission_state": "accepted", "queue_state": "pending",
				"acceptance_state": "not_attempted", "execution_state": "pending",
				"charge_state": "unknown", "delivery_state": "pending",
			},
		})
	}
	body := map[string]any{
		"contract_version": outer.ContractVersion,
		"order_id":         order.ID, "idempotency_key": order.GateIdempotencyKey,
		"quote_token": quote.GateToken, "operation": outer.Operation,
		"schema_version": outer.SchemaVersion, "request": outer.Request,
	}
	return r.gate.JSON(ctx, http.MethodPost, "/v1/media/executions",
		"media:executions:write", order.Identity(), body)
}

func (r *Runtime) refreshAndSettle(ctx context.Context, order *Order) error {
	response, err := r.gate.ExecutionByIdempotency(ctx, order.Identity(), order.GateIdempotencyKey)
	if err != nil {
		var gateErr *GateError
		if errors.As(err, &gateErr) && gateErr.Status == http.StatusNotFound {
			return r.repo.MarkSubmissionUnknown(ctx, order.ID, "gate_execution_not_visible", "Gate has not exposed the idempotent execution yet")
		}
		_ = r.repo.MarkSubmissionUnknown(ctx, order.ID, "gate_reconcile_failed", err.Error())
		return err
	}
	if err := r.repo.UpdateProjection(ctx, order.ID, response); err != nil {
		return err
	}
	var envelope gateExecutionEnvelope
	if err := json.Unmarshal(response, &envelope); err != nil {
		return err
	}
	switch gateSettlementDecision(envelope) {
	case "capture":
		if err := r.repo.MarkSettlementPending(ctx, order.ID, "capture_pending"); err != nil {
			return err
		}
		return r.capture(ctx, order)
	case "release":
		if err := r.repo.MarkSettlementPending(ctx, order.ID, "release_pending"); err != nil {
			return err
		}
		return r.release(ctx, order)
	case "manual_review":
		return r.repo.MarkManualReview(ctx, order.ID, "manual_review_required", "Gate requires an explicit human settlement decision")
	}
	return nil
}

func gateSettlementDecision(envelope gateExecutionEnvelope) string {
	if envelope.SettlementAction == "capture" {
		return "capture"
	}
	if envelope.SettlementAction == "release" {
		return "release"
	}
	if envelope.SettlementAction != "" || envelope.ManualReviewRequired || envelope.Projection.QueueState == "terminal" {
		return "manual_review"
	}
	return ""
}

func (r *Runtime) capture(ctx context.Context, order *Order) error {
	amount, err := moneyFloat(order.Amount)
	if err != nil {
		return err
	}
	if _, err := r.billing.CaptureBatchImageBalance(ctx,
		holdCommand(order, service.BatchImageCaptureRequestID(order.ID), amount, amount)); err != nil {
		return err
	}
	_, err = r.billing.Apply(ctx, &service.UsageBillingCommand{
		RequestID: service.BatchImageCaptureRequestID(order.ID) + ":api_key",
		UserID:    order.UserID, APIKeyID: order.APIKeyID, Model: mediaOrderModel(order.Request),
		BillingType: service.BillingTypeBalance, ImageCount: mediaOrderImageCount(order), MediaType: mediaOrderType(order),
		APIKeyQuotaCost: amount, APIKeyRateLimitCost: amount,
		RequestPayloadHash: requestHash(order.Request),
	})
	if err != nil {
		return err
	}
	r.invalidateCustomer(ctx, order.UserID)
	if r.usageLogs == nil || r.cfg.UsageAccountID <= 0 {
		return errors.New("media usage log repository is not configured")
	}
	if _, err := r.usageLogs.Create(ctx, buildMediaUsageLog(order, r.cfg.UsageAccountID, amount)); err != nil {
		return fmt.Errorf("record media usage: %w", err)
	}
	return r.repo.MarkSettled(ctx, order.ID, "captured")
}

func mediaOrderType(order *Order) string {
	if order != nil && order.Operation == "video.generate" {
		return "video"
	}
	return "image"
}

func mediaOrderImageCount(order *Order) int {
	if mediaOrderType(order) == "image" {
		return 1
	}
	return 0
}

func buildMediaUsageLog(order *Order, accountID int64, amount float64) *service.UsageLog {
	duration := int(time.Since(order.CreatedAt).Milliseconds())
	if duration < 0 {
		duration = 0
	}
	requestedModel := mediaOrderModel(order.Request)
	imageCount := mediaOrderImageCount(order)
	videoCount, videoResolution, videoDuration := mediaOrderVideoUsage(order)
	billingMode := string(service.BillingModePerRequest)
	inboundEndpoint := "/v1/media/orders"
	upstreamEndpoint := "/v1/media/executions"
	groupID := order.GroupID
	return &service.UsageLog{
		UserID: order.UserID, APIKeyID: order.APIKeyID, AccountID: accountID,
		RequestID: "media:" + order.ID, Model: requestedModel, RequestedModel: requestedModel,
		GroupID: &groupID, TotalCost: amount, ActualCost: amount, RateMultiplier: 1,
		BillingType: service.BillingTypeBalance, RequestType: service.RequestTypeSync,
		ImageCount: imageCount, VideoCount: videoCount,
		VideoResolution: videoResolution, VideoDurationSeconds: videoDuration,
		DurationMs: &duration, BillingMode: &billingMode,
		InboundEndpoint: &inboundEndpoint, UpstreamEndpoint: &upstreamEndpoint,
		CreatedAt: order.CreatedAt,
	}
}

func mediaOrderVideoUsage(order *Order) (int, *string, *int) {
	if mediaOrderType(order) != "video" {
		return 0, nil, nil
	}
	var envelope struct {
		Request struct {
			Resolution      string `json:"resolution"`
			DurationSeconds int    `json:"duration_seconds"`
		} `json:"request"`
	}
	if order == nil || json.Unmarshal(order.Request, &envelope) != nil {
		return 1, nil, nil
	}
	resolution := strings.TrimSpace(envelope.Request.Resolution)
	duration := envelope.Request.DurationSeconds
	var resolutionValue *string
	var durationValue *int
	if resolution != "" {
		resolutionValue = &resolution
	}
	if duration > 0 {
		durationValue = &duration
	}
	return 1, resolutionValue, durationValue
}

func mediaOrderModel(request json.RawMessage) string {
	var envelope struct {
		Request struct {
			Model string `json:"model"`
		} `json:"request"`
	}
	if json.Unmarshal(request, &envelope) == nil {
		if model := strings.TrimSpace(envelope.Request.Model); model != "" {
			return model
		}
	}
	return "gpt-image-2"
}

func (r *Runtime) release(ctx context.Context, order *Order) error {
	amount, err := moneyFloat(order.Amount)
	if err != nil {
		return err
	}
	if _, err := r.billing.ReleaseBatchImageBalance(ctx,
		holdCommand(order, service.BatchImageReleaseRequestID(order.ID), amount, 0)); err != nil {
		return err
	}
	r.invalidateCustomer(ctx, order.UserID)
	return r.repo.MarkSettled(ctx, order.ID, "released")
}

func (r *Runtime) reserve(ctx context.Context, order *Order) error {
	amount, err := moneyFloat(order.Amount)
	if err != nil {
		return err
	}
	command := holdCommand(order, service.BatchImageHoldRequestID(order.ID), amount, 0)
	if _, err := r.billing.ReserveBatchImageBalance(ctx, command); err != nil {
		return err
	}
	r.invalidateCustomer(ctx, order.UserID)
	return r.repo.MarkHeld(ctx, order.ID)
}

func (r *Runtime) reconcileLoop() {
	defer close(r.done)
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), r.cfg.RequestTimeout)
			orders, err := r.repo.ListDueOrders(ctx, 50)
			if err == nil {
				for _, order := range orders {
					switch order.SettlementState {
					case "unreserved":
						if err := r.reserve(ctx, order); errors.Is(err, service.ErrBatchImageInsufficientBalance) {
							_ = r.repo.MarkReservationRejected(ctx, order.ID,
								"insufficient_balance", err.Error())
						}
					case "capture_pending":
						_ = r.capture(ctx, order)
					case "release_pending":
						_ = r.release(ctx, order)
					default:
						_ = r.refreshAndSettle(ctx, order)
					}
				}
			}
			cancel()
		}
	}
}

func validateMediaAPIKey(apiKey *service.APIKey) (CustomerIdentity, error) {
	if apiKey == nil || apiKey.User == nil || apiKey.Group == nil || apiKey.GroupID == nil {
		return CustomerIdentity{}, errors.New("a Media API key must be bound to a group")
	}
	if apiKey.Group.Platform != service.PlatformMedia {
		return CustomerIdentity{}, errors.New("the API key group is not a Media group")
	}
	if apiKey.Group.IsSubscriptionType() {
		return CustomerIdentity{}, errors.New("media groups support balance billing only")
	}
	return CustomerIdentity{UserID: apiKey.UserID, APIKeyID: apiKey.ID}, nil
}

func holdCommand(order *Order, requestID string, hold, actual float64) *service.BatchImageBalanceHoldCommand {
	return &service.BatchImageBalanceHoldCommand{
		RequestID: requestID, UserID: order.UserID, APIKeyID: order.APIKeyID,
		BatchID: order.ID, HoldAmount: hold, ActualAmount: actual,
		RequestPayloadHash: requestHash(order.Request),
	}
}

func (r *Runtime) invalidateCustomer(ctx context.Context, userID int64) {
	if r.authCache != nil {
		r.authCache.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if r.billingCache != nil {
		_ = r.billingCache.InvalidateUserBalance(ctx, userID)
	}
}

func moneyFloat(value string) (float64, error) {
	amount, err := decimal.NewFromString(value)
	if err != nil || !amount.IsPositive() {
		return 0, errors.New("invalid Media order amount")
	}
	result, exact := amount.Float64()
	if !exact && amount.Exponent() < -8 {
		return 0, errors.New("media order amount exceeds billing precision")
	}
	return result, nil
}

func requestHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func compactUUID() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func removeJSONFields(value json.RawMessage, fields ...string) (json.RawMessage, error) {
	var object map[string]any
	if err := json.Unmarshal(value, &object); err != nil {
		return nil, err
	}
	for _, field := range fields {
		delete(object, field)
	}
	return json.Marshal(object)
}

func validateReferenceImages(request json.RawMessage, files []*multipart.FileHeader) error {
	var outer struct {
		Request struct {
			Assets []struct {
				Index    int    `json:"index"`
				SHA256   string `json:"sha256"`
				Bytes    int64  `json:"bytes"`
				MimeType string `json:"mime_type"`
			} `json:"assets"`
		} `json:"request"`
	}
	if err := json.Unmarshal(request, &outer); err != nil {
		return err
	}
	if len(files) < 1 || len(files) > 16 || len(files) != len(outer.Request.Assets) {
		return errors.New("image.edit requires the exact quoted set of 1 to 16 ordered reference images")
	}
	for index, header := range files {
		asset := outer.Request.Assets[index]
		if asset.Index != index || header.Size != asset.Bytes {
			return fmt.Errorf("reference image %d does not match its quote descriptor", index)
		}
		source, err := header.Open()
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, source)
		_ = source.Close()
		if copyErr != nil {
			return copyErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != asset.SHA256 {
			return fmt.Errorf("reference image %d sha256 does not match its quote descriptor", index)
		}
		contentType := header.Header.Get("Content-Type")
		if asset.MimeType != "" && contentType != "" && asset.MimeType != contentType {
			return fmt.Errorf("reference image %d MIME type does not match its quote descriptor", index)
		}
	}
	return nil
}

func publicOrder(order *Order) map[string]any {
	result := map[string]any{
		"contract_version": "sub2api-media/v1",
		"object":            "media.order",
		"order_id":          order.ID,
		"quote_id":          order.QuoteID,
		"idempotency_key":   order.ClientIdempotencyKey,
		"operation":         order.Operation,
		"submission_state":  order.SubmissionState,
		"settlement_state":  order.SettlementState,
		"price":             map[string]any{"amount": order.Amount, "currency": order.Currency},
		"created_at":        order.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":        order.UpdatedAt.UTC().Format(time.RFC3339),
		"artifacts":         publicArtifacts(order.GateResponse),
	}
	if order.GateExecutionID.Valid {
		result["execution_id"] = order.GateExecutionID.String
	}
	if len(order.Projection) > 0 {
		var projection any
		if json.Unmarshal(order.Projection, &projection) == nil {
			result["projection"] = projection
		}
	}
	if len(order.GateResponse) > 0 {
		var response map[string]any
		if json.Unmarshal(order.GateResponse, &response) == nil {
			delete(response, "caller_id")
			delete(response, "tenant_subject")
			delete(response, "billing_subject")
			result["gate"] = response
		}
	}
	if order.ErrorCode.Valid {
		result["error"] = map[string]any{"code": order.ErrorCode.String, "message": order.ErrorMessage.String}
	}
	return result
}

func publicArtifacts(raw json.RawMessage) []any {
	var response map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &response) != nil {
		return []any{}
	}
	items, ok := response["artifacts"].([]any)
	if !ok {
		return []any{}
	}
	artifacts := make([]any, 0, len(items))
	for _, item := range items {
		artifact, ok := item.(map[string]any)
		if !ok {
			continue
		}
		artifactID, ok := artifact["artifact_id"].(string)
		if !ok || artifactID == "" {
			continue
		}
		public := map[string]any{"artifact_id": artifactID}
		for _, field := range []string{"state", "media_type", "size_bytes", "sha256", "width", "height"} {
			if value, exists := artifact[field]; exists {
				public[field] = value
			}
		}
		artifacts = append(artifacts, public)
	}
	return artifacts
}

func orderHasArtifact(order *Order, artifactID string) bool {
	for _, item := range publicArtifacts(order.GateResponse) {
		artifact, ok := item.(map[string]any)
		if ok && artifact["artifact_id"] == artifactID {
			return true
		}
	}
	return false
}
