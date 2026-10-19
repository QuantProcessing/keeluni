package factory

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/routeprofile"
)

// Every served perpetual market has a funding board, built without I/O.
func TestPublicFundingBoardCoversEveryServedPerpMarket(t *testing.T) {
	for _, profile := range routeprofile.DefaultProfiles() {
		if profile.Product != routeprofile.ProductPerp {
			continue
		}
		board, err := NewPublicFundingBoard(exchange.Venue(profile.Venue), profile.SettlementAsset, EnvironmentLive, &http.Client{})
		if err != nil {
			t.Fatalf("%s: %v", profile.ID, err)
		}
		if board.Scope().Venue != exchange.Venue(profile.Venue) || board.Scope().Settlement != profile.SettlementAsset {
			t.Fatalf("%s scope = %+v", profile.ID, board.Scope())
		}
		if board.Streams() != (profile.Venue == "hyperliquid") || board.Cadence() <= 0 {
			t.Fatalf("%s transport stream=%v cadence=%s", profile.ID, board.Streams(), board.Cadence())
		}
		_ = board.Close()
	}
	if _, err := NewPublicFundingBoard(exchange.VenueBinance, "EUR", EnvironmentLive, &http.Client{}); err == nil {
		t.Fatal("unknown settlement accepted")
	}
}

// A polled board delivers a complete snapshot keyed by native identity, in
// the one convention: Kraken's hourly relative rate settles on the hour.
func TestPublicFundingBoardDeliversNativeFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/derivatives/api/v3/tickers":
			_, _ = io.WriteString(w, `{"result":"success","tickers":[{"symbol":"PF_XBTUSD","markPrice":81000,"indexPrice":80990,"openInterest":10,"relativeFundingRate":0.00002}]}`)
		case "/derivatives/api/v3/instruments":
			_, _ = io.WriteString(w, `{"result":"success","instruments":[{"symbol":"PF_XBTUSD","type":"flexible_futures","base":"BTC","quote":"USD","tickSize":1,"contractSize":1,"contractValueTradePrecision":4,"tradeable":true}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	board, err := newPublicFundingBoard(exchange.VenueKraken, "USD", server.Client(), WithEndpoint(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer board.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	patches := make(chan exchange.DerivativePatch, 2)
	go func() { _ = board.Run(ctx, func(patch exchange.DerivativePatch) { patches <- patch }) }()
	select {
	case patch := <-patches:
		if patch.Kind != exchange.EventSnapshot || len(patch.Facts) != 1 {
			t.Fatalf("patch = %+v", patch)
		}
		fact := patch.Facts[0]
		if fact.Instrument.NativeSymbol != "PF_XBTUSD" || fact.FundingRate.Value.String() != "0.00002" || fact.FundingIntervalHours.Value != 1 ||
			fact.NextFundingTime.Minute() != 0 || time.Until(fact.NextFundingTime) > time.Hour {
			t.Fatalf("fact = %+v", fact)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no snapshot")
	}
}
