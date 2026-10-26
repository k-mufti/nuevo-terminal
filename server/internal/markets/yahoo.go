// Package markets polls index, commodity, crypto and currency prices from
// Yahoo Finance's public chart endpoint and publishes them live.
package markets

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"

	"nuevo-rss/internal/live"
)

type Symbol struct {
	Sym  string
	Name string
}

var DefaultSymbols = []Symbol{
	{"^GSPC", "S&P 500"}, {"^IXIC", "NASDAQ"}, {"^DJI", "DOW"}, {"^FTSE", "FTSE"}, {"^N225", "NIKKEI"},
	{"^TNX", "US 10Y"}, {"DX-Y.NYB", "DXY"}, {"EURUSD=X", "EUR/USD"}, {"GBPUSD=X", "GBP/USD"}, {"USDJPY=X", "USD/JPY"},
	{"CL=F", "WTI"}, {"BZ=F", "BRENT"}, {"GC=F", "GOLD"}, {"BTC-USD", "BTC"}, {"ETH-USD", "ETH"},
}

type Quote struct {
	Sym    string    `json:"sym"`
	Name   string    `json:"name"`
	Price  float64   `json:"price"`
	Change float64   `json:"change"`
	Pct    float64   `json:"pct"`
	Time   int64     `json:"time"` // Unix ms of the last trade
	Spark  []float64 `json:"spark,omitempty"`
}

type Service struct {
	symbols  []Symbol
	hub      *live.Hub
	client   *http.Client
	interval time.Duration
	log      *slog.Logger

	mu     sync.RWMutex
	quotes map[string]Quote
}

func New(hub *live.Hub, symbols []Symbol, interval time.Duration, log *slog.Logger) *Service {
	return &Service{
		symbols:  symbols,
		hub:      hub,
		client:   &http.Client{Timeout: 10 * time.Second},
		interval: interval,
		log:      log,
		quotes:   make(map[string]Quote),
	}
}

func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		s.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Latest returns the current quotes in display order.
func (s *Service) Latest() []Quote {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Quote, 0, len(s.symbols))
	for _, sym := range s.symbols {
		if q, ok := s.quotes[sym.Sym]; ok {
			out = append(out, q)
		}
	}
	return out
}

func (s *Service) poll(ctx context.Context) {
	var wg sync.WaitGroup
	results := make([]*Quote, len(s.symbols))
	for i, sym := range s.symbols {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q, err := s.fetch(ctx, sym)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Debug("quote failed", "sym", sym.Sym, "err", err)
				}
				return
			}
			results[i] = q
		}()
	}
	wg.Wait()

	changed := false
	s.mu.Lock()
	for _, q := range results {
		if q == nil {
			continue
		}
		if old, ok := s.quotes[q.Sym]; !ok || old.Price != q.Price || old.Time != q.Time {
			changed = true
		}
		s.quotes[q.Sym] = *q
	}
	s.mu.Unlock()

	if changed {
		b, err := json.Marshal(s.Latest())
		if err == nil {
			s.hub.Publish(live.Event{Type: "markets", Data: b})
		}
	}
}

type chartResponse struct {
	Chart struct {
		Result []struct {
			Meta struct {
				RegularMarketPrice float64 `json:"regularMarketPrice"`
				ChartPreviousClose float64 `json:"chartPreviousClose"`
				PreviousClose      float64 `json:"previousClose"`
				RegularMarketTime  int64   `json:"regularMarketTime"`
			} `json:"meta"`
			Indicators struct {
				Quote []struct {
					Close []*float64 `json:"close"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

const sparkPoints = 48

func (s *Service) fetch(ctx context.Context, sym Symbol) (*Quote, error) {
	u := "https://query1.finance.yahoo.com/v8/finance/chart/" + url.PathEscape(sym.Sym) + "?range=1d&interval=5m"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var cr chartResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, err
	}
	if cr.Chart.Error != nil {
		return nil, fmt.Errorf("yahoo: %s", cr.Chart.Error.Description)
	}
	if len(cr.Chart.Result) == 0 {
		return nil, fmt.Errorf("no result")
	}
	r := cr.Chart.Result[0]
	m := r.Meta
	prev := m.ChartPreviousClose
	if prev == 0 {
		prev = m.PreviousClose
	}
	q := &Quote{Sym: sym.Sym, Name: sym.Name, Price: m.RegularMarketPrice, Time: m.RegularMarketTime * 1000}
	if prev != 0 {
		q.Change = round(q.Price-prev, 6)
		q.Pct = round((q.Price-prev)/prev*100, 3)
	}
	if len(r.Indicators.Quote) > 0 {
		q.Spark = downsample(r.Indicators.Quote[0].Close, sparkPoints)
	}
	return q, nil
}

func downsample(in []*float64, n int) []float64 {
	vals := make([]float64, 0, len(in))
	for _, v := range in {
		if v != nil {
			vals = append(vals, *v)
		}
	}
	if len(vals) <= n {
		return roundAll(vals)
	}
	out := make([]float64, n)
	for i := range n {
		out[i] = vals[i*(len(vals)-1)/(n-1)]
	}
	return roundAll(out)
}

func roundAll(v []float64) []float64 {
	for i := range v {
		v[i] = round(v[i], 5)
	}
	return v
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
