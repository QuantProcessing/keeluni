package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	hyperliquid "github.com/QuantProcessing/keeluni/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/keeluni/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

// Hyperliquid perp take-profit / stop-loss: an "order" action whose type is
// trigger {isMarket: true, triggerPx, tpsl: tp|sl}, reduce-only. Hyperliquid
// compares triggers with the mark price only. A market trigger still carries
// a limit price, the worst price it may fill at: we send the trigger price
// moved 10% against the order (Hyperliquid's own interface uses the same
// slippage bound), rounded to the venue's price rule.

var _ exchange.TriggerOrderREST = (*hyperliquidPerpClient)(nil)

// hlTriggerSlippage bounds a triggered market order's fill price.
var hlTriggerSlippage = decimal.RequireFromString("0.1")

func (client *hyperliquidPerpClient) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceMark}
}

func (client *hyperliquidPerpClient) PlaceTriggerOrder(ctx context.Context, req exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	const operation = "PlaceTriggerOrder"
	if err := hlRequireCtx(ctx, exchange.ProductPerp, operation); err != nil {
		return exchange.TriggerOrder{}, err
	}
	if err := req.Validate(); err != nil {
		return exchange.TriggerOrder{}, hlInvalid(exchange.ProductPerp, operation, safeTriggerMessage(err))
	}
	if req.PriceType != exchange.TriggerPriceMark {
		return exchange.TriggerOrder{}, hlInvalid(exchange.ProductPerp, operation, "Hyperliquid triggers on the mark price only")
	}
	meta, err := client.perpMeta(ctx, operation, req.Instrument)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	if !hlSameInstrument(meta.instrument, req.Instrument) {
		return exchange.TriggerOrder{}, hlInvalid(exchange.ProductPerp, operation, "instrument identity mismatch")
	}
	if !req.Quantity.Mod(meta.instrument.Rules.QuantityIncrement).IsZero() {
		return exchange.TriggerOrder{}, hlInvalid(exchange.ProductPerp, operation, "quantity must align to Hyperliquid size increment")
	}
	trigger, err := hlValidateLimitPrice(req.Side, req.TriggerPrice, meta.priceDecimals)
	if err != nil {
		return exchange.TriggerOrder{}, hlInvalid(exchange.ProductPerp, operation, "trigger price cannot be represented within Hyperliquid price precision")
	}
	limit, err := hlTriggerLimitPrice(req.Side, trigger, meta.priceDecimals)
	if err != nil {
		return exchange.TriggerOrder{}, hlInvalid(exchange.ProductPerp, operation, err.Error())
	}
	tpsl := hyperliquid.StopLoss
	if req.Kind == exchange.TriggerTakeProfit {
		tpsl = hyperliquid.TakeProfit
	}
	nativeClientID := hlNativeClientOrderID(req.ClientOrderID)
	if err := ctx.Err(); err != nil {
		return exchange.TriggerOrder{}, hlContextErr(exchange.ProductPerp, operation, err)
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	status, err := client.sdk.PlaceOrder(requestCtx, hyperliquidperp.PlaceOrderRequest{
		AssetID:       meta.assetID,
		IsBuy:         req.Side == exchange.SideBuy,
		Price:         hlMustFloat(limit),
		Size:          hlMustFloat(req.Quantity),
		ReduceOnly:    true,
		OrderType:     hyperliquidperp.OrderType{Trigger: &hyperliquidperp.OrderTypeTrigger{IsMarket: true, TriggerPx: hlMustFloat(trigger), Tpsl: tpsl}},
		ClientOrderID: hlOptionalString(nativeClientID),
	})
	if err != nil {
		_, mapped := hlMutationErr(exchange.ProductPerp, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, err, tracker)
		return exchange.TriggerOrder{}, withExchangeOperation(mapped, operation)
	}
	order := exchange.TriggerOrder{
		NativeSymbol: meta.instrument.Identity.NativeSymbol, NativeID: meta.instrument.Identity.NativeID,
		ClientOrderID: req.ClientOrderID, Side: req.Side, Kind: req.Kind, TriggerPrice: trigger,
		PriceType: exchange.TriggerPriceMark, Quantity: req.Quantity, Status: "open", CreatedAt: time.Now().UTC(),
	}
	switch {
	case status != nil && status.Resting != nil && status.Resting.Oid > 0:
		order.OrderID = strconv.FormatInt(status.Resting.Oid, 10)
		return order, nil
	case status != nil && status.Waiting == "waitingForTrigger":
		// Accepted, but the reply carries no order id: read it back by cloid.
		found, lookupStatus, lookupErr := client.LookupTriggerOrder(ctx, req.Instrument, req.ClientOrderID)
		if lookupErr == nil && lookupStatus == exchange.TriggerFound {
			return found, nil
		}
		return order, nil
	case status != nil && status.Filled != nil:
		// A trigger already through its price fires at once.
		order.OrderID = strconv.Itoa(status.Filled.Oid)
		order.Status = "triggered"
		return order, nil
	}
	return exchange.TriggerOrder{}, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{
		Venue: hyperliquidVenue, Product: exchange.ProductPerp, Operation: operation,
		SafeMessage: "order command outcome is unknown after possible send",
	})
}

func (client *hyperliquidPerpClient) CancelTriggerOrder(ctx context.Context, req exchange.CancelOrderRequest) error {
	const operation = "CancelTriggerOrder"
	ack, err := client.CancelOrder(ctx, req)
	if err != nil {
		return withExchangeOperation(err, operation)
	}
	_ = ack
	return nil
}

func (client *hyperliquidPerpClient) OpenTriggerOrders(ctx context.Context, req exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	const operation = "OpenTriggerOrders"
	if err := hlRequireCtx(ctx, exchange.ProductPerp, operation); err != nil {
		return nil, err
	}
	metas, requested, err := client.perpMetasForOptional(ctx, operation, req.Instrument)
	if err != nil {
		return nil, err
	}
	// Builder dexs (xyz:TSLA) list their orders apart from the main dex, at
	// 20 weight a read: the main dex, plus each dex the account holds a
	// position on (a reduce-only trigger needs one).
	dexs := map[string]bool{"": true}
	if requested != nil {
		dexs = map[string]bool{hlDexOf(requested.nativeCoin): true}
	} else if positions, err := client.Positions(ctx, exchange.PositionsRequest{}); err == nil {
		for _, position := range positions {
			for coin, meta := range metas {
				if meta.instrument.Identity.NativeSymbol == position.NativeSymbol {
					dexs[hlDexOf(coin)] = true
				}
			}
		}
	}
	out := []exchange.TriggerOrder{}
	for dex := range dexs {
		requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
		rows, err := client.sdk.UserOpenOrdersForDex(requestCtx, client.sdk.AccountAddr, dex)
		if err != nil {
			if dex != "" && requested == nil {
				continue
			}
			return nil, hlNormalizeQueryErr(exchange.ProductPerp, operation, err, tracker)
		}
		for _, row := range rows {
			if !hlIsTriggerRow(row.IsTrigger, row.OrderType) {
				continue
			}
			if requested != nil && row.Coin != requested.nativeCoin {
				continue
			}
			meta, ok := metas[row.Coin]
			if !ok {
				continue
			}
			order, err := hlTriggerOrder(meta, row)
			if err != nil {
				// Stop-limit and other trigger shapes: the venue's interface.
				continue
			}
			out = append(out, order)
		}
	}
	return out, nil
}

func (client *hyperliquidPerpClient) LookupTriggerOrder(ctx context.Context, instrument exchange.Instrument, clientOrderID string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	const operation = "LookupTriggerOrder"
	if err := hlRequireCtx(ctx, exchange.ProductPerp, operation); err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	meta, err := client.perpMeta(ctx, operation, instrument)
	if err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	cloid := hlNativeClientOrderID(clientOrderID)
	if cloid == "" {
		return exchange.TriggerOrder{}, "", hlInvalid(exchange.ProductPerp, operation, "client order id must be a positive decimal")
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	row, err := client.sdk.OrderStatusByCloid(requestCtx, client.sdk.AccountAddr, cloid)
	if errors.Is(err, hyperliquid.ErrOrderNotFound) {
		return exchange.TriggerOrder{}, exchange.TriggerNotFound, nil
	}
	if err != nil {
		return exchange.TriggerOrder{}, "", hlNormalizeQueryErr(exchange.ProductPerp, operation, err, tracker)
	}
	if row.Coin != meta.nativeCoin {
		return exchange.TriggerOrder{}, "", hlMalformed(exchange.ProductPerp, operation, "order belongs to another instrument")
	}
	order, err := hlTriggerOrder(meta, hyperliquidperp.Order{
		Coin: row.Coin, Side: row.Side, LimitPx: row.LimitPx, Sz: row.Sz, Oid: row.Oid, Cliod: row.Cliod,
		Timestamp: row.Timestamp, OrigSz: row.OrigSz, ReduceOnly: row.ReduceOnly, OrderType: row.OrderType,
		Tif: row.Tif, IsTrigger: row.IsTrigger, TriggerPx: row.TriggerPx,
	})
	if err != nil {
		return exchange.TriggerOrder{}, "", hlMalformed(exchange.ProductPerp, operation, err.Error())
	}
	order.Status = hlTriggerStatus(row.Status)
	return order, exchange.TriggerFound, nil
}

// hlIsTriggerRow tells trigger orders (resting until their price) from
// ordinary ones. Once triggered the venue reports the sent order as Market.
func hlIsTriggerRow(isTrigger bool, orderType string) bool {
	return isTrigger || strings.HasPrefix(orderType, "Stop") || strings.HasPrefix(orderType, "Take Profit")
}

func hlTriggerOrder(meta hyperliquidMarketMeta, row hyperliquidperp.Order) (exchange.TriggerOrder, error) {
	if row.Coin != meta.nativeCoin || row.Oid <= 0 {
		return exchange.TriggerOrder{}, fmt.Errorf("trigger identity mismatch")
	}
	var kind exchange.TriggerKind
	switch row.OrderType {
	case "Take Profit Market":
		kind = exchange.TriggerTakeProfit
	case "Stop Market":
		kind = exchange.TriggerStopLoss
	default:
		return exchange.TriggerOrder{}, fmt.Errorf("unsupported trigger order type %q", row.OrderType)
	}
	side, err := hlSide(row.Side)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	trigger, err := hlPositiveDecimal(row.TriggerPx)
	if err != nil {
		return exchange.TriggerOrder{}, fmt.Errorf("invalid trigger price")
	}
	quantity, err := hlNonNegativeDecimal(defaultString(row.OrigSz, row.Sz))
	if err != nil {
		return exchange.TriggerOrder{}, fmt.Errorf("invalid trigger size")
	}
	order := exchange.TriggerOrder{
		NativeSymbol: meta.instrument.Identity.NativeSymbol, NativeID: meta.instrument.Identity.NativeID,
		OrderID: strconv.FormatInt(row.Oid, 10), ClientOrderID: hlPortableClientOrderID(row.Cliod),
		Side: side, Kind: kind, TriggerPrice: trigger, PriceType: exchange.TriggerPriceMark,
		Quantity: quantity, ClosePosition: row.IsPositionTpsl || quantity.IsZero(), Status: "open",
	}
	if order.ClosePosition {
		order.Quantity = decimal.Zero
	}
	if row.Timestamp > 0 {
		order.CreatedAt = time.UnixMilli(row.Timestamp).UTC()
	}
	return order, nil
}

func hlTriggerStatus(status string) string {
	switch status {
	case "open", "":
		return "open"
	case "triggered":
		return "triggered"
	case "filled":
		return "filled"
	}
	if strings.HasSuffix(status, "Rejected") {
		return "rejected"
	}
	return "canceled"
}

// hlTriggerLimitPrice is the worst fill price of a triggered market order:
// the trigger moved hlTriggerSlippage against the order, then rounded the
// same way (down for a sell, up for a buy) to five significant figures and
// the price decimals.
func hlTriggerLimitPrice(side exchange.Side, trigger decimal.Decimal, priceDecimals int) (decimal.Decimal, error) {
	one := decimal.NewFromInt(1)
	bound := trigger.Mul(one.Sub(hlTriggerSlippage))
	if side == exchange.SideBuy {
		bound = trigger.Mul(one.Add(hlTriggerSlippage))
	}
	// Five significant figures, at most priceDecimals places; an integer
	// price is always valid.
	places := int32(0)
	if bound.GreaterThanOrEqual(one) {
		places = int32(5 - len(bound.Truncate(0).String()))
	} else {
		// 0.000123: three leading zeros after the point.
		zeros := int32(0)
		for scaled := bound.Mul(decimal.NewFromInt(10)); scaled.LessThan(one); scaled = scaled.Mul(decimal.NewFromInt(10)) {
			zeros++
		}
		places = zeros + 5
	}
	if places < 0 {
		places = 0
	}
	if places > int32(priceDecimals) {
		places = int32(priceDecimals)
	}
	if side == exchange.SideBuy {
		bound = bound.RoundCeil(places)
	} else {
		bound = bound.RoundFloor(places)
	}
	if !bound.IsPositive() {
		return decimal.Zero, fmt.Errorf("trigger price too small for a protected limit")
	}
	return bound, nil
}

// hlDexOf is the builder dex of a coin ("xyz" for "xyz:TSLA").
func hlDexOf(coin string) string {
	if dex, _, ok := strings.Cut(coin, ":"); ok {
		return dex
	}
	return ""
}

func safeTriggerMessage(err error) string {
	var normalized *exchange.Error
	if errors.As(err, &normalized) && normalized.Details().SafeMessage != "" {
		return normalized.Details().SafeMessage
	}
	return "invalid trigger order request"
}
