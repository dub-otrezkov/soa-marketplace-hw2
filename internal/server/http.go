package server

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"regexp"
	"strings"

	contract "github.com/dub-otrezkov/soa-marketplace-hw2/api"
	"github.com/dub-otrezkov/soa-marketplace-hw2/internal/api"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// Handler mounts generated routes behind contract validation and authentication.
// The same embedded specification produces both the interface and validation rules.
func (s *Server) Handler() (http.Handler, error) {
	spec, err := api.GetSwagger()
	if err != nil {
		return nil, err
	}
	spec.Servers = nil
	if err := spec.Validate(context.Background()); err != nil {
		return nil, err
	}
	router, err := legacy.NewRouter(spec)
	if err != nil {
		return nil, err
	}
	generated := api.HandlerWithOptions(s, api.StdHTTPServerOptions{
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, fieldError("parameters", "Invalid path or query parameter"))
		},
	})
	options := &openapi3filter.Options{MultiError: true, AuthenticationFunc: openapi3filter.NoopAuthenticationFunc, RejectWhenRequestBodyNotSpecified: true}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/actuator/health" || r.URL.Path == "/openapi.yaml" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				writeError(w, apiError(405, "METHOD_NOT_ALLOWED", "Method not allowed", nil))
				return
			}
			if r.URL.Path == "/openapi.yaml" {
				w.Header().Set("Content-Type", "application/yaml")
				_, _ = w.Write(contract.OpenAPI)
				return
			}
			if err := s.pool.Ping(r.Context()); err != nil {
				writeJSON(w, 503, map[string]string{"status": "DOWN"})
				return
			}
			writeJSON(w, 200, map[string]string{"status": "UP"})
			return
		}
		public := r.URL.Path == "/auth/register" || r.URL.Path == "/auth/login" || r.URL.Path == "/auth/refresh"
		if !public {
			if err := s.authenticate(r); err != nil {
				writeError(w, err)
				return
			}
		}
		route, params, err := router.FindRoute(r)
		if err != nil {
			if errors.Is(err, routers.ErrMethodNotAllowed) {
				writeError(w, apiError(405, "METHOD_NOT_ALLOWED", "Method not allowed", nil))
			} else {
				writeError(w, apiError(404, "NOT_FOUND", "Endpoint not found", nil))
			}
			return
		}
		if route.Operation.RequestBody != nil && r.ContentLength != 0 {
			media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || media != "application/json" {
				writeError(w, apiError(415, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json", nil))
				return
			}
		}
		input := &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route, Options: options}
		if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
			writeError(w, validationError(err))
			return
		}
		generated.ServeHTTP(w, r)
	})
	return logRequests(handler), nil
}

var quotedProperty = regexp.MustCompile(`property "([^"]+)"`)

func validationError(err error) error {
	fields := []map[string]string{}
	var collect func(error, string)
	collect = func(err error, fallback string) {
		switch e := err.(type) {
		case openapi3.MultiError:
			for _, child := range e {
				collect(child, fallback)
			}
		case *openapi3filter.RequestError:
			if e.Parameter != nil {
				fallback = e.Parameter.Name
			}
			if e.Err != nil {
				collect(e.Err, fallback)
			} else {
				fields = append(fields, map[string]string{"field": fallback, "message": "Required or invalid request value"})
			}
		case *openapi3.SchemaError:
			field := strings.Join(e.JSONPointer(), ".")
			if property := quotedProperty.FindStringSubmatch(e.Reason); len(property) > 1 {
				if field != "" {
					field += "."
				}
				field += property[1]
			}
			if field == "" {
				field = fallback
			}
			message := e.Reason
			if message == "" {
				message = "Value does not match the OpenAPI schema"
			}
			fields = append(fields, map[string]string{"field": field, "message": message})
		default:
			fields = append(fields, map[string]string{"field": fallback, "message": "Invalid JSON value or parameter"})
		}
	}
	collect(err, "body")
	return apiError(400, "VALIDATION_ERROR", "Request validation failed", map[string]any{"fields": fields})
}
