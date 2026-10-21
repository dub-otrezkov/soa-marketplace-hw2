package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

const logBodyLimit = 64 << 10
const requestBodyLimit = 1 << 20

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(data)
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := uuid.NewString()
		w.Header().Set("X-Request-Id", id)
		writer := &statusWriter{ResponseWriter: w}
		state := &requestState{}
		r = r.WithContext(context.WithValue(r.Context(), requestContextKey{}, state))
		var body []byte
		defer func() {
			if recover() != nil {
				slog.Error("request panic recovered")
				writeError(writer, errorsForPanic{})
			}
			if writer.status == 0 {
				writer.status = 200
			}
			var userID any
			if state.user != nil {
				userID = state.user.ID
			}
			entry := map[string]any{
				"request_id": id, "method": r.Method, "endpoint": r.URL.Path,
				"status_code": writer.status, "duration_ms": float64(time.Since(start).Microseconds()) / 1000,
				"user_id": userID, "timestamp": start.UTC().Format(time.RFC3339Nano),
			}
			if r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE" || r.Method == "PATCH" {
				entry["request_body"] = safeLogBody(body)
			}
			// One write per complete JSON line prevents interleaving between requests.
			if encoded, err := json.Marshal(entry); err == nil {
				_, _ = os.Stdout.Write(append(encoded, '\n'))
			}
		}()
		if r.Body != nil {
			var err error
			body, err = io.ReadAll(io.LimitReader(r.Body, requestBodyLimit+1))
			_ = r.Body.Close()
			if err != nil {
				writeError(writer, fieldError("body", "Cannot read request body"))
				return
			}
			if len(body) > requestBodyLimit {
				writeError(writer, fieldError("body", "Request body exceeds 1 MiB"))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(writer, r)
	})
}

type errorsForPanic struct{}

func (errorsForPanic) Error() string { return "handler panic" }

func safeLogBody(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	if len(body) > logBodyLimit {
		return "[body exceeds log limit]"
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return "[unparseable body omitted]"
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return "[unparseable body omitted]"
	}
	redact(value)
	return value
}
func redact(value any) {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			switch strings.ToLower(key) {
			case "password", "password_hash", "access_token", "refresh_token", "authorization", "secret", "token":
				item[key] = "***"
			default:
				redact(child)
			}
		}
	case []any:
		for _, child := range item {
			redact(child)
		}
	}
}
