package media

import (
	"bytes"
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
	defer form.RemoveAll()
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
