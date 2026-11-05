package asset

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExchangeCacheAndStaleFallback(t *testing.T) {
	calls := 0
	bad := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if bad {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"base":"USD","quote":"CNY","date":"2026-11-04","rate":7},{"base":"USD","quote":"CAD","date":"2026-11-03","rate":1.4},{"base":"USD","quote":"XYZ","date":"2026-11-04","rate":99}]`))
	}))
	defer server.Close()
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := NewExchange()
	e.url = server.URL
	e.now = func() time.Time { return now }
	first, err := e.Rates(context.Background())
	if err != nil || first.Rates["USD"] != 1 || first.Rates["CNY"] != 7 || first.Date != "2026-11-03" || first.Rates["XYZ"] != 0 {
		t.Fatal(first, err)
	}
	_, _ = e.Rates(context.Background())
	if calls != 1 {
		t.Fatal("cache not used")
	}
	now = now.Add(7 * time.Hour)
	bad = true
	stale, err := e.Rates(context.Background())
	if err != nil || !stale.Stale || stale.Rates["CAD"] != 1.4 {
		t.Fatal(stale, err)
	}
	_, _ = e.Rates(context.Background())
	if calls != 2 {
		t.Fatal("failed request not throttled")
	}
}

func TestExchangeFailsWithoutInventingRates(t *testing.T) {
	for _, body := range []string{`[]`, `invalid`, `[{"base":"USD","quote":"CNY","date":"bad","rate":7}]`, `[{"base":"USD","quote":"CNY","date":"2026-11-04","rate":-1}]`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		e := NewExchange()
		e.url = server.URL
		got, err := e.Rates(context.Background())
		server.Close()
		if err == nil || got.Rates != nil {
			t.Fatal("bad rates accepted", got, err)
		}
	}
}
