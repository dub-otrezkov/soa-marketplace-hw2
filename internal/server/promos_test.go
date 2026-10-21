package server

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func assertOrderError(t *testing.T, err error, status int, code string) *applicationError {
	t.Helper()
	var result *applicationError
	if !errors.As(err, &result) {
		t.Fatalf("expected application error %s, got %v", code, err)
	}
	if result.status != status || result.code != code {
		t.Fatalf("expected %d %s, got %d %s", status, code, result.status, result.code)
	}
	return result
}

func TestPromoDiscountMoneyBoundaries(t *testing.T) {
	tests := []struct {
		name, kind, value, subtotal, expected string
	}{
		{"ordinary percentage", "PERCENTAGE", "10.00", "100.00", "10.00"},
		{"maximum percentage", "PERCENTAGE", "70.00", "200.00", "140.00"},
		{"above percentage limit", "PERCENTAGE", "70.01", "200.00", "0.00"},
		{"full percentage pays full price", "PERCENTAGE", "100.00", "200.00", "0.00"},
		{"large percentage pays full price", "PERCENTAGE", "1000.00", "200.00", "0.00"},
		{"decimal rounding down", "PERCENTAGE", "25.00", "10.01", "2.50"},
		{"decimal rounding half up", "PERCENTAGE", "25.00", "10.02", "2.51"},
		{"fixed discount", "FIXED_AMOUNT", "20.00", "100.00", "20.00"},
		{"fixed discount capped at subtotal", "FIXED_AMOUNT", "200.00", "100.00", "100.00"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			promo := promoRecord{discountType: test.kind, discountValue: decimal.RequireFromString(test.value)}
			actual := promoDiscount(promo, decimal.RequireFromString(test.subtotal))
			if !actual.Equal(decimal.RequireFromString(test.expected)) {
				t.Fatalf("discount = %s, want %s", actual, test.expected)
			}
		})
	}
}

func TestExistingOrderKeepsItsReservedLastPromoUse(t *testing.T) {
	now := time.Date(2026, time.October, 21, 12, 0, 0, 0, time.UTC)
	promo := promoRecord{active: true, maxUses: 1, currentUses: 1,
		validFrom: now.Add(-time.Hour), validUntil: now.Add(time.Hour)}
	if err := validatePromo(promo, true, now); err != nil {
		t.Fatalf("existing use must remain valid at full capacity: %v", err)
	}
	assertOrderError(t, validatePromo(promo, false, now), http.StatusUnprocessableEntity, "PROMO_CODE_INVALID")
}

func TestPromotionValidityWindowIsInclusive(t *testing.T) {
	start := time.Date(2026, time.October, 21, 12, 0, 0, 0, time.UTC)
	promo := promoRecord{active: true, maxUses: 1, validFrom: start, validUntil: start.Add(time.Hour)}
	for _, now := range []time.Time{promo.validFrom, promo.validUntil} {
		if err := validatePromo(promo, false, now); err != nil {
			t.Fatalf("exact validity boundary must be included: %v", err)
		}
	}
	for _, now := range []time.Time{promo.validFrom.Add(-time.Nanosecond), promo.validUntil.Add(time.Nanosecond)} {
		assertOrderError(t, validatePromo(promo, true, now), http.StatusUnprocessableEntity, "PROMO_CODE_INVALID")
	}
	promo.active = false
	assertOrderError(t, validatePromo(promo, true, start), http.StatusUnprocessableEntity, "PROMO_CODE_INVALID")
}

func TestPromotionMinimumUsesSubtotalAndIncludesExactBoundary(t *testing.T) {
	promo := promoRecord{minOrderAmount: decimal.RequireFromString("100.00")}
	if err := requirePromoMinimum(promo, decimal.RequireFromString("100.00")); err != nil {
		t.Fatalf("minimum equality must be accepted: %v", err)
	}
	assertOrderError(t, requirePromoMinimum(promo, decimal.RequireFromString("99.99")), http.StatusUnprocessableEntity, "PROMO_CODE_MIN_AMOUNT")
}

func TestPromoMoneyRejectsPrecisionOverflowAndNegativeValues(t *testing.T) {
	for _, value := range []string{"0", "-0.01", "0.001", "10000000000"} {
		assertOrderError(t, validatePromoMoney("discount_value", decimal.RequireFromString(value), false), http.StatusBadRequest, "VALIDATION_ERROR")
	}
	for _, value := range []string{"0.01", "1.2300", "9999999999.99"} {
		if err := validatePromoMoney("discount_value", decimal.RequireFromString(value), false); err != nil {
			t.Fatalf("valid money %s rejected: %v", value, err)
		}
	}
	if err := validatePromoMoney("min_order_amount", decimal.Zero, true); err != nil {
		t.Fatalf("zero minimum should be allowed: %v", err)
	}
}
