package server

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/api"
	"github.com/shopspring/decimal"
)

func TestRepeatedProductsAggregateWithoutBypassingQuantityLimit(t *testing.T) {
	items := []api.OrderItemRequest{{ProductId: 5, Quantity: 500}, {ProductId: 2, Quantity: 3}, {ProductId: 5, Quantity: 499}}
	quantities, err := aggregateOrderItems(items)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(quantities, map[int64]int32{2: 3, 5: 999}) {
		t.Fatalf("unexpected aggregated quantities: %v", quantities)
	}
	if !reflect.DeepEqual(quantityIDs(quantities), []int64{2, 5}) {
		t.Fatal("product locks must have a deterministic identifier order")
	}
	items[2].Quantity = 500
	_, err = aggregateOrderItems(items)
	assertOrderError(t, err, http.StatusBadRequest, "VALIDATION_ERROR")
}

func TestOrderItemsRejectEmptyMissingOrInvalidValues(t *testing.T) {
	for _, items := range [][]api.OrderItemRequest{
		nil,
		{},
		{{}},
		{{ProductId: 1, Quantity: 0}},
		{{ProductId: 1, Quantity: 1000}},
		{{ProductId: -1, Quantity: 1}},
		make([]api.OrderItemRequest, 51),
	} {
		_, err := aggregateOrderItems(items)
		assertOrderError(t, err, http.StatusBadRequest, "VALIDATION_ERROR")
	}
}

func TestStockChecksCountOldReservationAndReportEveryShortage(t *testing.T) {
	products := map[int64]orderProduct{1: {stock: 3}, 2: {stock: 1}}
	old := map[int64]int32{1: 2}
	if err := checkOrderStock(products, map[int64]int32{1: 5, 2: 1}, old); err != nil {
		t.Fatalf("the previous reservation is available to its order: %v", err)
	}
	err := checkOrderStock(products, map[int64]int32{1: 6, 2: 2}, old)
	failure := assertOrderError(t, err, http.StatusConflict, "INSUFFICIENT_STOCK")
	shortages, ok := failure.details["products"].([]any)
	if !ok || len(shortages) != 2 {
		t.Fatalf("expected every insufficient product in details, got %#v", failure.details)
	}
	first := shortages[0].(map[string]any)
	if first["product_id"] != int64(1) || first["available_stock"] != int64(5) {
		t.Fatalf("available stock must include the existing reservation: %#v", first)
	}
}

func TestSubtotalUsesExactSnapshotPrices(t *testing.T) {
	quantities := map[int64]int32{1: 3, 2: 4}
	prices := map[int64]decimal.Decimal{1: decimal.RequireFromString("0.10"), 2: decimal.RequireFromString("0.20")}
	if total := orderSubtotal(quantities, prices); !total.Equal(decimal.RequireFromString("1.10")) {
		t.Fatalf("expected exact decimal 1.10, got %s", total)
	}
	max := decimal.RequireFromString("9999999999.99")
	if err := checkOrderMoney(max, decimal.Zero); err != nil {
		t.Fatalf("exact monetary maximum must fit: %v", err)
	}
	assertOrderError(t, checkOrderMoney(max.Add(decimal.RequireFromString("0.01")), decimal.Zero), http.StatusBadRequest, "VALIDATION_ERROR")
	assertOrderError(t, checkOrderMoney(decimal.Zero, max.Add(decimal.RequireFromString("0.01"))), http.StatusBadRequest, "VALIDATION_ERROR")
}

func TestOrderPermissionsRespectOwnerAndAdministrator(t *testing.T) {
	order := orderRecord{userID: 10}
	if err := checkOrderOwnership(order, Principal{ID: 10, Role: "USER"}); err != nil {
		t.Fatal(err)
	}
	if err := checkOrderOwnership(order, Principal{ID: 20, Role: "ADMIN"}); err != nil {
		t.Fatal(err)
	}
	assertOrderError(t, checkOrderOwnership(order, Principal{ID: 20, Role: "USER"}), http.StatusForbidden, "ORDER_OWNERSHIP_VIOLATION")
	assertOrderError(t, requireOrderRole(Principal{ID: 10, Role: "SELLER"}), http.StatusForbidden, "ACCESS_DENIED")
}
