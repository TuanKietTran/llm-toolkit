package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"time"
)

type ctxKey struct{}

// requestID returns the id assigned to r by the logging middleware ("" outside it).
func requestID(r *http.Request) string {
	id, _ := r.Context().Value(ctxKey{}).(string)
	return id
}

func newRequestID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "req-" + hex.EncodeToString(b[:])
}

// statusWriter records status and bytes written while preserving Flusher and Unwrap.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logging assigns every request an id (response header X-Request-Id), and logs one
// line per request with the matched route pattern rather than the raw path, so
// unknown paths and path parameters never reach the log verbatim.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		sw := &statusWriter{ResponseWriter: w}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, id))
		next.ServeHTTP(sw, r)
		route := r.Pattern
		if route == "" {
			route = "<unmatched>"
		}
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		log.Printf("http id=%s route=%q status=%d bytes=%d duration=%s",
			id, route, status, sw.bytes, time.Since(start).Round(time.Millisecond))
	})
}

func isTooLarge(err error) bool {
	var mb *http.MaxBytesError
	return errors.As(err, &mb)
}
