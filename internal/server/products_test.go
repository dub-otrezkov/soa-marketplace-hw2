package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/api"
	"github.com/shopspring/decimal"
)

func productTestPointer[T any](value T) *T { return &value }

func assertProductError(t *testing.T, err error, status int, code, field string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an API error")
	}
	recorder := httptest.NewRecorder()
	writeError(recorder, err)
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	var body struct {
		ErrorCode string `json:"error_code"`
		Details   struct {
			Fields []struct {
				Field   string `json:"field"`
				Message string `json:"message"`
			} `json:"fields"`
		} `json:"details"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ErrorCode != code {
		t.Fatalf("error_code = %s, want %s", body.ErrorCode, code)
	}
	if field != "" {
		if len(body.Details.Fields) == 0 || body.Details.Fields[0].Field != field || body.Details.Fields[0].Message == "" {
			t.Fatalf("expected a detailed %s validation error: %s", field, recorder.Body.String())
		}
	}
}

func TestProductValidationRejectsContractBoundaryViolations(t *testing.T) {
	base := api.ProductCreate{Name: "Товар", Category: "Учёба", Price: decimal.RequireFromString("12.34"), Stock: 1, Status: "ACTIVE"}
	tests := []struct {
		name   string
		field  string
		change func(*api.ProductCreate)
	}{
		{"empty name", "name", func(p *api.ProductCreate) { p.Name = "" }},
		{"blank name", "name", func(p *api.ProductCreate) { p.Name = " \t\n" }},
		{"name over 255 code points", "name", func(p *api.ProductCreate) { p.Name = strings.Repeat("я", 256) }},
		{"description over 4000 code points", "description", func(p *api.ProductCreate) { p.Description = productTestPointer(strings.Repeat("я", 4001)) }},
		{"zero price", "price", func(p *api.ProductCreate) { p.Price = decimal.Zero }},
		{"negative price", "price", func(p *api.ProductCreate) { p.Price = decimal.NewFromInt(-1) }},
		{"subcent price", "price", func(p *api.ProductCreate) { p.Price = decimal.RequireFromString("0.001") }},
		{"excess precision", "price", func(p *api.ProductCreate) { p.Price = decimal.RequireFromString("12.345") }},
		{"numeric overflow", "price", func(p *api.ProductCreate) { p.Price = decimal.RequireFromString("10000000000.00") }},
		{"negative stock", "stock", func(p *api.ProductCreate) { p.Stock = -1 }},
		{"empty category", "category", func(p *api.ProductCreate) { p.Category = "" }},
		{"blank category", "category", func(p *api.ProductCreate) { p.Category = "   " }},
		{"category over 100 code points", "category", func(p *api.ProductCreate) { p.Category = strings.Repeat("я", 101) }},
		{"unknown status", "status", func(p *api.ProductCreate) { p.Status = "UNKNOWN" }},
		{"invalid seller", "seller_id", func(p *api.ProductCreate) { p.SellerId = productTestPointer(int64(0)) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.change(&input)
			err := validateProduct(input.Name, input.Description, input.Price, input.Stock, input.Category, input.Status, input.SellerId)
			assertProductError(t, err, http.StatusBadRequest, "VALIDATION_ERROR", test.field)
		})
	}
}

func TestProductValidationAcceptsExactMoneyAndUnicodeBoundaries(t *testing.T) {
	for _, amount := range []string{"0.01", "1.0000", "9999999999.99"} {
		for _, status := range []api.ProductStatus{"ACTIVE", "INACTIVE", "ARCHIVED"} {
			description := strings.Repeat("я", 4000)
			err := validateProduct(strings.Repeat("я", 255), &description, decimal.RequireFromString(amount), 0,
				strings.Repeat("я", 100), status, productTestPointer(int64(1)))
			if err != nil {
				t.Fatalf("valid amount=%s, status=%s rejected: %v", amount, status, err)
			}
		}
	}
}

func TestProductOwnershipAndAdministratorAssignment(t *testing.T) {
	seller := Principal{ID: 11, Role: "SELLER"}
	admin := Principal{ID: 99, Role: "ADMIN"}
	user := Principal{ID: 22, Role: "USER"}
	tests := []struct {
		name      string
		actor     Principal
		current   int64 // Zero denotes creation; persisted owners are positive.
		requested *int64
		want      int64
		denied    bool
	}{
		{"seller create owns by default", seller, 0, nil, 11, false},
		{"seller create explicit self", seller, 0, productTestPointer(int64(11)), 11, false},
		{"seller cannot create for another", seller, 0, productTestPointer(int64(22)), 0, true},
		{"user cannot create", user, 0, nil, 0, true},
		{"admin create default", admin, 0, nil, 99, false},
		{"admin assign seller", admin, 0, productTestPointer(int64(11)), 11, false},
		{"seller update own product", seller, 11, nil, 11, false},
		{"seller cannot change others", seller, 22, nil, 0, true},
		{"seller cannot claim others", seller, 22, productTestPointer(int64(11)), 0, true},
		{"seller cannot transfer own", seller, 11, productTestPointer(int64(22)), 0, true},
		{"user cannot update", user, 22, nil, 0, true},
		{"admin retains owner by default", admin, 11, nil, 11, false},
		{"admin can transfer owner", admin, 11, productTestPointer(int64(33)), 33, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got int64
			var err error
			if test.current == 0 {
				got, err = productCreateSeller(test.actor, test.requested)
			} else {
				got, err = productUpdateSeller(test.actor, test.current, test.requested)
			}
			if test.denied {
				assertProductError(t, err, http.StatusForbidden, "ACCESS_DENIED", "")
			} else if err != nil || got != test.want {
				t.Fatalf("owner=%d error=%v; want owner=%d", got, err, test.want)
			}
		})
	}
}

func TestProductPaginationBoundaries(t *testing.T) {
	page, size, err := productPagination(api.ListProductsParams{})
	if err != nil || page != 0 || size != 20 {
		t.Fatalf("defaults page=%d size=%d err=%v", page, size, err)
	}
	page, size, err = productPagination(api.ListProductsParams{Page: productTestPointer(int32(2147483647)), Size: productTestPointer(int32(100))})
	if err != nil || int64(page)*int64(size) != 214748364700 {
		t.Fatalf("large offset overflow or rejection: page=%d size=%d err=%v", page, size, err)
	}
	for _, test := range []struct {
		params api.ListProductsParams
		field  string
	}{
		{api.ListProductsParams{Page: productTestPointer(int32(-1))}, "page"},
		{api.ListProductsParams{Size: productTestPointer(int32(0))}, "size"},
		{api.ListProductsParams{Size: productTestPointer(int32(101))}, "size"},
		{api.ListProductsParams{Category: productTestPointer("")}, "category"},
		{api.ListProductsParams{Category: productTestPointer(strings.Repeat("я", 101))}, "category"},
		{api.ListProductsParams{Status: productTestPointer(api.ProductStatus("BAD"))}, "status"},
	} {
		_, _, err := productPagination(test.params)
		assertProductError(t, err, http.StatusBadRequest, "VALIDATION_ERROR", test.field)
	}
}
