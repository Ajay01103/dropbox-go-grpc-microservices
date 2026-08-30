package jwks

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type ScyllaDBTransport struct {
	inner     http.RoundTripper
	store     JWKSStore
	serviceID string
	jwksURL   string

	seedOnce         sync.Once
	seededFromScylla atomic.Bool
}

func newScyllaDBTransport(inner http.RoundTripper, store JWKSStore, serviceID, jwksURL string) *ScyllaDBTransport {
	return &ScyllaDBTransport{
		inner:     inner,
		store:     store,
		serviceID: serviceID,
		jwksURL:   jwksURL,
	}
}

func (t *ScyllaDBTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	isJWKSURL := req.URL.String() == t.jwksURL

	if isJWKSURL && !t.seededFromScylla.Load() {
		var seededResp *http.Response
		t.seedOnce.Do(func() {
			data, err := t.store.Load(req.Context(), t.serviceID)
			if err != nil {
				slog.Warn("jwks: scylla load failed, falling back to HTTP", "err", err)
				return
			}
			if len(data) == 0 {
				slog.Info("jwks: no scylla snapshot found, doing cold HTTP fetch")
				return
			}

			slog.Info("jwks: seeded from scylla", "service", t.serviceID, "bytes", len(data))
			seededResp = &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header: http.Header{
					"Content-Type":  []string{"application/json"},
					"Cache-Control": []string{"max-age=300"},
				},
				Body:    io.NopCloser(bytes.NewReader(data)),
				Request: req.Clone(req.Context()),
			}
			t.seededFromScylla.Store(true)
		})
		if seededResp != nil {
			return seededResp, nil
		}
	}

	resp, err := t.inner.RoundTrip(req)
	if err != nil || !isJWKSURL || resp.StatusCode != http.StatusOK {
		return resp, err
	}

	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))

	if readErr == nil {
		go func(snapshot []byte) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if saveErr := t.store.Save(ctx, t.serviceID, snapshot); saveErr != nil {
				slog.Warn("jwks: failed to persist to scylla", "err", saveErr)
			} else {
				slog.Info("jwks: persisted fresh JWKS to scylla", "service", t.serviceID)
			}
		}(body)
	}

	return resp, nil
}