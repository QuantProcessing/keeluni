package factoryclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

// HyperliquidFundingStream is the whole-venue funding board as one WebSocket
// subscription (allDexsAssetCtxs). It covers the primary dex and every HIP-3
// dex in one complete snapshot about every 15 s, and costs no REST weight —
// polling the same over REST would cost 20 weight per dex per read.
//
// The stream carries asset contexts by position, not by coin, so it is joined
// to the dex directory (allPerpMetas + perpDexs + spotMeta, three REST reads)
// that names each position and its collateral. The directory is read when the
// stream starts and re-read every 15 minutes or when a dex grows; markets
// whose collateral is not the board's settlement asset (e.g. USDH or USDE
// builder dexes on a USDC board) and delisted markets are left out.
type HyperliquidFundingStream struct {
	scope    exchange.CatalogScope
	settings Settings
	now      func() time.Time
}

func NewHyperliquidFundingStream(scope exchange.CatalogScope, settings Settings) *HyperliquidFundingStream {
	return &HyperliquidFundingStream{scope: scope, settings: settings, now: time.Now}
}

const hyperliquidDirectoryRefresh = 15 * time.Minute

// hyperliquidFundingSlot names one stream position.
type hyperliquidFundingSlot struct {
	coin    string
	assetID int
	listed  bool
}

type hyperliquidFundingDirectory struct {
	loadedAt time.Time
	// dexes maps dex name ("" = primary) to its positions, or nil when the
	// dex settles in another collateral.
	dexes map[string][]hyperliquidFundingSlot
}

// StreamFundingBoard blocks until ctx ends or the socket fails. Every emitted
// board is complete for the market.
func (stream *HyperliquidFundingStream) StreamFundingBoard(ctx context.Context, emit func(exchange.FundingBoard)) error {
	directory, err := stream.loadDirectory(ctx)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	refreshing := false
	base := hyperliquid.NewWebsocketClient(ctx)
	if stream.settings.WebSocketEndpoint != "" {
		base.WithURL(stream.settings.WebSocketEndpoint)
	}
	defer base.Close()
	failures := make(chan error, 1)
	handler := func(message hyperliquid.WsMessage) {
		var data hyperliquid.WsAllDexsAssetCtxs
		if err := json.Unmarshal(message.Data, &data); err != nil {
			select {
			case failures <- fmt.Errorf("hyperliquid allDexsAssetCtxs: %w", err):
			default:
			}
			return
		}
		mu.Lock()
		current := directory
		stale := stream.now().Sub(current.loadedAt) > hyperliquidDirectoryRefresh || current.outgrown(data)
		if stale && !refreshing {
			refreshing = true
			go func() {
				next, err := stream.loadDirectory(ctx)
				mu.Lock()
				if err == nil {
					directory = next
				}
				refreshing = false
				mu.Unlock()
			}()
		}
		mu.Unlock()
		emit(stream.board(current, data))
	}
	if err := base.Connect(); err != nil {
		return fmt.Errorf("hyperliquid funding stream connect: %w", err)
	}
	if err := base.SubscribeConfirmed("allDexsAssetCtxs", map[string]string{"type": "allDexsAssetCtxs"}, handler); err != nil {
		return fmt.Errorf("hyperliquid funding stream subscribe: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-failures:
		return err
	}
}

func (directory hyperliquidFundingDirectory) outgrown(data hyperliquid.WsAllDexsAssetCtxs) bool {
	for dex, rows := range data.Ctxs {
		slots, known := directory.dexes[dex]
		if !known || (slots != nil && len(rows) > len(slots)) {
			return true
		}
	}
	return false
}

func (stream *HyperliquidFundingStream) board(directory hyperliquidFundingDirectory, data hyperliquid.WsAllDexsAssetCtxs) exchange.FundingBoard {
	now := stream.now().UTC()
	quotes := make([]exchange.FundingQuote, 0, 512)
	for dex, rows := range data.Ctxs {
		slots := directory.dexes[dex]
		for index, row := range rows {
			if index >= len(slots) || !slots[index].listed {
				continue
			}
			rate, err := decimalOrSkip(row.Funding)
			if err != nil {
				continue
			}
			quotes = append(quotes, hyperliquidQuote(slots[index].coin, slots[index].assetID, rate, now))
		}
	}
	return newFundingBoard(stream.scope, now, quotes)
}

func (stream *HyperliquidFundingStream) loadDirectory(ctx context.Context) (hyperliquidFundingDirectory, error) {
	client := hyperliquidperp.NewClient(newHyperliquidPublicSDK(stream.settings))
	metas, err := client.GetAllPerpMetas(ctx)
	if err != nil {
		return hyperliquidFundingDirectory{}, hlNormalizeQueryErr(exchange.ProductPerp, fundingBoardOperation, err, nil)
	}
	dexs, err := client.GetPerpDexs(ctx)
	if err != nil {
		return hyperliquidFundingDirectory{}, hlNormalizeQueryErr(exchange.ProductPerp, fundingBoardOperation, err, nil)
	}
	collaterals, err := hyperliquidCollateralTokens(ctx, stream.settings)
	if err != nil {
		return hyperliquidFundingDirectory{}, err
	}
	names := map[int]string{0: ""}
	for _, dex := range dexs {
		if dex.Index > 0 && strings.TrimSpace(dex.Name) != "" {
			names[dex.Index] = dex.Name
		}
	}
	return buildHyperliquidFundingDirectory(metas, names, collaterals, stream.scope.Settlement, stream.now())
}

func hyperliquidCollateralTokens(ctx context.Context, settings Settings) (map[int]string, error) {
	spot, err := hyperliquidspot.NewClient(newHyperliquidPublicSDK(settings)).GetSpotMeta(ctx)
	if err != nil {
		return nil, hlNormalizeQueryErr(exchange.ProductPerp, fundingBoardOperation, err, nil)
	}
	return hyperliquidCollateralSymbols(spot)
}

// buildHyperliquidFundingDirectory names every stream position of every dex
// settling in settlement. A dex settling in anything else maps to nil so its
// rows are skipped without forcing a directory reload.
func buildHyperliquidFundingDirectory(metas []hyperliquidperp.PrepMeta, names map[int]string, collaterals map[int]string, settlement string, now time.Time) (hyperliquidFundingDirectory, error) {
	directory := hyperliquidFundingDirectory{loadedAt: now, dexes: map[string][]hyperliquidFundingSlot{}}
	for index := range metas {
		name, named := names[index]
		if !named {
			continue // a dex index without a published name cannot be matched
		}
		meta := &metas[index]
		if collaterals[meta.CollateralToken] != settlement {
			directory.dexes[name] = nil
			continue
		}
		slots := make([]hyperliquidFundingSlot, len(meta.Universe))
		for position, market := range meta.Universe {
			slots[position] = hyperliquidFundingSlot{
				coin:    market.Name,
				assetID: hyperliquidAssetID(index, position),
				listed:  !market.IsDelisted && hyperliquidValidPerpCoin(market.Name, name),
			}
		}
		directory.dexes[name] = slots
	}
	if len(directory.dexes) == 0 {
		return directory, hlMalformed(exchange.ProductPerp, fundingBoardOperation, "Hyperliquid returned no perp dex")
	}
	return directory, nil
}
