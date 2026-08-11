package media

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type CustomerIdentity struct {
	UserID   int64
	APIKeyID int64
}

func (i CustomerIdentity) TenantSubject() string {
	return fmt.Sprintf("sub2api:user:%d", i.UserID)
}

func (i CustomerIdentity) BillingSubject() string {
	return fmt.Sprintf("sub2api:api-key:%d", i.APIKeyID)
}

type AssertionSigner struct {
	cfg Config
	key *ecdsa.PrivateKey
}

func NewAssertionSigner(cfg Config) (*AssertionSigner, error) {
	if !cfg.Enabled {
		return &AssertionSigner{cfg: cfg}, nil
	}
	key, err := parsePrivateKey(cfg)
	if err != nil {
		return nil, fmt.Errorf("parse Media ES256 private key: %w", err)
	}
	return &AssertionSigner{cfg: cfg, key: key}, nil
}

func parsePrivateKey(cfg Config) (*ecdsa.PrivateKey, error) {
	if strings.TrimSpace(cfg.PrivateKeyPEM) != "" {
		return jwt.ParseECPrivateKeyFromPEM([]byte(cfg.PrivateKeyPEM))
	}
	var jwk struct {
		KTY string `json:"kty"`
		CRV string `json:"crv"`
		Kid string `json:"kid"`
		X   string `json:"x"`
		Y   string `json:"y"`
		D   string `json:"d"`
	}
	if err := json.Unmarshal([]byte(cfg.PrivateKeyJWK), &jwk); err != nil {
		return nil, err
	}
	if jwk.KTY != "EC" || jwk.CRV != "P-256" || jwk.Kid != cfg.KeyID {
		return nil, fmt.Errorf("private JWK must be P-256 and match key id %q", cfg.KeyID)
	}
	decode := func(name, value string) ([]byte, error) {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("private JWK %s must be a 32-byte base64url value", name)
		}
		return decoded, nil
	}
	xBytes, err := decode("x", jwk.X)
	if err != nil {
		return nil, err
	}
	yBytes, err := decode("y", jwk.Y)
	if err != nil {
		return nil, err
	}
	dBytes, err := decode("d", jwk.D)
	if err != nil {
		return nil, err
	}
	curve := elliptic.P256()
	x, y, d := new(big.Int).SetBytes(xBytes), new(big.Int).SetBytes(yBytes), new(big.Int).SetBytes(dBytes)
	derivedX, derivedY := curve.ScalarBaseMult(dBytes)
	if d.Sign() <= 0 || d.Cmp(curve.Params().N) >= 0 || !curve.IsOnCurve(x, y) ||
		derivedX.Cmp(x) != 0 || derivedY.Cmp(y) != 0 {
		return nil, fmt.Errorf("private JWK coordinates do not match its scalar")
	}
	return &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}, nil
}

func (s *AssertionSigner) Sign(scope string, identity CustomerIdentity) (string, error) {
	if s == nil || s.key == nil {
		return "", errorsDisabled
	}
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"iss":             s.cfg.Issuer,
		"sub":             s.cfg.CallerID,
		"caller_id":       s.cfg.CallerID,
		"aud":             s.cfg.Audience,
		"jti":             uuid.NewString(),
		"iat":             now.Unix(),
		"nbf":             now.Add(-5 * time.Second).Unix(),
		"exp":             now.Add(120 * time.Second).Unix(),
		"scope":           scope,
		"tenant_subject":  identity.TenantSubject(),
		"billing_subject": identity.BillingSubject(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = s.cfg.KeyID
	return token.SignedString(s.key)
}
