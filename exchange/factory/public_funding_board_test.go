package factory

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// TestPublicFundingBoardCoversEveryServedPerpMarket pins which markets have a
// funding board and which push it: every gateway perp market reads over REST,
// and only Hyperliquid also streams (its whole-venue board, HIP-3 included,
// would otherwise cost most of its REST weight). Construction does no I/O.
func TestPublicFundingBoardCoversEveryServedPerpMarket(t *testing.T) {
	transport := new(countingTransport)
	client := &http.Client{Transport: transport}
	for _, market := range []struct {
		venue      exchange.Venue
		settlement string
		streams    bool
	}{
		{exchange.VenueBinance, "USDT", false}, {exchange.VenueOKX, "USDT", false},
		{exchange.VenueBybit, "USDT", false}, {exchange.VenueBybit, "USDC", false},
		{exchange.VenueBitget, "USDT", false}, {exchange.VenueBitget, "USDC", false},
		{exchange.VenueGate, "USDT", false}, {exchange.VenueLighter, "USDC", false},
		{exchange.VenueHyperliquid, "USDC", true},
	} {
		board, err := NewPublicFundingBoard(market.venue, market.settlement, EnvironmentLive, client)
		if err != nil {
			t.Fatalf("%s/%s: %v", market.venue, market.settlement, err)
		}
		scope := board.Scope()
		if scope.Venue != market.venue || scope.Product != exchange.ProductPerp || scope.Settlement != market.settlement {
			t.Fatalf("%s/%s scope = %+v", market.venue, market.settlement, scope)
		}
		if board.Streams() != market.streams {
			t.Fatalf("%s/%s streams = %t", market.venue, market.settlement, board.Streams())
		}
		if !market.streams {
			if err := board.Stream(context.Background(), func(exchange.FundingBoard) {}); !errors.Is(err, exchange.ErrUnsupported) {
				t.Fatalf("%s/%s Stream err = %v", market.venue, market.settlement, err)
			}
		}
	}
	if transport.requests.Load() != 0 {
		t.Fatalf("construction sent %d requests", transport.requests.Load())
	}
	for _, bad := range []struct {
		venue      exchange.Venue
		settlement string
	}{{exchange.VenueOKX, "USDC"}, {exchange.Venue("nope"), "USDT"}} {
		if _, err := NewPublicFundingBoard(bad.venue, bad.settlement, EnvironmentLive, client); err == nil {
			t.Fatalf("%s/%s built", bad.venue, bad.settlement)
		}
	}
	if _, err := NewPublicFundingBoard(exchange.VenueOKX, "USDT", EnvironmentLive, nil); err == nil {
		t.Fatal("built without an HTTP client")
	}
}
