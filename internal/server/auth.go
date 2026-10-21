package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/api"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"golang.org/x/crypto/bcrypt"
)

const issuer = "soa-marketplace"
const accessLifetime = 20 * time.Minute
const refreshLifetime = 14 * 24 * time.Hour

// These are internal JWT claims, not request/response DTOs.
type tokenClaims struct {
	jwt.RegisteredClaims
	Role      string `json:"role"`
	TokenType string `json:"token_type"`
}

func (s *Server) bootstrap(ctx context.Context) error {
	if s.cfg.AdminEmail == "" && s.cfg.AdminPassword == "" {
		return nil
	}
	if s.cfg.AdminEmail == "" || utf8.RuneCountInString(s.cfg.AdminPassword) < 12 || len(s.cfg.AdminPassword) > 72 {
		return errors.New("bootstrap admin needs an email and a password of at least 12 characters and at most 72 UTF-8 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO users(email,password_hash,role) VALUES ($1,$2,'ADMIN') ON CONFLICT(email) DO NOTHING`, strings.ToLower(s.cfg.AdminEmail), string(hash))
	return err
}

func checkPasswordSize(password string) error {
	if len(password) > 72 {
		return fieldError("password", "Must contain at most 72 UTF-8 bytes")
	}
	return nil
}

func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
	var request api.RegisterRequest
	if err := decodeBody(r, &request); err != nil {
		writeError(w, err)
		return
	}
	if err := checkPasswordSize(request.Password); err != nil {
		writeError(w, err)
		return
	}
	if request.Role != api.Role("USER") && request.Role != api.Role("SELLER") {
		writeError(w, fieldError("role", "Only USER and SELLER may self-register"))
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, err)
		return
	}
	email := strings.ToLower(string(request.Email))
	response := api.UserResponse{Email: openapi_types.Email(email), Role: request.Role}
	err = s.pool.QueryRow(r.Context(), `INSERT INTO users(email,password_hash,role) VALUES($1,$2,$3) ON CONFLICT(email) DO NOTHING RETURNING id`, email, string(hash), string(request.Role)).Scan(&response.Id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = apiError(409, "EMAIL_ALREADY_EXISTS", "Email already registered", nil)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var request api.LoginRequest
	if err := decodeBody(r, &request); err != nil {
		writeError(w, err)
		return
	}
	if err := checkPasswordSize(request.Password); err != nil {
		writeError(w, err)
		return
	}
	var id int64
	var hash, role string
	err := s.pool.QueryRow(r.Context(), `SELECT id,password_hash,role FROM users WHERE email=$1`, strings.ToLower(string(request.Email))).Scan(&id, &hash, &role)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, err)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		hash = string(s.dummyHash)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(request.Password)) != nil || id == 0 {
		writeError(w, apiError(401, "INVALID_CREDENTIALS", "Incorrect email or password", nil))
		return
	}
	var response api.TokenResponse
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		var err error
		response, err = s.issueTokens(r.Context(), tx, id, role)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, response)
}

func invalidRefresh() error {
	return apiError(401, "REFRESH_TOKEN_INVALID", "Invalid, expired or already used refresh token", nil)
}

func (s *Server) Refresh(w http.ResponseWriter, r *http.Request) {
	var request api.RefreshRequest
	if err := decodeBody(r, &request); err != nil {
		writeError(w, err)
		return
	}
	claims, err := s.parseToken(request.RefreshToken)
	if err != nil || claims.TokenType != "refresh" {
		writeError(w, invalidRefresh())
		return
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || id < 1 {
		writeError(w, invalidRefresh())
		return
	}
	jti, err := uuid.Parse(claims.ID)
	if err != nil {
		writeError(w, invalidRefresh())
		return
	}
	var response api.TokenResponse
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		var revoked, valid bool
		var role string
		err := tx.QueryRow(r.Context(), `SELECT r.revoked,r.expires_at>clock_timestamp(),u.role FROM refresh_tokens r JOIN users u ON u.id=r.user_id WHERE r.jti=$1 AND r.user_id=$2 FOR UPDATE OF r`, jti.String(), id).Scan(&revoked, &valid, &role)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalidRefresh()
		}
		if err != nil {
			return err
		}
		if revoked || !valid {
			return invalidRefresh()
		}
		if _, err := tx.Exec(r.Context(), `UPDATE refresh_tokens SET revoked=TRUE WHERE jti=$1`, jti.String()); err != nil {
			return err
		}
		response, err = s.issueTokens(r.Context(), tx, id, role)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, response)
}

func (s *Server) issueTokens(ctx context.Context, tx pgx.Tx, id int64, role string) (api.TokenResponse, error) {
	now := time.Now().UTC().Truncate(time.Second)
	refreshID := uuid.NewString()
	expires := now.Add(refreshLifetime)
	if _, err := tx.Exec(ctx, `INSERT INTO refresh_tokens(jti,user_id,expires_at) VALUES($1,$2,$3)`, refreshID, id, expires); err != nil {
		return api.TokenResponse{}, err
	}
	access, err := s.signToken(id, role, "access", uuid.NewString(), now, now.Add(accessLifetime))
	if err != nil {
		return api.TokenResponse{}, err
	}
	refresh, err := s.signToken(id, role, "refresh", refreshID, now, expires)
	if err != nil {
		return api.TokenResponse{}, err
	}
	return api.TokenResponse{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresIn: int64(accessLifetime / time.Second)}, nil
}

func (s *Server) signToken(id int64, role, kind, jti string, now, expiry time.Time) (string, error) {
	claims := tokenClaims{RegisteredClaims: jwt.RegisteredClaims{
		Issuer: issuer, Subject: strconv.FormatInt(id, 10), ID: jti,
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiry),
	}, Role: role, TokenType: kind}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTSecret))
}

func (s *Server) parseToken(raw string) (*tokenClaims, error) {
	claims := &tokenClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) { return []byte(s.cfg.JWTSecret), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	return claims, err
}

func (s *Server) authenticate(r *http.Request) error {
	header := strings.Fields(r.Header.Get("Authorization"))
	if len(header) != 2 || !strings.EqualFold(header[0], "Bearer") {
		return apiError(401, "TOKEN_INVALID", "Bearer access token is required", nil)
	}
	claims, err := s.parseToken(header[1])
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) && !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			return apiError(401, "TOKEN_EXPIRED", "Access token has expired", nil)
		}
		return apiError(401, "TOKEN_INVALID", "Invalid access token", nil)
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || id < 1 || claims.TokenType != "access" || (claims.Role != "USER" && claims.Role != "SELLER" && claims.Role != "ADMIN") {
		return apiError(401, "TOKEN_INVALID", "Invalid access token", nil)
	}
	state := r.Context().Value(requestContextKey{}).(*requestState)
	state.user = &Principal{ID: id, Role: claims.Role}
	return nil
}
