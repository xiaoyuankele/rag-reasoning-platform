package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	authapplication "rag-reasoning-platform/backend/internal/application/auth"
)

const testAPIOwnerUserID int64 = 42

// useTestAuthenticatedIdentity 模拟 AuthMiddleware 已经验证当前用户。
func useTestAuthenticatedIdentity(router *gin.Engine) {
	router.Use(func(c *gin.Context) {
		c.Set(
			authenticatedIdentityContextKey,
			authapplication.AuthenticatedIdentity{
				Actor: authapplication.Actor{UserID: testAPIOwnerUserID, SessionID: 7},
			},
		)
		c.Next()
	})
}

func TestOwnerScopeFromContextUsesAuthenticatedActor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(
		authenticatedIdentityContextKey,
		authapplication.AuthenticatedIdentity{
			Actor: authapplication.Actor{UserID: 42, SessionID: 7},
		},
	)

	scope, found := ownerScopeFromContext(c)
	if !found || scope.OwnerUserID() != 42 {
		t.Fatalf("ownerScopeFromContext() = (%+v, %t), want owner 42", scope, found)
	}
}

func TestOwnerScopeFromContextRejectsMissingAndInvalidIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		identity *authapplication.AuthenticatedIdentity
	}{
		{name: "missing identity"},
		{
			name: "invalid actor user ID",
			identity: &authapplication.AuthenticatedIdentity{
				Actor: authapplication.Actor{UserID: 0, SessionID: 7},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if test.identity != nil {
				c.Set(authenticatedIdentityContextKey, *test.identity)
			}
			if scope, found := ownerScopeFromContext(c); found || scope.IsValid() {
				t.Fatalf("ownerScopeFromContext() = (%+v, %t), want invalid", scope, found)
			}
		})
	}
}
