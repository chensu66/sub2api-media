package media

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestAssertionSignerBindsOpaqueCustomerIdentity(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	signer, err := NewAssertionSigner(Config{
		Enabled: true, Issuer: "https://sub.example/internal/media",
		Audience: "gate-media", CallerID: "sub2api", KeyID: "media-test",
		PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	require.NoError(t, err)

	signed, err := signer.Sign("media:quotes:write", CustomerIdentity{UserID: 42, APIKeyID: 7})
	require.NoError(t, err)
	token, err := jwt.Parse(signed, func(token *jwt.Token) (any, error) {
		return &privateKey.PublicKey, nil
	}, jwt.WithAudience("gate-media"), jwt.WithIssuer("https://sub.example/internal/media"))
	require.NoError(t, err)
	claims := token.Claims.(jwt.MapClaims)
	require.Equal(t, "sub2api:user:42", claims["tenant_subject"])
	require.Equal(t, "sub2api:api-key:7", claims["billing_subject"])
	require.Equal(t, "media:quotes:write", claims["scope"])
}

func TestAssertionSignerAcceptsPrivateJWK(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	encode := func(value *big.Int) string {
		return base64.RawURLEncoding.EncodeToString(value.FillBytes(make([]byte, 32)))
	}
	jwk, err := json.Marshal(map[string]string{
		"kty": "EC", "crv": "P-256", "kid": "media-test",
		"x": encode(privateKey.X), "y": encode(privateKey.Y), "d": encode(privateKey.D),
	})
	require.NoError(t, err)
	signer, err := NewAssertionSigner(Config{
		Enabled: true, Issuer: "issuer", Audience: "audience", CallerID: "sub2api",
		KeyID: "media-test", PrivateKeyJWK: string(jwk),
	})
	require.NoError(t, err)
	signed, err := signer.Sign("media:catalog:read", CustomerIdentity{UserID: 1, APIKeyID: 2})
	require.NoError(t, err)
	_, err = jwt.Parse(signed, func(token *jwt.Token) (any, error) {
		return &privateKey.PublicKey, nil
	})
	require.NoError(t, err)
}

func TestValidateMediaAPIKeyRequiresBalanceMediaGroup(t *testing.T) {
	groupID := int64(3)
	key := &service.APIKey{
		ID: 7, UserID: 42, GroupID: &groupID, User: &service.User{ID: 42},
		Group: &service.Group{
			ID: groupID, Platform: service.PlatformMedia,
			SubscriptionType: service.SubscriptionTypeStandard,
		},
	}
	identity, err := validateMediaAPIKey(key)
	require.NoError(t, err)
	require.Equal(t, "sub2api:user:42", identity.TenantSubject())
	require.Equal(t, "sub2api:api-key:7", identity.BillingSubject())

	key.Group.SubscriptionType = service.SubscriptionTypeSubscription
	_, err = validateMediaAPIKey(key)
	require.ErrorContains(t, err, "balance billing only")
}
