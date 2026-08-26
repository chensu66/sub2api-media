package media

import (
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

const maxQuoteBodyBytes = 1 << 20
const maxOrderBodyBytes = 128 << 20
const maxUploadProxyBodyBytes = 9 << 20

var mediaIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Handler struct {
	runtime *Runtime
}

func NewHandler(runtime *Runtime) *Handler {
	return &Handler{runtime: runtime}
}

func (h *Handler) Models(c *gin.Context) {
	if !h.available(c) {
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		mediaError(c, http.StatusUnauthorized, "authentication_error", "A valid API key is required.")
		return
	}
	response, err := h.runtime.Models(c.Request.Context(), apiKey)
	if err != nil {
		h.writeError(c, err)
		return
	}
	writeRawJSON(c, http.StatusOK, response)
}

func (h *Handler) Quote(c *gin.Context) {
	if !h.available(c) {
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		mediaError(c, http.StatusUnauthorized, "authentication_error", "A valid API key is required.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxQuoteBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxQuoteBodyBytes || !json.Valid(body) {
		mediaError(c, http.StatusBadRequest, "invalid_request", "A valid Media quote JSON body is required.")
		return
	}
	response, err := h.runtime.Quote(c.Request.Context(), apiKey, body)
	if err != nil {
		h.writeError(c, err)
		return
	}
	writeRawJSON(c, http.StatusCreated, response)
}

func (h *Handler) CreateOrder(c *gin.Context) {
	if !h.available(c) {
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		mediaError(c, http.StatusUnauthorized, "authentication_error", "A valid API key is required.")
		return
	}
	quoteID, idempotencyKey, files, err := parseOrderInput(c)
	if err != nil {
		mediaError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if c.Request.MultipartForm != nil {
		defer func() { _ = c.Request.MultipartForm.RemoveAll() }()
	}
	if !mediaIdentifier.MatchString(quoteID) || !mediaIdentifier.MatchString(idempotencyKey) {
		mediaError(c, http.StatusBadRequest, "invalid_request", "quote_id and idempotency_key must be valid identifiers.")
		return
	}
	order, replay, err := h.runtime.SubmitOrder(c.Request.Context(), apiKey, quoteID, idempotencyKey, files)
	if err != nil {
		h.writeError(c, err)
		return
	}
	status := http.StatusAccepted
	if replay {
		status = http.StatusOK
	}
	c.JSON(status, publicOrder(order))
}

func (h *Handler) GetOrder(c *gin.Context) {
	if !h.available(c) {
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		mediaError(c, http.StatusUnauthorized, "authentication_error", "A valid API key is required.")
		return
	}
	order, err := h.runtime.GetOrder(c.Request.Context(), apiKey, c.Param("order_id"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, publicOrder(order))
}

func (h *Handler) AuthorizeArtifact(c *gin.Context) {
	if !h.available(c) {
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		mediaError(c, http.StatusUnauthorized, "authentication_error", "A valid API key is required.")
		return
	}
	response, err := h.runtime.AuthorizeArtifact(
		c.Request.Context(), apiKey, c.Param("order_id"), c.Param("artifact_id"))
	if err != nil {
		h.writeError(c, err)
		return
	}
	writeRawJSON(c, http.StatusOK, response)
}

func (h *Handler) ArtifactContent(c *gin.Context) {
	if !h.available(c) {
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		mediaError(c, http.StatusUnauthorized, "authentication_error", "A valid API key is required.")
		return
	}
	response, err := h.runtime.ProxyArtifactContent(
		c.Request.Context(), apiKey, c.Param("order_id"), c.Param("artifact_id"), c.Request)
	if err != nil {
		h.writeError(c, err)
		return
	}
	defer func() { _ = response.Body.Close() }()
	for _, header := range []string{
		"Accept-Ranges", "Cache-Control", "Content-Disposition", "Content-Length",
		"Content-Range", "Content-Type", "ETag", "Retry-After", "X-Content-Type-Options",
	} {
		if value := response.Header.Get(header); value != "" {
			c.Header(header, value)
		}
	}
	c.Status(response.StatusCode)
	if c.Request.Method != http.MethodHead {
		_, _ = io.Copy(c.Writer, response.Body)
	}
}

func (h *Handler) ProxyUpload(c *gin.Context) {
	if !h.available(c) {
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok {
		mediaError(c, http.StatusUnauthorized, "authentication_error", "A valid API key is required.")
		return
	}
	if c.Request.ContentLength > maxUploadProxyBodyBytes {
		mediaError(c, http.StatusRequestEntityTooLarge, "request_too_large", "The Media upload request is too large.")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadProxyBodyBytes)
	path := "/v1/media/uploads"
	if uploadID := c.Param("upload_id"); uploadID != "" {
		path += "/" + uploadID
		if partNumber := c.Param("part_number"); partNumber != "" {
			path += "/parts/" + partNumber
		} else if c.Request.Method == http.MethodPost {
			path += "/complete"
		}
	}
	response, err := h.runtime.ProxyUpload(c.Request.Context(), apiKey, c.Request, path)
	if err != nil {
		h.writeError(c, err)
		return
	}
	contentType := response.ContentType
	if contentType == "" {
		contentType = "application/json; charset=utf-8"
	}
	c.Data(response.Status, contentType, response.Body)
}

func (h *Handler) available(c *gin.Context) bool {
	if h == nil || h.runtime == nil || !h.runtime.Enabled() {
		mediaError(c, http.StatusServiceUnavailable, "media_unavailable", "The Media platform is not enabled.")
		return false
	}
	return true
}

func (h *Handler) writeError(c *gin.Context, err error) {
	var gateErr *GateError
	switch {
	case errors.Is(err, ErrNotFound):
		mediaError(c, http.StatusNotFound, "not_found", "The Media quote or order does not exist.")
	case errors.Is(err, ErrArtifactNotFound):
		mediaError(c, http.StatusNotFound, "artifact_not_found", "The Media artifact does not exist for this order.")
	case errors.Is(err, ErrArtifactNotReady):
		mediaError(c, http.StatusConflict, "artifact_not_ready", "The Media artifact is not ready for download.")
	case errors.Is(err, ErrArtifactAuth):
		mediaError(c, http.StatusUnauthorized, "invalid_artifact_authorization", "The Media artifact authorization is invalid or expired.")
	case errors.Is(err, ErrQuoteExpired):
		mediaError(c, http.StatusConflict, "quote_expired", err.Error())
	case errors.Is(err, ErrQuoteConsumed), errors.Is(err, ErrIdempotencyConflict):
		mediaError(c, http.StatusConflict, "idempotency_conflict", err.Error())
	case errors.As(err, &gateErr):
		status := gateErr.Status
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		code := gateErr.Code
		if code == "" {
			code = "gate_error"
		}
		mediaError(c, status, code, "Gate rejected the Media request.")
	default:
		if strings.Contains(strings.ToLower(err.Error()), "insufficient") {
			mediaError(c, http.StatusPaymentRequired, "insufficient_balance", "The account balance is insufficient for this Media quote.")
			return
		}
		mediaError(c, http.StatusBadGateway, "media_request_failed", err.Error())
	}
}

func parseOrderInput(c *gin.Context) (string, string, []*multipart.FileHeader, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxOrderBodyBytes)
	contentType := c.GetHeader("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
			return "", "", nil, errors.New("invalid multipart Media order")
		}
		form := c.Request.MultipartForm
		if len(form.File["image"]) > 0 && len(form.File["image[]"]) > 0 {
			return "", "", nil, errors.New("use either image or image[] for ordered references, not both")
		}
		files := form.File["image[]"]
		if len(files) == 0 {
			files = form.File["image"]
		}
		return c.PostForm("quote_id"), c.PostForm("idempotency_key"), files, nil
	}
	var input struct {
		QuoteID        string `json:"quote_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", "", nil, errors.New("invalid Media order JSON")
	}
	return input.QuoteID, input.IdempotencyKey, nil, nil
}

func mediaError(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "media_error", "code": code, "message": message}})
}

func writeRawJSON(c *gin.Context, status int, body json.RawMessage) {
	c.Data(status, "application/json; charset=utf-8", body)
}
