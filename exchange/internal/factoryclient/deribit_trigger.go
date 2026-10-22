package factoryclient

import (
	"context"
	"strings"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/sdk/deribit"
	"github.com/shopspring/decimal"
)

// Deribit perpetual take-profit / stop-loss: private/buy or private/sell
// with type take_market or stop_market, trigger_price and trigger
// (last_price | mark_price), reduce-only. The label is the client order id;
// an untriggered order rests as order_state "untriggered".

var _ exchange.TriggerOrderREST = (*deribitPerpClient)(nil)

func (client *deribitPerpClient) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceLast, exchange.TriggerPriceMark}
}

func (client *deribitPerpClient) PlaceTriggerOrder(ctx context.Context, req exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	const operation = "PlaceTriggerOrder"
	if err := deribitContext(ctx, client.meta, operation); err != nil {
		return exchange.TriggerOrder{}, err
	}
	native, err := client.native(operation, req.Instrument)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	if err := req.Validate(); err != nil {
		return exchange.TriggerOrder{}, withExchangeOperation(err, operation)
	}
	request := deribit.OrderRequest{Direction: "buy", Instrument: native, Amount: req.Quantity.String(), Type: "stop_market",
		Label: req.ClientOrderID, ReduceOnly: true, TriggerPrice: req.TriggerPrice.String(), Trigger: "last_price"}
	if req.Side == exchange.SideSell {
		request.Direction = "sell"
	}
	if req.Kind == exchange.TriggerTakeProfit {
		request.Type = "take_market"
	}
	if req.PriceType == exchange.TriggerPriceMark {
		request.Trigger = "mark_price"
	}
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueDeribit, Product: client.meta.product, Operation: exchange.OrderOperationPlace,
		NativeSymbol: native, OrderType: exchange.OrderTypeMarket, ClientOrderID: req.ClientOrderID}
	result, err := client.sdk.Place(ctx, request)
	if err != nil {
		_, mapped := deribitCommandErr(client.meta, ack, err)
		return exchange.TriggerOrder{}, withExchangeOperation(mapped, operation)
	}
	if result.Order.OrderID == "" {
		return exchange.TriggerOrder{}, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product,
			Operation: operation, SafeMessage: "deribit returned no order id"})
	}
	if result.Order.OrderState == "rejected" {
		return exchange.TriggerOrder{}, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product,
			Operation: operation, Code: result.Order.CancelReason, SafeMessage: "deribit rejected the order", VenueMessage: deribitReasonText(result.Order.CancelReason)})
	}
	order, ok := deribitTriggerOrder(result.Order)
	if !ok {
		order = exchange.TriggerOrder{NativeSymbol: native, OrderID: result.Order.OrderID, ClientOrderID: req.ClientOrderID, Side: req.Side, Kind: req.Kind,
			TriggerPrice: req.TriggerPrice, PriceType: req.PriceType, Quantity: req.Quantity, Status: "open"}
	}
	return order, nil
}

func (client *deribitPerpClient) CancelTriggerOrder(ctx context.Context, req exchange.CancelOrderRequest) error {
	if _, err := client.CancelOrder(ctx, req); err != nil {
		return withExchangeOperation(err, "CancelTriggerOrder")
	}
	return nil
}

func (client *deribitPerpClient) OpenTriggerOrders(ctx context.Context, req exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	const operation = "OpenTriggerOrders"
	if err := deribitContext(ctx, client.meta, operation); err != nil {
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
	rows, err := client.sdk.AllOpenOrders(ctx, client.scope.kind)
	if err != nil {
		return nil, deribitNormalizeErr(client.meta, operation, err)
	}
	out := []exchange.TriggerOrder{}
	for _, row := range rows {
		if !client.scope.serves(row.InstrumentName) || (filter != "" && row.InstrumentName != filter) || !deribitIsTriggerRow(row) {
			continue
		}
		if order, ok := deribitTriggerOrder(row); ok {
			out = append(out, order)
		}
	}
	return out, nil
}

func (client *deribitPerpClient) LookupTriggerOrder(ctx context.Context, instrument exchange.Instrument, clientOrderID string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	const operation = "LookupTriggerOrder"
	native, err := client.native(operation, instrument)
	if err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	if clientOrderID == "" {
		return exchange.TriggerOrder{}, "", client.meta.catalogInvalidRequest(operation, "client order id is required")
	}
	rows, err := client.sdk.OrderStateByLabel(ctx, client.scope.labelCurrency(instrument), clientOrderID)
	if err != nil {
		return exchange.TriggerOrder{}, "", deribitNormalizeErr(client.meta, operation, err)
	}
	for _, row := range rows {
		if row.Label != clientOrderID || row.InstrumentName != native {
			continue
		}
		order, ok := deribitTriggerOrder(row)
		if !ok {
			return exchange.TriggerOrder{}, "", deribitMalformed(client.meta, operation, "order is not a take-profit / stop-loss")
		}
		return order, exchange.TriggerFound, nil
	}
	// By-label covers open and recently closed orders only.
	return exchange.TriggerOrder{}, "", exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product,
		Operation: operation, SafeMessage: "trigger order not visible"})
}

// deribitIsTriggerRow tells resting trigger orders (stops, takes, trailing)
// from ordinary ones. Once fired Deribit reports the order it became.
func deribitIsTriggerRow(row deribit.Order) bool {
	if row.OrderState == "untriggered" {
		return true
	}
	switch row.OrderType {
	case "stop_market", "stop_limit", "take_market", "take_limit", "trailing_stop":
		return !row.Triggered
	}
	return false
}

func deribitTriggerOrder(row deribit.Order) (exchange.TriggerOrder, bool) {
	var kind exchange.TriggerKind
	switch row.OrderType {
	case "stop_market":
		kind = exchange.TriggerStopLoss
	case "take_market":
		kind = exchange.TriggerTakeProfit
	default:
		return exchange.TriggerOrder{}, false
	}
	trigger, err := decimal.NewFromString(row.TriggerPrice.String())
	if err != nil || !trigger.IsPositive() {
		return exchange.TriggerOrder{}, false
	}
	priceType := exchange.TriggerPriceLast
	switch strings.ToLower(row.Trigger) {
	case "mark_price":
		priceType = exchange.TriggerPriceMark
	case "index_price":
		return exchange.TriggerOrder{}, false
	}
	status := "open"
	switch row.OrderState {
	case "triggered", "filled":
		status = "triggered"
	case "cancelled":
		status = "canceled"
	case "rejected":
		status = "rejected"
	}
	return exchange.TriggerOrder{NativeSymbol: row.InstrumentName, OrderID: row.OrderID, ClientOrderID: crossExPortableClientOrderID(row.Label),
		Side: deribitSide(row.Direction), Kind: kind, TriggerPrice: trigger, PriceType: priceType, Quantity: numberDecimal(row.Amount),
		Status: status, CreatedAt: krakenMillis(row.CreationTimestamp), UpdatedAt: krakenMillis(row.LastUpdateTimestamp)}, true
}
