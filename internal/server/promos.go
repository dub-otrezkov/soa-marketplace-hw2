package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/api"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"
)

// promoRecord is a persistence record, never an HTTP request or response DTO.
type promoRecord struct {
	id             int64
	code           string
	discountType   string
	discountValue  decimal.Decimal
	minOrderAmount decimal.Decimal
	maxUses        int32
	currentUses    int32
	validFrom      time.Time
	validUntil     time.Time
	active         bool
}

const promoColumns = `id, code, discount_type, discount_value::text,
    min_order_amount::text, max_uses, current_uses, valid_from, valid_until, active`

func (s *Server) CreatePromoCode(w http.ResponseWriter, r *http.Request) {
	actor := principal(r)
	if actor.Role != "SELLER" && actor.Role != "ADMIN" {
		writeError(w, apiError(http.StatusForbidden, "ACCESS_DENIED", "Role does not allow this operation", nil))
		return
	}
	var input api.PromoCodeCreate
	if err := decodeBody(r, &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.createPromo(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) createPromo(ctx context.Context, input api.PromoCodeCreate) (api.PromoCodeResponse, error) {
	var result api.PromoCodeResponse
	if err := validatePromoMoney("discount_value", input.DiscountValue, false); err != nil {
		return result, err
	}
	if err := validatePromoMoney("min_order_amount", input.MinOrderAmount, true); err != nil {
		return result, err
	}
	if !input.ValidUntil.After(input.ValidFrom) {
		return result, apiError(http.StatusBadRequest, "VALIDATION_ERROR", "The end of the promotion must be after its start",
			map[string]any{"fields": []any{map[string]any{"field": "valid_until", "message": "must be after valid_from"}}})
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO promo_codes
            (code, discount_type, discount_value, min_order_amount, max_uses, valid_from, valid_until, active)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			input.Code, string(input.DiscountType), input.DiscountValue.String(), input.MinOrderAmount.String(),
			input.MaxUses, input.ValidFrom, input.ValidUntil, input.Active).Scan(&id)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return apiError(http.StatusConflict, "PROMO_CODE_ALREADY_EXISTS", "A promotion with this code already exists",
					map[string]any{"fields": []any{map[string]any{"field": "code", "message": "must be unique"}}})
			}
			return err
		}
		promo, err := scanPromo(tx.QueryRow(ctx, "SELECT "+promoColumns+" FROM promo_codes WHERE id = $1", id))
		if err != nil {
			return err
		}
		result = promoResponse(promo)
		return nil
	})
	return result, err
}

func lockPromoByCode(ctx context.Context, tx pgx.Tx, code string) (promoRecord, error) {
	return scanPromo(tx.QueryRow(ctx, "SELECT "+promoColumns+" FROM promo_codes WHERE code = $1 FOR UPDATE", code))
}

func lockPromoByID(ctx context.Context, tx pgx.Tx, id int64) (promoRecord, error) {
	return scanPromo(tx.QueryRow(ctx, "SELECT "+promoColumns+" FROM promo_codes WHERE id = $1 FOR UPDATE", id))
}

func scanPromo(row pgx.Row) (promoRecord, error) {
	var promo promoRecord
	var value, minimum string
	err := row.Scan(&promo.id, &promo.code, &promo.discountType, &value, &minimum,
		&promo.maxUses, &promo.currentUses, &promo.validFrom, &promo.validUntil, &promo.active)
	if errors.Is(err, pgx.ErrNoRows) {
		return promo, invalidPromo()
	}
	if err != nil {
		return promo, err
	}
	promo.discountValue, err = decimal.NewFromString(value)
	if err != nil {
		return promo, err
	}
	promo.minOrderAmount, err = decimal.NewFromString(minimum)
	return promo, err
}

func validatePromo(promo promoRecord, alreadyApplied bool, now time.Time) error {
	if !promo.active || now.Before(promo.validFrom) || now.After(promo.validUntil) ||
		(!alreadyApplied && promo.currentUses >= promo.maxUses) {
		return invalidPromo()
	}
	return nil
}

func requirePromoMinimum(promo promoRecord, subtotal decimal.Decimal) error {
	if subtotal.LessThan(promo.minOrderAmount) {
		return apiError(http.StatusUnprocessableEntity, "PROMO_CODE_MIN_AMOUNT", "Order amount is below the promotion minimum",
			map[string]any{"min_order_amount": promo.minOrderAmount, "order_amount": subtotal})
	}
	return nil
}

func promoDiscount(promo promoRecord, subtotal decimal.Decimal) decimal.Decimal {
	if promo.discountType == "PERCENTAGE" {
		// Literal assignment rule: above 70%, the customer pays the full price.
		if promo.discountValue.GreaterThan(decimal.NewFromInt(70)) {
			return decimal.Zero
		}
		// Decimal division by 100 is exact; Round uses financial HALF_UP rounding.
		return subtotal.Mul(promo.discountValue).Shift(-2).Round(2)
	}
	return decimal.Min(promo.discountValue, subtotal).Round(2)
}

func usePromo(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, "UPDATE promo_codes SET current_uses = current_uses + 1 WHERE id = $1", id)
	return err
}

func releasePromo(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, "UPDATE promo_codes SET current_uses = current_uses - 1 WHERE id = $1", id)
	return err
}

func promoResponse(promo promoRecord) api.PromoCodeResponse {
	return api.PromoCodeResponse{
		Id: promo.id, Code: promo.code, DiscountType: api.DiscountType(promo.discountType),
		DiscountValue: promo.discountValue, MinOrderAmount: promo.minOrderAmount,
		MaxUses: promo.maxUses, CurrentUses: promo.currentUses,
		ValidFrom: promo.validFrom, ValidUntil: promo.validUntil, Active: promo.active,
	}
}

func invalidPromo() error {
	return apiError(http.StatusUnprocessableEntity, "PROMO_CODE_INVALID",
		"Promotion does not exist, is inactive, expired, not yet valid, or exhausted", nil)
}

func validatePromoMoney(field string, value decimal.Decimal, allowZero bool) error {
	if value.IsNegative() || (!allowZero && value.IsZero()) || !value.Equal(value.Round(2)) ||
		value.GreaterThan(decimal.RequireFromString("9999999999.99")) {
		return apiError(http.StatusBadRequest, "VALIDATION_ERROR", "Invalid decimal amount",
			map[string]any{"fields": []any{map[string]any{"field": field,
				"message": "must have at most two decimal places and not exceed 9999999999.99"}}})
	}
	return nil
}
