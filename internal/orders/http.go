package orders

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
)

const (
	defaultLimit = 20
	maxLimit     = 100
	maxBody      = 64 << 10
)

// Handler wires the routes. ping reports database health for /healthz.
func Handler(s *Store, ping func(*http.Request) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := ping(r); err != nil {
			writeErr(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /products", func(w http.ResponseWriter, r *http.Request) {
		ps, err := s.Products(r.Context())
		if err != nil {
			fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, ps)
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		var req CreateRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if err := req.Normalize(); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) > 200 {
			writeErr(w, http.StatusBadRequest, "Idempotency-Key longer than 200 bytes")
			return
		}
		o, replayed, err := s.Create(r.Context(), req, key)
		if err != nil {
			fail(w, r, err)
			return
		}
		code := http.StatusCreated
		if replayed {
			code = http.StatusOK
		}
		writeJSON(w, code, o)
	})
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		customer, err1 := optInt(q.Get("customer_id"), 0)
		after, err2 := optInt(q.Get("after"), 0)
		limit, err3 := optInt(q.Get("limit"), defaultLimit)
		if err := errors.Join(err1, err2, err3); err != nil || limit < 1 || limit > maxLimit || after < 0 {
			writeErr(w, http.StatusBadRequest, "customer_id, after must be >= 0 and limit in 1..100")
			return
		}
		list, next, err := s.List(r.Context(), customer, after, int(limit))
		if err != nil {
			fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"orders": list, "next_after": next})
	})
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		o, err := s.Get(r.Context(), id)
		if err != nil {
			fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, o)
	})
	mux.HandleFunc("POST /orders/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		action := r.PathValue("action")
		if _, known := transitions[action]; !known {
			writeErr(w, http.StatusNotFound, "unknown action")
			return
		}
		o, err := s.Transition(r.Context(), id, action)
		if err != nil {
			fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, o)
	})
	return mux
}

func optInt(s string, def int64) (int64, error) {
	if s == "" {
		return def, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "invalid order id")
		return 0, false
	}
	return id, true
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrOutOfStock), errors.Is(err, ErrBadTransition):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrKeyReused), errors.Is(err, ErrUnknownProduct), errors.Is(err, ErrUnknownCustomer):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
