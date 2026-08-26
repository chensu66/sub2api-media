package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
)

type GateError struct {
	Status int
	Code   string
	Body   []byte
}

func (e *GateError) Error() string {
	return fmt.Sprintf("Gate request failed with HTTP %d (%s)", e.Status, e.Code)
}

type GateClient struct {
	cfg    Config
	signer *AssertionSigner
	client *http.Client
}

type GateProxyResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

func NewGateClient(cfg Config, signer *AssertionSigner) *GateClient {
	return &GateClient{
		cfg: cfg, signer: signer,
		client: &http.Client{
			Timeout: cfg.RequestTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *GateClient) JSON(ctx context.Context, method, path, scope string, identity CustomerIdentity, body any) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.GateBaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, scope, identity)
}

func (c *GateClient) SubmitEdit(
	ctx context.Context,
	identity CustomerIdentity,
	orderID, quoteToken string,
	request map[string]any,
	files []*multipart.FileHeader,
) (json.RawMessage, error) {
	reader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	go func() {
		err := writeEditMultipart(writer, request, files)
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		_ = pipeWriter.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.GateBaseURL+"/v1/images/edits", reader)
	if err != nil {
		_ = reader.Close()
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Gate-Media-Quote", quoteToken)
	req.Header.Set("X-Gate-Order-Id", orderID)
	req.Header.Set("Idempotency-Key", orderID)
	return c.do(req, "media:executions:write", identity)
}

func writeEditMultipart(writer *multipart.Writer, request map[string]any, files []*multipart.FileHeader) error {
	for name, value := range request {
		if name == "assets" || value == nil {
			continue
		}
		var field string
		switch typed := value.(type) {
		case string:
			field = typed
		case float64:
			field = strconv.FormatFloat(typed, 'f', -1, 64)
		case bool:
			field = strconv.FormatBool(typed)
		default:
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			field = string(encoded)
		}
		if err := writer.WriteField(name, field); err != nil {
			return err
		}
	}
	for _, header := range files {
		source, err := header.Open()
		if err != nil {
			return err
		}
		part, err := writer.CreateFormFile("image[]", header.Filename)
		if err == nil {
			_, err = io.Copy(part, source)
		}
		closeErr := source.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (c *GateClient) ExecutionByIdempotency(ctx context.Context, identity CustomerIdentity, key string) (json.RawMessage, error) {
	return c.JSON(ctx, http.MethodGet,
		"/v1/media/executions/by-idempotency-key/"+url.PathEscape(key),
		"media:executions:read", identity, nil)
}

func (c *GateClient) ProxyUpload(ctx context.Context, method, path, authorization, contentType string, contentLength int64, body io.Reader) (*GateProxyResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.GateBaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return &GateProxyResponse{
		Status: response.StatusCode, ContentType: response.Header.Get("Content-Type"), Body: payload,
	}, nil
}

func (c *GateClient) ProxyArtifactContent(ctx context.Context, artifactID, token, rangeValue string) (*http.Response, error) {
	path := "/v1/media/artifacts/" + url.PathEscape(artifactID) + "/content?token=" + url.QueryEscape(token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.GateBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if rangeValue != "" {
		req.Header.Set("Range", rangeValue)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &envelope)
		return nil, &GateError{Status: response.StatusCode, Code: envelope.Error.Code, Body: body}
	}
	return response, nil
}

func (c *GateClient) do(req *http.Request, scope string, identity CustomerIdentity) (json.RawMessage, error) {
	assertion, err := c.signer.Sign(scope, identity)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+assertion)
	req.Header.Set("Accept", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &envelope)
		return nil, &GateError{Status: response.StatusCode, Code: envelope.Error.Code, Body: body}
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("gate returned invalid JSON")
	}
	return json.RawMessage(body), nil
}
