package factoryclient

import (
	"context"
	"strings"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/sdk/kraken"
	"github.com/shopspring/decimal"
)

// Kraken Futures take-profit / stop-loss: sendorder with orderType
// take_profit or stp, a stopPrice and no limit price (a market order when
// triggered), reduce-only, triggerSignal last or mark. cliOrdId is the client
// order id; orders/status reads it back, open or recently closed.

var _ exchange.TriggerOrderREST = (*krakenPerpClient)(nil)

func (client *krakenPerpClient) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceLast, exchange.TriggerPriceMark}
}

func (client *krakenPerpClient) PlaceTriggerOrder(ctx context.Context, req exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	const operation = "PlaceTriggerOrder"
	if err := krakenContext(ctx, client.meta, operation); err != nil {
		return exchange.TriggerOrder{}, err
	}
	native, err := client.native(operation, req.Instrument)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	if err := req.Validate(); err != nil {
		return exchange.TriggerOrder{}, withExchangeOperation(err, operation)
	}
	order := kraken.OrderRequest{OrderType: "stp", Symbol: native, Side: "buy", Size: req.Quantity.String(), ClientOrderID: req.ClientOrderID,
		ReduceOnly: true, StopPrice: req.TriggerPrice.String(), TriggerSignal: krakenTriggerSignal(req.PriceType)}
	if req.Kind == exchange.TriggerTakeProfit {
		order.OrderType = "take_profit"
	}
	if req.Side == exchange.SideSell {
		order.Side = "sell"
	}
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueKraken, Product: exchange.ProductPerp, Operation: exchange.OrderOperationPlace,
		NativeSymbol: native, OrderType: exchange.OrderTypeMarket, ClientOrderID: req.ClientOrderID}
	status, err := client.sdk.SendOrder(ctx, order)
	if err != nil {
		_, mapped := krakenCommandErr(client.meta, ack, err)
		return exchange.TriggerOrder{}, withExchangeOperation(mapped, operation)
	}
	if !status.IsAccepted() && status.Status != "untouched" {
		code := status.Status
		for _, event := range status.OrderEvents {
			if event.Type == "REJECT" && event.Reason != "" {
				code = event.Reason
			}
		}
		return exchange.TriggerOrder{}, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product,
			Operation: operation, Code: code, SafeMessage: "kraken rejected the order", VenueMessage: krakenRejectionText(code)})
	}
	if status.OrderID == "" {
		return exchange.TriggerOrder{}, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product,
			Operation: operation, SafeMessage: "kraken placed the order without an order id"})
	}
	return exchange.TriggerOrder{NativeSymbol: native, OrderID: status.OrderID, ClientOrderID: req.ClientOrderID, Side: req.Side, Kind: req.Kind,
		TriggerPrice: req.TriggerPrice, PriceType: req.PriceType, Quantity: req.Quantity, Status: "open"}, nil
}

func (client *krakenPerpClient) CancelTriggerOrder(ctx context.Context, req exchange.CancelOrderRequest) error {
	if _, err := client.CancelOrder(ctx, req); err != nil {
		return withExchangeOperation(err, "CancelTriggerOrder")
	}
	return nil
}

func (client *krakenPerpClient) OpenTriggerOrders(ctx context.Context, req exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	const operation = "OpenTriggerOrders"
	if err := krakenContext(ctx, client.meta, operation); err != nil {
		return nil, err
	}
	filter := ""
	if req.Instrument != nil {
		native, err := client.native(operation, *req.Instrument)
		if err != nil {
			return nil, err
		}
		filter = native
	}
	rows, err := client.sdk.OpenOrders(ctx)
	if err != nil {
		return nil, krakenNormalizeErr(client.meta, operation, err)
	}
	out := []exchange.TriggerOrder{}
	for _, row := range rows {
		if !strings.HasPrefix(row.Symbol, "PF_") || (filter != "" && row.Symbol != filter) {
			continue
		}
		if order, ok := krakenTriggerOrder(row.OrderType, row.Symbol, row.OrderID, row.ClientOrderID, row.Side, row.StopPrice, row.LimitPrice, row.TriggerSignal,
			numberDecimal(row.FilledSize).Add(numberDecimal(row.UnfilledSize)), "open", row.ReceivedTime); ok {
			out = append(out, order)
		}
	}
	return out, nil
}

func (client *krakenPerpClient) LookupTriggerOrder(ctx context.Context, instrument exchange.Instrument, clientOrderID string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	const operation = "LookupTriggerOrder"
	native, err := client.native(operation, instrument)
	if err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	if clientOrderID == "" {
		return exchange.TriggerOrder{}, "", invalid(client.meta, operation, "client order id is required")
	}
	rows, err := client.sdk.OrderStatus(ctx, nil, []string{clientOrderID})
	if err != nil {
		return exchange.TriggerOrder{}, "", krakenNormalizeErr(client.meta, operation, err)
	}
	for _, row := range rows {
		source := row.Order
		if source.Symbol != native || source.ClientOrderID != clientOrderID {
			continue
		}
		status := "open"
		switch row.Status {
		case "CANCELLED":
			status = "canceled"
		case "REJECTED":
			status = "rejected"
		case "FULLY_EXECUTED", "TRIGGERED":
			status = "triggered"
		}
		order, ok := krakenTriggerOrder(source.Type, source.Symbol, source.OrderID, source.ClientOrderID, source.Side, source.StopPrice, source.LimitPrice,
			source.TriggerSignal, numberDecimal(source.Quantity), status, source.Timestamp)
		if !ok {
			return exchange.TriggerOrder{}, "", malformed(client.meta, operation, "order is not a take-profit / stop-loss")
		}
		return order, exchange.TriggerFound, nil
	}
	// orders/status covers open orders and those closed in the last
	// seconds only: absence is not proof.
	return exchange.TriggerOrder{}, "", exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product,
		Operation: operation, SafeMessage: "trigger order not visible"})
}

// krakenIsTriggerType tells resting trigger orders from ordinary ones.
func krakenIsTriggerType(orderType string) bool {
	switch strings.ToLower(orderType) {
	case "stp", "stop", "take_profit", "trailing_stop":
		return true
	}
	return false
}

func krakenTriggerOrder(orderType, symbol, orderID, clientOrderID, side string, stop, limit interface{ String() string }, signal string,
	quantity decimal.Decimal, status, created string) (exchange.TriggerOrder, bool) {
	var kind exchange.TriggerKind
	switch strings.ToLower(orderType) {
	case "stp", "stop":
		kind = exchange.TriggerStopLoss
	case "take_profit":
		kind = exchange.TriggerTakeProfit
	default:
		return exchange.TriggerOrder{}, false
	}
	// A stop with a limit price is a stop-limit: the venue's interface.
	if limitPrice, err := decimal.NewFromString(limit.String()); err == nil && limitPrice.IsPositive() {
		return exchange.TriggerOrder{}, false
	}
	trigger, err := decimal.NewFromString(stop.String())
	if err != nil || !trigger.IsPositive() {
		return exchange.TriggerOrder{}, false
	}
	priceType := exchange.TriggerPriceLast
	switch strings.ToLower(signal) {
	case "mark", "mark_price":
		priceType = exchange.TriggerPriceMark
	case "spot", "index", "spot_price":
		return exchange.TriggerOrder{}, false
	}
	return exchange.TriggerOrder{NativeSymbol: symbol, OrderID: orderID, ClientOrderID: krakenPortableClientOrderID(clientOrderID), Side: krakenSide(side),
		Kind: kind, TriggerPrice: trigger, PriceType: priceType, Quantity: quantity, Status: status, CreatedAt: krakenTime(created)}, true
}

func krakenTriggerSignal(priceType exchange.TriggerPriceType) string {
	if priceType == exchange.TriggerPriceMark {
		return "mark"
	}
	return "last"
}
