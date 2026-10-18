package factoryclient

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// TestFundingBoardLive reads every reachable venue's real board. It needs the
// network and runs only with BOLTER_LIVE_FUNDING=1.
func TestFundingBoardLive(t *testing.T) {
	if os.Getenv("BOLTER_LIVE_FUNDING") != "1" {
		t.Skip("set BOLTER_LIVE_FUNDING=1 to read live venue funding")
	}
	settings := Settings{HTTPClient: &http.Client{Timeout: 30 * time.Second}}
	for _, market := range []struct {
		venue      exchange.Venue
		settlement string
	}{
		{exchange.VenueOKX, "USDT"}, {exchange.VenueBitget, "USDT"}, {exchange.VenueBitget, "USDC"},
		{exchange.VenueGate, "USDT"}, {exchange.VenueLighter, "USDC"}, {exchange.VenueHyperliquid, "USDC"},
		{exchange.VenueBinance, "USDT"}, {exchange.VenueBybit, "USDT"}, {exchange.VenueBybit, "USDC"},
	} {
		scope := exchange.CatalogScope{Venue: market.venue, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: market.settlement}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		board, err := NewFundingBoardReader(scope, settings).LoadFundingBoard(ctx)
		cancel()
		if err != nil {
			t.Logf("%s/%s: %v", market.venue, market.settlement, err)
			continue
		}
		if err := board.Validate(market.venue); err != nil {
			t.Errorf("%s/%s invalid: %v", market.venue, market.settlement, err)
			continue
		}
		periods := map[int64]int{}
		withClock := 0
		for _, quote := range board.Quotes {
			if quote.IntervalHours.Valid {
				periods[quote.IntervalHours.Value]++
			}
			if quote.NextFundingTime.After(time.Now()) {
				withClock++
			}
		}
		sample := ""
		for _, quote := range board.Quotes {
			if quote.NativeSymbol == "BTCUSDT" || quote.NativeSymbol == "BTC-USDT-SWAP" || quote.NativeSymbol == "BTC_USDT" || quote.NativeSymbol == "BTC" || quote.NativeSymbol == "BTCPERP" {
				sample = quote.NativeSymbol + " rate=" + quote.Rate.String() + " apr=" + quote.Annualized().Value.StringFixed(4) +
					" next=" + quote.NextFundingTime.Format("15:04")
			}
		}
		t.Logf("%-12s %-4s quotes=%d periods=%v futureClock=%d  %s", market.venue, market.settlement, len(board.Quotes), periods, withClock, sample)
	}
}

func TestHyperliquidFundingStreamLive(t *testing.T) {
	if os.Getenv("BOLTER_LIVE_FUNDING") != "1" {
		t.Skip("set BOLTER_LIVE_FUNDING=1 to read live venue funding")
	}
	scope := exchange.CatalogScope{Venue: exchange.VenueHyperliquid, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDC"}
	stream := NewHyperliquidFundingStream(scope, Settings{HTTPClient: &http.Client{Timeout: 30 * time.Second}})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	boards := make(chan exchange.FundingBoard, 4)
	go func() {
		_ = stream.StreamFundingBoard(ctx, func(board exchange.FundingBoard) { boards <- board; cancel() })
	}()
	select {
	case board := <-boards:
		if err := board.Validate(exchange.VenueHyperliquid); err != nil {
			t.Fatal(err)
		}
		builders := 0
		for _, quote := range board.Quotes {
			if len(quote.NativeID) >= 6 {
				builders++
			}
		}
		t.Logf("hyperliquid stream: %d quotes (%d HIP-3)", len(board.Quotes), builders)
		if builders == 0 {
			t.Fatal("no HIP-3 quotes in the stream board")
		}
	case <-ctx.Done():
		t.Fatal("no board within 40s")
	}
}
