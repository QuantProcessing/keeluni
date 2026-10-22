package factoryclient

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	gate "github.com/QuantProcessing/keeluni/sdk/gate"
	"github.com/shopspring/decimal"
)

// Gate futures take-profit / stop-loss: a price-triggered order
// (POST /futures/{settle}/price_orders) whose initial order is a reduce-only
// market close (price 0, ioc) of a fixed number of contracts, fired by rule 1
// (price at or above) or 2 (at or below) on the last or mark price. Gate
// takes no client order id for price orders: the initial order's text
// carries it, and a lookup reads open then recently finished orders.

var _ exchange.TriggerOrderREST = (*gatePerpClient)(nil)

func (client *gatePerpClient) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceLast, exchange.TriggerPriceMark}
}

func (client *gatePerpClient) PlaceTriggerOrder(ctx context.Context, req exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	const operation = "PlaceTriggerOrder"
	if err := gateContext(ctx, client.meta, operation); err != nil {
		return exchange.TriggerOrder{}, err
	}
	if err := req.Validate(); err != nil {
		return exchange.TriggerOrder{}, withExchangeOperation(err, operation)
	}
	native, err := gateRequiredNativeInstrument(client.meta, operation, req.Instrument)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	multiplier, err := client.gateContractMultiplier(operation, req.Instrument)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	contracts := req.Quantity.Div(multiplier)
	if !contracts.IsPositive() || !contracts.Equal(contracts.Truncate(0)) {
		return exchange.TriggerOrder{}, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: operation, SafeMessage: "gate futures quantity must align with the contract multiplier"})
	}
	size := contracts.IntPart()
	if req.Side == exchange.SideSell {
		size = -size
	}
	rule := int32(2)
	if triggerDirection(req.Kind, req.Side) == 1 {
		rule = 1
	}
	priceType := int32(0)
	if req.PriceType == exchange.TriggerPriceMark {
		priceType = 1
	}
	order := gate.FuturesPriceOrder{
		Initial: gate.FuturesPriceInitial{Contract: native, Size: size, Price: "0", TIF: "ioc", Text: gateClientOrderID(req.ClientOrderID), ReduceOnly: true},
		Trigger: gate.FuturesPriceTrigger{StrategyType: 0, PriceType: priceType, Price: req.TriggerPrice.String(), Rule: rule},
	}
	id, err := client.sdk.CreateFuturesPriceOrder(ctx, client.settle, order)
	if err != nil {
		_, mapped := gateCommandErr(client.meta, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, err)
		return exchange.TriggerOrder{}, withExchangeOperation(mapped, operation)
	}
	return exchange.TriggerOrder{NativeSymbol: native, OrderID: strconv.FormatInt(id, 10), ClientOrderID: req.ClientOrderID, Side: req.Side, Kind: req.Kind,
		TriggerPrice: req.TriggerPrice, PriceType: req.PriceType, Quantity: req.Quantity, Status: "open", CreatedAt: time.Now().UTC()}, nil
}

func (client *gatePerpClient) CancelTriggerOrder(ctx context.Context, req exchange.CancelOrderRequest) error {
	const operation = "CancelTriggerOrder"
	if err := gateContext(ctx, client.meta, operation); err != nil {
		return err
	}
	if err := validateGateCancel(client.meta, req); err != nil {
		return err
	}
	id, err := parseGateOrderID(client.meta, operation, req.OrderID)
	if err != nil {
		return err
	}
	if _, err := client.sdk.CancelFuturesPriceOrder(ctx, client.settle, id); err != nil {
		return gateNormalizeErr(client.meta, operation, err)
	}
	return nil
}

func (client *gatePerpClient) OpenTriggerOrders(ctx context.Context, req exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	const operation = "OpenTriggerOrders"
	if err := gateContext(ctx, client.meta, operation); err != nil {
		return nil, err
	}
	native, _, err := gateOptionalNativeInstrument(client.meta, operation, req.Instrument)
	if err != nil {
		return nil, err
	}
	rows, err := client.sdk.ListFuturesPriceOrders(ctx, client.settle, "open", native, 100)
	if err != nil {
		return nil, gateNormalizeErr(client.meta, operation, err)
	}
	return client.gateTriggerOrders(ctx, operation, rows, native), nil
}

func (client *gatePerpClient) LookupTriggerOrder(ctx context.Context, instrument exchange.Instrument, clientOrderID string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	const operation = "LookupTriggerOrder"
	native, err := gateRequiredNativeInstrument(client.meta, operation, instrument)
	if err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	if clientOrderID == "" {
		return exchange.TriggerOrder{}, "", exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: operation, SafeMessage: "client order id is required"})
	}
	for _, status := range []string{"open", "finished"} {
		rows, err := client.sdk.ListFuturesPriceOrders(ctx, client.settle, status, native, 100)
		if err != nil {
			return exchange.TriggerOrder{}, "", gateNormalizeErr(client.meta, operation, err)
		}
		for _, order := range client.gateTriggerOrders(ctx, operation, rows, native) {
			if order.ClientOrderID == clientOrderID {
				return order, exchange.TriggerFound, nil
			}
		}
	}
	// Only the newest 100 finished orders are read: absence is not proof.
	return exchange.TriggerOrder{}, "", exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: client.meta.venue, Product: client.meta.product, Operation: operation, SafeMessage: "trigger order not found in the open or recent orders"})
}

func (client *gatePerpClient) gateTriggerOrders(ctx context.Context, operation string, rows []gate.FuturesPriceOrder, native string) []exchange.TriggerOrder {
	out := []exchange.TriggerOrder{}
	for _, row := range rows {
		if native != "" && row.Initial.Contract != native {
			continue
		}
		// A limit initial order is a stop-limit: the venue's interface.
		if price, err := decimal.NewFromString(row.Initial.Price); err == nil && !price.IsZero() {
			continue
		}
		if row.Trigger.PriceType == 2 || row.Trigger.StrategyType != 0 {
			continue
		}
		trigger, err := decimal.NewFromString(row.Trigger.Price)
		if err != nil || !trigger.IsPositive() {
			continue
		}
		side := exchange.SideBuy
		switch {
		case row.Initial.Size < 0, strings.Contains(row.OrderType, "close-long"):
			side = exchange.SideSell
		}
		// The rule and the closing side give the kind: a sell firing on a
		// rise takes profit, a sell firing on a fall stops a loss.
		kind := exchange.TriggerStopLoss
		if (row.Trigger.Rule == 1) == (side == exchange.SideSell) {
			kind = exchange.TriggerTakeProfit
		}
		quantity := decimal.Zero
		closePosition := row.Initial.Size == 0
		if !closePosition {
			instrument, err := gateCatalogInstrumentByNative(ctx, client.meta, operation, row.Initial.Contract)
			if err != nil {
				continue
			}
			multiplier, err := client.gateContractMultiplier(operation, instrument)
			if err != nil {
				continue
			}
			quantity = decimal.NewFromInt(row.Initial.Size).Abs().Mul(multiplier)
		}
		priceType := exchange.TriggerPriceLast
		if row.Trigger.PriceType == 1 {
			priceType = exchange.TriggerPriceMark
		}
		status := "open"
		if row.Status != "open" {
			switch row.FinishAs {
			case "succeeded":
				status = "triggered"
			case "failed":
				status = "rejected"
			default:
				status = "canceled"
			}
		}
		order := exchange.TriggerOrder{NativeSymbol: row.Initial.Contract, OrderID: strconv.FormatInt(row.ID, 10), ClientOrderID: gatePortableClientOrderID(row.Initial.Text),
			Side: side, Kind: kind, TriggerPrice: trigger, PriceType: priceType, Quantity: quantity, ClosePosition: closePosition, Status: status}
		if row.CreateTime > 0 {
			order.CreatedAt = time.UnixMilli(int64(row.CreateTime * 1000)).UTC()
		}
		out = append(out, order)
	}
	return out
}
