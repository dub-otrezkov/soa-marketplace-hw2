// Package server implements the generated OpenAPI interface and transactional services.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"
)

func init() { decimal.MarshalJSONWithoutQuotes = true }

type Config struct {
	JWTSecret     string
	OrderCooldown time.Duration
	AdminEmail    string
	AdminPassword string
}

type Server struct {
	pool      *pgxpool.Pool
	cfg       Config
	dummyHash []byte
}

type Principal struct {
	ID   int64
	Role string
}

type requestContextKey struct{}
type requestState struct{ user *Principal }

func principal(r *http.Request) Principal {
	state, _ := r.Context().Value(requestContextKey{}).(*requestState)
	if state == nil || state.user == nil {
		return Principal{}
	}
	return *state.user
}

func New(ctx context.Context, pool *pgxpool.Pool, cfg Config) (*Server, error) {
	if len(cfg.JWTSecret) < 32 {
		return nil, errors.New("JWT_SECRET must contain at least 32 bytes")
	}
	if cfg.OrderCooldown < 0 {
		return nil, errors.New("order cooldown cannot be negative")
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte("dummy-password-not-an-account"), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	s := &Server{pool: pool, cfg: cfg, dummyHash: dummy}
	if err := s.bootstrap(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// inTx owns the transaction boundary; a service error always rolls back stock,
// promotion usage, order items and cooldown records together.
func (s *Server) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type applicationError struct {
	status  int
	code    string
	message string
	details map[string]any
}

func (e *applicationError) Error() string { return e.code + ": " + e.message }
func apiError(status int, code, message string, details map[string]any) error {
	return &applicationError{status: status, code: code, message: message, details: details}
}
func fieldError(field, message string) error {
	return apiError(http.StatusBadRequest, "VALIDATION_ERROR", "Request validation failed", map[string]any{
		"fields": []map[string]string{{"field": field, "message": message}},
	})
}
func writeError(w http.ResponseWriter, err error) {
	var failure *applicationError
	if !errors.As(err, &failure) {
		// Do not expose SQL arguments, password hashes or database internals.
		slog.Error("request failed", "error_type", fmt.Sprintf("%T", err))
		failure = &applicationError{status: 500, code: "INTERNAL_ERROR", message: "Internal server error"}
	}
	body := api.ErrorResponse{ErrorCode: api.ErrorCode(failure.code), Message: failure.message}
	if failure.details != nil {
		body.Details = &failure.details
	}
	writeJSON(w, failure.status, body)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func decodeBody(r *http.Request, out any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			return fieldError(typeError.Field, "Invalid JSON value type")
		}
		return fieldError("body", "Invalid JSON body")
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return fieldError("body", "Expected one JSON document")
	}
	return nil
}
