package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/api"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/google/uuid"
)

// With no pool, reaching database logic would produce a 500. These checks prove
// that malformed input is rejected by the actual HTTP boundary before any SQL.
func TestContractValidationRejectsMalformedRequestsBeforeDatabase(t *testing.T) {
	s := &Server{cfg: Config{JWTSecret: strings.Repeat("contract-test-key", 4)}}
	handler, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tokens := map[string]string{}
	for _, role := range []string{"USER", "SELLER"} {
		token, err := s.signToken(1, role, "access", uuid.NewString(), now, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		tokens[role] = token
	}
	product := `{"name":"Valid","price":12.34,"stock":10,"category":"CAT","status":"ACTIVE"}`
	order := `{"items":[{"product_id":1,"quantity":1}]}`
	tests := []struct {
		name, method, path, role, body, field string
	}{
		{"null create item", "POST", "/orders", "USER", `{"items":[null]}`, "items"},
		{"null update item", "PUT", "/orders/1", "USER", `{"items":[null]}`, "items"},
		{"fractional quantity", "POST", "/orders", "USER", `{"items":[{"product_id":1,"quantity":1.5}]}`, "quantity"},
		{"string quantity", "PUT", "/orders/1", "USER", `{"items":[{"product_id":1,"quantity":"1"}]}`, "quantity"},
		{"unknown nested property", "POST", "/orders", "USER", `{"items":[{"product_id":1,"quantity":1,"unknown":true}]}`, "unknown"},
		{"cannot inject user", "POST", "/orders", "USER", strings.TrimSuffix(order, "}") + `,"user_id":2}`, "user_id"},
		{"empty items", "POST", "/orders", "USER", `{"items":[]}`, "items"},
		{"too many items", "POST", "/orders", "USER", `{"items":[` + strings.Repeat(`{"product_id":1,"quantity":1},`, 50) + `{"product_id":1,"quantity":1}]}`, "items"},
		{"invalid promo pattern", "POST", "/orders", "USER", strings.TrimSuffix(order, "}") + `,"promo_code":"bad"}`, "promo_code"},
		{"fractional stock", "POST", "/products", "SELLER", strings.Replace(product, `"stock":10`, `"stock":2.5`, 1), "stock"},
		{"stock beyond int32", "PUT", "/products/1", "SELLER", strings.Replace(product, `"stock":10`, `"stock":2147483648`, 1), "stock"},
		{"numeric name", "POST", "/products", "SELLER", strings.Replace(product, `"name":"Valid"`, `"name":123`, 1), "name"},
		{"boolean name", "PUT", "/products/1", "SELLER", strings.Replace(product, `"name":"Valid"`, `"name":true`, 1), "name"},
		{"price precision", "POST", "/products", "SELLER", strings.Replace(product, "12.34", "1.001", 1), "price"},
		{"string price", "POST", "/products", "SELLER", strings.Replace(product, "12.34", `"12.34"`, 1), "price"},
		{"unknown property", "PUT", "/products/1", "SELLER", strings.TrimSuffix(product, "}") + `,"surprise":1}`, "surprise"},
		{"missing required price", "POST", "/products", "SELLER", strings.Replace(product, `"price":12.34,`, "", 1), "price"},
		{"unknown status", "POST", "/products", "SELLER", strings.Replace(product, "ACTIVE", "UNKNOWN", 1), "status"},
		{"long name", "POST", "/products", "SELLER", strings.Replace(product, "Valid", strings.Repeat("я", 256), 1), "name"},
		{"bad id", "GET", "/products/abc", "USER", "", "id"},
		{"negative id", "GET", "/products/-1", "USER", "", "id"},
		{"negative page", "GET", "/products?page=-1", "USER", "", "page"},
		{"fractional page", "GET", "/products?page=1.5", "USER", "", "page"},
		{"excess page size", "GET", "/products?size=101", "USER", "", "size"},
		{"null document", "POST", "/products", "SELLER", "null", "body"},
		{"oversize UTF8 password", "POST", "/auth/register", "", `{"email":"utf8@example.test","password":"` + strings.Repeat("я", 37) + `","role":"USER"}`, "password"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			if test.role != "" {
				request.Header.Set("Authorization", "Bearer "+tokens[test.role])
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400: %s", recorder.Code, recorder.Body.String())
			}
			if _, err := uuid.Parse(recorder.Header().Get("X-Request-Id")); err != nil {
				t.Fatalf("missing request ID: %v", err)
			}
			var response struct {
				ErrorCode string `json:"error_code"`
				Details   struct {
					Fields []struct {
						Field, Message string
					} `json:"fields"`
				} `json:"details"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.ErrorCode != "VALIDATION_ERROR" || len(response.Details.Fields) == 0 {
				t.Fatalf("missing validation details: %s", recorder.Body.String())
			}
			matched := false
			for _, field := range response.Details.Fields {
				if strings.Contains(field.Field, test.field) && field.Message != "" {
					matched = true
				}
			}
			if !matched {
				t.Fatalf("missing concrete field %s: %s", test.field, recorder.Body.String())
			}
		})
	}
}

func TestOpenAPIValidatorAcceptsValidDecimalAndOptionalValues(t *testing.T) {
	spec, err := api.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	spec.Servers = nil
	router, err := legacy.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, amount := range []string{"0.01", "0.29", "0.57", "12.34", "9999999999.99"} {
		t.Run(amount, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/products", strings.NewReader(`{"name":"`+strings.Repeat("я", 255)+`","description":null,"price":`+amount+`,"stock":0,"category":"CAT","status":"ACTIVE"}`))
			request.Header.Set("Content-Type", "application/json")
			route, params, err := router.FindRoute(request)
			if err != nil {
				t.Fatal(err)
			}
			err = openapi3filter.ValidateRequest(context.Background(), &openapi3filter.RequestValidationInput{
				Request: request, PathParams: params, Route: route,
				Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
			})
			if err != nil {
				t.Fatalf("valid decimal/nullable request rejected: %v", err)
			}
		})
	}
}

func TestGeneratedProductResponseKeepsNullableDescription(t *testing.T) {
	encoded, err := json.Marshal(api.ProductResponse{})
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if string(response["description"]) != "null" {
		t.Fatalf("nullable description must remain explicit for existing clients: %s", encoded)
	}
}
