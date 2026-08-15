package media

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteEditMultipartPreservesReferenceOrder(t *testing.T) {
	files := referenceFileHeaders(t, []string{"first", "second", "third"})
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	require.NoError(t, writeEditMultipart(writer, map[string]any{
		"prompt": "ordered references", "n": float64(1), "assets": []any{"not-forwarded"},
	}, files))
	require.NoError(t, writer.Close())

	form, err := multipart.NewReader(bytes.NewReader(payload.Bytes()), writer.Boundary()).ReadForm(1 << 20)
	require.NoError(t, err)
	defer func() { require.NoError(t, form.RemoveAll()) }()
	require.Equal(t, []string{"ordered references"}, form.Value["prompt"])
	require.Equal(t, []string{"1"}, form.Value["n"])
	require.NotContains(t, form.Value, "assets")
	require.Len(t, form.File["image[]"], 3)
	for index, header := range form.File["image[]"] {
		file, openErr := header.Open()
		require.NoError(t, openErr)
		body, readErr := io.ReadAll(file)
		require.NoError(t, readErr)
		require.NoError(t, file.Close())
		require.Equal(t, []byte([]string{"first", "second", "third"}[index]), body)
	}
}

func TestProxyUploadForwardsCustomerCredentialAndStreamsThePart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPut, r.Method)
		require.Equal(t, "/v1/media/uploads/signed-token/parts/1", r.URL.Path)
		require.Equal(t, "Bearer customer-sk", r.Header.Get("Authorization"))
		require.Equal(t, "application/octet-stream", r.Header.Get("Content-Type"))
		payload, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, []byte("reference-media"), payload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"object\":\"media_upload_part\",\"part_number\":1}"))
	}))
	defer server.Close()

	client := &GateClient{
		cfg:    Config{GateBaseURL: server.URL},
		client: server.Client(),
	}
	response, err := client.ProxyUpload(
		context.Background(),
		http.MethodPut,
		"/v1/media/uploads/signed-token/parts/1",
		"Bearer customer-sk",
		"application/octet-stream",
		int64(len("reference-media")),
		strings.NewReader("reference-media"),
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.Status)
	require.Equal(t, "application/json", response.ContentType)
	require.JSONEq(t, "{\"object\":\"media_upload_part\",\"part_number\":1}", string(response.Body))
}

func referenceFileHeaders(t *testing.T, values []string) []*multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for index, value := range values {
		part, err := writer.CreateFormFile("image[]", string(rune('a'+index))+".png")
		require.NoError(t, err)
		_, err = io.Copy(part, strings.NewReader(value))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	require.NoError(t, request.ParseMultipartForm(1<<20))
	t.Cleanup(func() { _ = request.MultipartForm.RemoveAll() })
	return request.MultipartForm.File["image[]"]
}
