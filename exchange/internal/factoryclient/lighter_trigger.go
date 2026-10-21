package factoryclient

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/sdk/lighter"
	"github.com/shopspring/decimal"
)

// Lighter perp take-profit / stop-loss: a create-order transaction of type
// stop-loss (2) or take-profit (4), immediate-or-cancel once triggered,
// reduce-only, with a 28-day expiry. Lighter triggers on the mark price. The
// order's price is the worst fill price once it fires: the trigger moved 10%
// against the order. Placement is asynchronous (a transaction hash), so the
// order is read back from the active orders by client order index.

var _ exchange.TriggerOrderREST = (*lighterPerpClient)(nil)

var lighterTriggerSlippage = decimal.RequireFromString("0.1")

func (client *lighterPerpClient) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceMark}
}

func (client *lighterPerpClient) PlaceTriggerOrder(ctx context.Context, req exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	const operation = "PlaceTriggerOrder"
	product := exchange.ProductPerp
	if err := req.Validate(); err != nil {
		return exchange.TriggerOrder{}, lighterInvalid(product, operation, safeTriggerMessage(err))
	}
	if req.PriceType != exchange.TriggerPriceMark {
		return exchange.TriggerOrder{}, lighterInvalid(product, operation, "Lighter triggers on the mark price only")
	}
	meta, err := client.lighterMeta(ctx, operation, product, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	clientID, err := strconv.ParseInt(req.ClientOrderID, 10, 64)
	if err != nil || clientID <= 0 || clientID > lighterMaxClientOrderIndex {
		return exchange.TriggerOrder{}, lighterInvalid(product, operation, "client order id must be a positive uint48 decimal string")
	}
	trigger, err := lighterScaleUint32(req.TriggerPrice, meta.priceScale)
	if err != nil {
		return exchange.TriggerOrder{}, lighterInvalid(product, operation, "trigger price must align to Lighter price tick")
	}
	quantity, err := lighterScaleInt64(req.Quantity, meta.sizeScale)
	if err != nil {
		return exchange.TriggerOrder{}, lighterInvalid(product, operation, "quantity must align to Lighter size tick")
	}
	limit, err := lighterTriggerLimit(req.Side, req.TriggerPrice, meta.priceScale)
	if err != nil {
		return exchange.TriggerOrder{}, lighterInvalid(product, operation, err.Error())
	}
	orderType := uint32(lighter.OrderTypeStopLoss)
	if req.Kind == exchange.TriggerTakeProfit {
		orderType = lighter.OrderTypeTakeProfit
	}
	isAsk := uint32(0)
	if req.Side == exchange.SideSell {
		isAsk = 1
	}
	if err := lighterEnterCommand(ctx, product, operation, client.state); err != nil {
		return exchange.TriggerOrder{}, err
	}
	trackedCtx, tracker := lighterWithSendTracker(ctx)
	resp, err := client.sdk.PlaceOrder(trackedCtx, lighter.CreateOrderRequest{
		MarketId: meta.marketID, Price: limit, BaseAmount: quantity, IsAsk: isAsk, OrderType: orderType,
		ClientOrderId: clientID, TimeInForce: lighter.OrderTimeInForceImmediateOrCancel, ReduceOnly: 1,
		TriggerPrice: trigger, OrderExpiry: lighter.Default28DayOrderExpiry,
	})
	lighterLeaveCommand(client.state)
	if err != nil {
		_, mapped := lighterCommandErr(product, exchange.OrderOperationPlace, meta.instrument, "", req.ClientOrderID, err, client.sdk, tracker)
		return exchange.TriggerOrder{}, withExchangeOperation(mapped, operation)
	}
	if _, err := lighterCommandAck(product, operation, exchange.OrderOperationPlace, meta.instrument, "", req.ClientOrderID, resp.Code, resp.Message, resp.TxHash, client.sdk, tracker); err != nil {
		return exchange.TriggerOrder{}, err
	}
	// Accepted into the sequencer: read it back for its order index. It may
	// not be visible yet; the client order id identifies it meanwhile.
	if found, status, lookupErr := client.LookupTriggerOrder(ctx, req.Instrument, req.ClientOrderID); lookupErr == nil && status == exchange.TriggerFound {
		return found, nil
	}
	return exchange.TriggerOrder{
		NativeSymbol: meta.instrument.Identity.NativeSymbol, NativeID: meta.instrument.Identity.NativeID,
		ClientOrderID: req.ClientOrderID, Side: req.Side, Kind: req.Kind, TriggerPrice: req.TriggerPrice,
		PriceType: exchange.TriggerPriceMark, Quantity: req.Quantity, Status: "pending",
	}, nil
}

func (client *lighterPerpClient) CancelTriggerOrder(ctx context.Context, req exchange.CancelOrderRequest) error {
	_, err := client.CancelOrder(ctx, req)
	if err != nil {
		return withExchangeOperation(err, "CancelTriggerOrder")
	}
	return nil
}

func (client *lighterPerpClient) OpenTriggerOrders(ctx context.Context, req exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	const operation = "OpenTriggerOrders"
	product := exchange.ProductPerp
	var instruments []exchange.Instrument
	if req.Instrument != nil {
		instruments = []exchange.Instrument{*req.Instrument}
	} else {
		// Active orders are read per market: only markets with a position
		// can hold a reduce-only trigger.
		positions, err := client.Positions(ctx, exchange.PositionsRequest{})
		if err != nil {
			return nil, err
		}
		metas, err := client.lighterMetas(ctx, operation, product, lighterPerp)
		if err != nil {
			return nil, err
		}
		for _, position := range positions {
			for _, meta := range metas {
				if meta.instrument.Identity.NativeSymbol == position.NativeSymbol {
					instruments = append(instruments, meta.instrument)
				}
			}
		}
	}
	out := []exchange.TriggerOrder{}
	for _, instrument := range instruments {
		meta, err := client.lighterMeta(ctx, operation, product, lighterPerp, instrument)
		if err != nil {
			return nil, err
		}
		res, err := client.sdk.GetAccountActiveOrders(ctx, meta.marketID)
		if err != nil {
			return nil, lighterNormalizeErr(product, operation, err)
		}
		if res == nil || res.Code != 200 {
			return nil, lighterMalformed(product, operation, "active orders response is malformed")
		}
		for _, row := range res.Orders {
			if row == nil || row.MarketIndex != meta.marketID || !lighterIsTriggerRow(row) {
				continue
			}
			order, err := lighterTriggerOrder(row, meta)
			if err != nil {
				continue
			}
			out = append(out, order)
		}
	}
	return out, nil
}

func (client *lighterPerpClient) LookupTriggerOrder(ctx context.Context, instrument exchange.Instrument, clientOrderID string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	const operation = "LookupTriggerOrder"
	product := exchange.ProductPerp
	meta, err := client.lighterMeta(ctx, operation, product, lighterPerp, instrument)
	if err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	match := func(rows []*lighter.Order) (exchange.TriggerOrder, bool) {
		for _, row := range rows {
			if row == nil || row.MarketIndex != meta.marketID || lighterOrderClientID(row.ClientOrderIndex) != clientOrderID {
				continue
			}
			if order, err := lighterTriggerOrder(row, meta); err == nil {
				return order, true
			}
		}
		return exchange.TriggerOrder{}, false
	}
	active, err := client.sdk.GetAccountActiveOrders(ctx, meta.marketID)
	if err != nil {
		return exchange.TriggerOrder{}, "", lighterNormalizeErr(product, operation, err)
	}
	if active == nil || active.Code != 200 {
		return exchange.TriggerOrder{}, "", lighterMalformed(product, operation, "active orders response is malformed")
	}
	if order, ok := match(active.Orders); ok {
		return order, exchange.TriggerFound, nil
	}
	inactive, err := client.sdk.GetInactiveOrders(ctx, &meta.marketID, 100)
	if err != nil {
		return exchange.TriggerOrder{}, "", lighterNormalizeErr(product, operation, err)
	}
	if inactive == nil || inactive.Code != 200 {
		return exchange.TriggerOrder{}, "", lighterMalformed(product, operation, "inactive orders response is malformed")
	}
	if order, ok := match(inactive.Orders); ok {
		return order, exchange.TriggerFound, nil
	}
	// Lighter is asynchronous: absent from both lists within the newest 100
	// inactive orders is not proof the transaction will never land.
	return exchange.TriggerOrder{}, "", exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{
		Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: "trigger order not visible yet",
	})
}

func lighterTriggerOrder(row *lighter.Order, meta lighterMarketMeta) (exchange.TriggerOrder, error) {
	var kind exchange.TriggerKind
	switch row.OrderType {
	case lighter.OrderTypeRespTakeProfit:
		kind = exchange.TriggerTakeProfit
	case lighter.OrderTypeRespStopLoss:
		kind = exchange.TriggerStopLoss
	default:
		return exchange.TriggerOrder{}, errors.New("not a market take-profit / stop-loss")
	}
	if row.OrderIndex <= 0 {
		return exchange.TriggerOrder{}, errors.New("order id must be positive")
	}
	trigger, err := lighterPositiveDecimal(row.TriggerPrice)
	if err != nil {
		return exchange.TriggerOrder{}, errors.New("invalid trigger price")
	}
	quantity, err := lighterNonNegativeDecimal(defaultString(row.RemainingBaseAmount, row.InitialBaseAmount))
	if err != nil {
		return exchange.TriggerOrder{}, errors.New("invalid trigger size")
	}
	side := exchange.SideBuy
	if row.IsAsk {
		side = exchange.SideSell
	}
	order := exchange.TriggerOrder{
		NativeSymbol: meta.instrument.Identity.NativeSymbol, NativeID: meta.instrument.Identity.NativeID,
		OrderID: strconv.FormatInt(row.OrderIndex, 10), ClientOrderID: lighterOrderClientID(row.ClientOrderIndex),
		Side: side, Kind: kind, TriggerPrice: trigger, PriceType: exchange.TriggerPriceMark, Quantity: quantity,
		Status: lighterTriggerStatus(row.Status, row.TriggerStatus),
	}
	if created := lighterUnixMillis(row.CreatedAt); !created.IsZero() {
		order.CreatedAt = created
	}
	if updated := lighterUnixMillis(row.UpdatedAt); !updated.IsZero() {
		order.UpdatedAt = updated
	}
	return order, nil
}

func lighterTriggerStatus(status lighter.OrderStatus, triggerStatus string) string {
	switch status {
	case lighter.OrderStatusOpen, lighter.OrderStatusPending, lighter.OrderStatusInProgress, "":
		if strings.EqualFold(triggerStatus, "triggered") {
			return "triggered"
		}
		return "open"
	case lighter.OrderStatusFilled:
		return "filled"
	}
	if strings.HasPrefix(string(status), "canceled") {
		return "canceled"
	}
	return string(status)
}

// lighterTriggerLimit is the worst fill price of the triggered order, in
// price ticks: the trigger moved lighterTriggerSlippage against the order.
func lighterTriggerLimit(side exchange.Side, trigger, priceScale decimal.Decimal) (uint32, error) {
	one := decimal.NewFromInt(1)
	bound := trigger.Mul(one.Sub(lighterTriggerSlippage))
	if side == exchange.SideBuy {
		bound = trigger.Mul(one.Add(lighterTriggerSlippage))
	}
	scaled := bound.Mul(priceScale)
	if side == exchange.SideBuy {
		scaled = scaled.Ceil()
	} else {
		scaled = scaled.Floor()
	}
	if !scaled.IsPositive() || scaled.GreaterThan(decimal.NewFromInt(math.MaxUint32)) {
		return 0, errors.New("trigger price out of range for a protected limit")
	}
	return uint32(scaled.IntPart()), nil
}

// lighterIsTriggerRow tells resting trigger orders (take-profit, stop-loss and
// their limit variants) from ordinary ones: they carry a trigger price.
func lighterIsTriggerRow(row *lighter.Order) bool {
	if row == nil {
		return false
	}
	switch row.OrderType {
	case lighter.OrderTypeRespStopLoss, lighter.OrderTypeRespStopLossLimit, lighter.OrderTypeRespTakeProfit, lighter.OrderTypeRespTakeProfitLimit:
		return true
	}
	return lighterOrderOutsideExchangeSubset(row)
}
