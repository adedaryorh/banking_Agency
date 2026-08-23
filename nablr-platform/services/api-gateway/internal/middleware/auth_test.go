package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

const testUserID = "4f4afc38-13ef-4adb-97a0-817cd2ec4ff8"

func TestJWTAuthUsesIdentityTokenClaimsAndOverwritesSpoofedHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	token := signTestToken(t, "secret", "nabla", "buyer")
	router := gin.New()
	router.Use(JWTAuth("secret", "nabla"))
	router.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"user_id": c.GetHeader("X-User-ID"), "role": c.GetHeader("X-User-Role"), "email": c.GetHeader("X-User-Email"), "tier": c.GetHeader("X-KYC-Tier")})
	})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-User-ID", "attacker")
	req.Header.Set("X-User-Email", "attacker@example.com")
	req.Header.Set("X-KYC-Tier", "3")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	want := `{"email":"","role":"buyer","tier":"","user_id":"` + testUserID + `"}`
	if response.Body.String() != want {
		t.Fatalf("body=%s want=%s", response.Body.String(), want)
	}
}

func TestJWTAuthRejectsWrongIssuerAndMissingRole(t *testing.T) {
	for name, token := range map[string]string{"wrong issuer": signTestToken(t, "secret", "other", "buyer"), "missing role": signTestToken(t, "secret", "nabla", "")} {
		t.Run(name, func(t *testing.T) {
			router := gin.New()
			router.Use(JWTAuth("secret", "nabla"))
			router.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d", response.Code)
			}
		})
	}
}

func signTestToken(t *testing.T, secret, issuer, role string) string {
	t.Helper()
	claims := jwt.MapClaims{"sub": testUserID, "role": role, "iss": issuer, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}
	value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
