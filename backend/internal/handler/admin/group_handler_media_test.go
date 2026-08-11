package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGroupRequestsAcceptMediaPlatform(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("create", func(t *testing.T) {
		var req CreateGroupRequest
		err := bindGroupRequestJSON(`{"name":"Media","platform":"media","subscription_type":"standard"}`, &req)
		require.NoError(t, err)
		require.Equal(t, "media", req.Platform)
	})

	t.Run("update", func(t *testing.T) {
		var req UpdateGroupRequest
		err := bindGroupRequestJSON(`{"platform":"media"}`, &req)
		require.NoError(t, err)
		require.Equal(t, "media", req.Platform)
	})
}

func bindGroupRequestJSON(body string, target any) error {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	return context.ShouldBindJSON(target)
}
