package factoryclient

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantProcessing/keeluni/exchange"
	bitget "github.com/QuantProcessing/keeluni/sdk/bitget"
	"github.com/shopspring/decimal"
)

// Bitget UTA take-profit / stop-loss: a strategy order of type tpsl in
// partial mode (a fixed quantity) carrying takeProfit or stopLoss, a market
// order on trigger, triggered by the last ("market") or mark price. In
// one-way mode it is reduce-only; in hedge mode the position side closes it
// (side sell + posSide long closes a long). clientOid is the client order id.

var _ exchange.TriggerOrderREST = (*bitgetPerpClient)(nil)

func (c *bitgetPerpClient) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceLast, exchange.TriggerPriceMark}
}

func (c *bitgetPerpClient) PlaceTriggerOrder(ctx context.Context, req exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	const operation = "PlaceTriggerOrder"
	if err := ctxErr(ctx, c.meta, operation); err != nil {
		return exchange.TriggerOrder{}, err
	}
	if err := req.Validate(); err != nil {
		return exchange.TriggerOrder{}, invalid(c.meta, operation, safeTriggerMessage(err))
	}
	native, _, err := c.symbols(req.Instrument, operation)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	// The closing order is reduce-only: in hedge mode that is the position
	// side of the position it closes.
	positionSide, err := c.bitgetPositionSide(ctx, exchange.PlaceOrderRequest{Side: req.Side, ReduceOnly: true})
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	request := &bitget.StrategyOrderRequest{
		Category: c.bitgetPrivateCategory(), Symbol: native, Type: "tpsl", Side: strings.ToLower(string(req.Side)),
		PosSide: positionSide, Qty: req.Quantity.String(), MarginMode: "crossed", ClientOID: req.ClientOrderID, TPSLMode: "partial",
	}
	if positionSide == "" {
		request.ReduceOnly = "yes"
	}
	triggerBy := bitgetTriggerBy(req.PriceType)
	if req.Kind == exchange.TriggerTakeProfit {
		request.TakeProfit, request.TPTriggerBy, request.TPOrderType = req.TriggerPrice.String(), triggerBy, "market"
	} else {
		request.StopLoss, request.SLTriggerBy, request.SLOrderType = req.TriggerPrice.String(), triggerBy, "market"
	}
	resp, err := c.sdk.PlaceStrategyOrder(ctx, request)
	if err != nil {
		return exchange.TriggerOrder{}, bybitBitgetTriggerCommandErr(c.meta, operation, native, req.ClientOrderID, err)
	}
	return exchange.TriggerOrder{
		NativeSymbol: native, OrderID: resp.OrderID, ClientOrderID: req.ClientOrderID, Side: req.Side, Kind: req.Kind,
		TriggerPrice: req.TriggerPrice, PriceType: req.PriceType, Quantity: req.Quantity, Status: "open",
	}, nil
}

func (c *bitgetPerpClient) CancelTriggerOrder(ctx context.Context, req exchange.CancelOrderRequest) error {
	const operation = "CancelTriggerOrder"
	if err := ctxErr(ctx, c.meta, operation); err != nil {
		return err
	}
	if _, _, err := c.symbols(req.Instrument, operation); err != nil {
		return err
	}
	if !positiveDecimalOrderID(req.OrderID) {
		return invalid(c.meta, operation, "order id must be a positive decimal int64")
	}
	if err := c.sdk.CancelStrategyOrder(ctx, req.OrderID); err != nil {
		return normErr(c.meta, operation, err)
	}
	return nil
}

func (c *bitgetPerpClient) OpenTriggerOrders(ctx context.Context, req exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	const operation = "OpenTriggerOrders"
	if err := ctxErr(ctx, c.meta, operation); err != nil {
		return nil, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, operation)
	if err != nil {
		return nil, err
	}
	rows, err := c.sdk.GetUnfilledStrategyOrders(ctx, c.bitgetPrivateCategory(), "tpsl")
	if err != nil {
		return nil, normErr(c.meta, operation, err)
	}
	out := []exchange.TriggerOrder{}
	for _, row := range rows {
		if native != "" && row.Symbol != native {
			continue
		}
		out = append(out, bitgetTriggerOrders(row)...)
	}
	return out, nil
}

func (c *bitgetPerpClient) LookupTriggerOrder(ctx context.Context, instrument exchange.Instrument, clientOrderID string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	const operation = "LookupTriggerOrder"
	native, _, err := c.symbols(instrument, operation)
	if err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	if clientOrderID == "" {
		return exchange.TriggerOrder{}, "", invalid(c.meta, operation, "client order id is required")
	}
	find := func(rows []bitget.StrategyOrder) (exchange.TriggerOrder, bool) {
		for _, row := range rows {
			if row.ClientOID == clientOrderID && row.Symbol == native {
				if orders := bitgetTriggerOrders(row); len(orders) > 0 {
					return orders[0], true
				}
			}
		}
		return exchange.TriggerOrder{}, false
	}
	open, err := c.sdk.GetUnfilledStrategyOrders(ctx, c.bitgetPrivateCategory(), "tpsl")
	if err != nil {
		return exchange.TriggerOrder{}, "", normErr(c.meta, operation, err)
	}
	if order, ok := find(open); ok {
		return order, exchange.TriggerFound, nil
	}
	history, err := c.sdk.GetStrategyOrderHistory(ctx, c.bitgetPrivateCategory(), "tpsl")
	if err != nil {
		return exchange.TriggerOrder{}, "", normErr(c.meta, operation, err)
	}
	if order, ok := find(history); ok {
		return order, exchange.TriggerFound, nil
	}
	// Only the newest 100 finished orders are read: absence is not proof.
	return exchange.TriggerOrder{}, "", exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: c.meta.venue, Product: c.meta.product, Operation: operation, SafeMessage: "trigger order not found in the open or recent orders"})
}

// bitgetTriggerOrders projects one tpsl strategy order: a full-position
// tpsl can carry both legs; each market leg is one trigger.
func bitgetTriggerOrders(row bitget.StrategyOrder) []exchange.TriggerOrder {
	if row.OrderID == "" {
		return nil
	}
	side := exchange.SideSell
	switch {
	case strings.EqualFold(row.Side, "buy"):
		side = exchange.SideBuy
	case row.Side == "" && strings.EqualFold(row.PosSide, "short"):
		side = exchange.SideBuy
	}
	quantity, _ := decimal.NewFromString(row.Qty)
	closePosition := strings.EqualFold(row.TPSLMode, "full") || quantity.IsZero()
	base := exchange.TriggerOrder{
		NativeSymbol: row.Symbol, OrderID: row.OrderID, ClientOrderID: row.ClientOID, Side: side,
		Quantity: quantity, ClosePosition: closePosition, Status: bitgetTriggerStatus(row.Status),
	}
	if closePosition {
		base.Quantity = decimal.Zero
	}
	if created, err := parseMilliString(row.CreatedTime); err == nil {
		base.CreatedAt = created
	}
	var out []exchange.TriggerOrder
	add := func(kind exchange.TriggerKind, raw, triggerBy, orderType string) {
		price, err := decimal.NewFromString(raw)
		if err != nil || !price.IsPositive() || (orderType != "" && !strings.EqualFold(orderType, "market")) {
			return
		}
		order := base
		order.Kind, order.TriggerPrice, order.PriceType = kind, price, exchange.TriggerPriceLast
		if strings.EqualFold(triggerBy, "mark") {
			order.PriceType = exchange.TriggerPriceMark
		}
		out = append(out, order)
	}
	add(exchange.TriggerTakeProfit, row.TakeProfit, row.TPTriggerBy, row.TPOrderType)
	add(exchange.TriggerStopLoss, row.StopLoss, row.SLTriggerBy, row.SLOrderType)
	return out
}

func bitgetTriggerStatus(status string) string {
	switch strings.ToLower(status) {
	case "pending", "submitting", "":
		return "open"
	case "success":
		return "triggered"
	case "failed":
		return "rejected"
	}
	return "canceled"
}

func bitgetTriggerBy(priceType exchange.TriggerPriceType) string {
	if priceType == exchange.TriggerPriceMark {
		return "mark"
	}
	return "market"
}

// bybitBitgetTriggerCommandErr maps a trigger placement failure with the
// order rules: a definitive venue rejection is rejected, anything after a
// possible send is ambiguous.
func bybitBitgetTriggerCommandErr(meta clientMeta, operation, native, clientOrderID string, err error) error {
	ambiguous := exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "order command outcome is unknown after possible send"})
	if isOrderActionMalformed(err) {
		return ambiguous
	}
	ack, mapped := commandAck(meta, operation, exchange.OrderOperationPlace, native, "", clientOrderID, err)
	if ack.State == exchange.AckRejected {
		// A code the SDK documents as a definitive rejection.
		return withVenueMessage(exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, Code: ack.VenueCode, SafeMessage: "venue rejected order command"}), nil, err)
	}
	// Any other venue code (not in the SDK's rejection taxonomy) or failure
	// after a possible send is ambiguous: the order may rest on the venue.
	// Errors proven to precede sending (auth, local rate limit) pass through.
	var normalized *exchange.Error
	if errors.As(mapped, &normalized) {
		switch {
		case normalized.Details().CommandNotSent,
			normalized.Kind() == exchange.KindAuthentication, normalized.Kind() == exchange.KindPermission,
			normalized.Kind() == exchange.KindInvalidRequest && normalized.Details().HTTPStatus == 0:
			return mapped
		}
	}
	return ambiguous
}
