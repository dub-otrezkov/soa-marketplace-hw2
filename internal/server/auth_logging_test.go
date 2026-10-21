package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestAccessTokensEnforceSignatureIssuerTypeAndExpiry(t *testing.T) {
	s := &Server{cfg: Config{JWTSecret: strings.Repeat("x", 64)}}
	now := time.Now().UTC()
	for _, test := range []struct {
		name, kind, issuer, role, secret string
		expiry                           time.Time
		want                             string
	}{
		{"valid", "access", issuer, "USER", s.cfg.JWTSecret, now.Add(time.Minute), ""},
		{"expired", "access", issuer, "USER", s.cfg.JWTSecret, now.Add(-time.Minute), "TOKEN_EXPIRED"},
		{"refresh", "refresh", issuer, "USER", s.cfg.JWTSecret, now.Add(time.Minute), "TOKEN_INVALID"},
		{"issuer", "access", "other-service", "USER", s.cfg.JWTSecret, now.Add(time.Minute), "TOKEN_INVALID"},
		{"signature", "access", issuer, "USER", strings.Repeat("y", 64), now.Add(time.Minute), "TOKEN_INVALID"},
		{"role", "access", issuer, "ROOT", s.cfg.JWTSecret, now.Add(time.Minute), "TOKEN_INVALID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := tokenClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: test.issuer, Subject: "42", IssuedAt: jwt.NewNumericDate(now.Add(-time.Hour)), ExpiresAt: jwt.NewNumericDate(test.expiry)}, Role: test.role, TokenType: test.kind}
			raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(test.secret))
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/products", nil)
			r = r.WithContext(context.WithValue(r.Context(), requestContextKey{}, &requestState{}))
			r.Header.Set("Authorization", "Bearer "+raw)
			err = s.authenticate(r)
			if test.want == "" {
				if err != nil || principal(r).ID != 42 {
					t.Fatalf("valid token: principal=%+v error=%v", principal(r), err)
				}
				return
			}
			var failure *applicationError
			if !errors.As(err, &failure) || failure.code != test.want {
				t.Fatalf("expected %s, got %v", test.want, err)
			}
		})
	}
}

func TestPasswordLimitCountsUTF8Bytes(t *testing.T) {
	if err := checkPasswordSize(strings.Repeat("я", 36)); err != nil {
		t.Fatal(err)
	}
	if err := checkPasswordSize(strings.Repeat("я", 37)); err == nil {
		t.Fatal("bcrypt must not truncate a 74-byte password")
	}
}

func TestLogBodiesRecursivelyRedactSecrets(t *testing.T) {
	input := []byte(`{"email":"a@example.test","password":"top-secret","nested":[{"ACCESS_TOKEN":"secret-token","refresh_token":"refresh-secret"}],"quantity":3}`)
	encoded, err := json.Marshal(safeLogBody(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"top-secret", "secret-token", "refresh-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("log leaked %s", secret)
		}
	}
	if !strings.Contains(string(encoded), `"quantity":3`) || !strings.Contains(string(encoded), `"email":"a@example.test"`) {
		t.Fatalf("lost non-secret request fields: %s", encoded)
	}
}

func TestMalformedAndOversizedLogBodiesAreOmitted(t *testing.T) {
	for _, input := range []string{`{"password":"secret"`, `{"password":"secret"} {"x":1}`, strings.Repeat("x", logBodyLimit+1)} {
		result, ok := safeLogBody([]byte(input)).(string)
		if !ok || strings.Contains(result, "secret") || len(result) > 100 {
			t.Fatalf("unsafe log body: %v", result)
		}
	}
}
