package asset

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sync"
	"time"
)

// Only public currency codes are sent; ledger records never leave the server.
const exchangeURL = "https://api.frankfurter.dev/v2/rates?base=USD&quotes=CNY,EUR,GBP,CAD,HKD,JPY,AUD,SGD"

type ExchangeRates struct {
	Base   string             `json:"base"`
	Rates  map[string]float64 `json:"rates"`
	Date   string             `json:"date"`
	Source string             `json:"source"`
	Stale  bool               `json:"stale"`
}

type Exchange struct {
	mu     sync.Mutex
	client *http.Client
	url    string
	cached ExchangeRates
	next   time.Time
	now    func() time.Time
}

func NewExchange() *Exchange {
	return &Exchange{client: &http.Client{Timeout: 5 * time.Second}, url: exchangeURL, now: time.Now}
}

func (e *Exchange) Rates(ctx context.Context) (ExchangeRates, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.now().Before(e.next) {
		if e.cached.Rates != nil {
			return e.cached, nil
		}
		return ExchangeRates{}, errors.New("rates unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.url, nil)
	if err != nil {
		return ExchangeRates{}, err
	}
	res, err := e.client.Do(req)
	if err == nil {
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			err = errors.New("exchange provider unavailable")
		} else {
			var rows []struct {
				Base, Quote, Date string
				Rate              float64
			}
			err = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&rows)
			rates := ExchangeRates{Base: "USD", Rates: map[string]float64{"USD": 1}, Source: "Frankfurter"}
			if err == nil {
				for _, row := range rows {
					if row.Base != "USD" || row.Rate <= 0 || math.IsNaN(row.Rate) || math.IsInf(row.Rate, 0) {
						continue
					}
					if _, dateErr := time.Parse("2006-01-02", row.Date); dateErr != nil {
						continue
					}
					switch row.Quote {
					case "CNY", "EUR", "GBP", "CAD", "HKD", "JPY", "AUD", "SGD":
					default:
						continue
					}
					rates.Rates[row.Quote] = row.Rate
					if rates.Date == "" || row.Date < rates.Date {
						rates.Date = row.Date
					}
				}
				if len(rates.Rates) > 1 {
					e.cached = rates
					e.next = e.now().Add(6 * time.Hour)
					return rates, nil
				}
				err = errors.New("no usable exchange rates")
			}
		}
	}
	e.next = e.now().Add(5 * time.Minute)
	if e.cached.Rates != nil {
		e.cached.Stale = true
		return e.cached, nil
	}
	return ExchangeRates{}, err
}
