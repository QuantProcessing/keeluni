package factoryclient

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	binanceperp "github.com/QuantProcessing/keeluni/sdk/binance/perp"
)

// Binance USD-M take-profit / stop-loss: conditional algo orders
// (POST /fapi/v1/algoOrder, algoType CONDITIONAL) of type TAKE_PROFIT_MARKET
// or STOP_MARKET, reduce-only, keyed by clientAlgoId. Since the 2025 algo
// migration they are listed and cancelled apart from ordinary orders.

var _ exchange.TriggerOrderREST = (*binancePerpClient)(nil)

func (client *binancePerpClient) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceLast, exchange.TriggerPriceMark}
}

func (client *binancePerpClient) PlaceTriggerOrder(ctx context.Context, req exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	const operation = "PlaceTriggerOrder"
	if err := client.binancePerpReady(ctx, operation); err != nil {
		return exchange.TriggerOrder{}, err
	}
	if err := req.Validate(); err != nil {
		return exchange.TriggerOrder{}, binancePerpInvalidRequest(operation, err.Error())
	}
	canonical, native, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.TriggerOrder{}, binancePerpInvalidRequest(operation, err.Error())
	}
	params := binanceperp.NewAlgoOrderParams{
		Symbol:       native,
		Side:         strings.ToUpper(string(req.Side)),
		Type:         binancePerpTriggerType(req.Kind),
		Quantity:     req.Quantity.String(),
		TriggerPrice: req.TriggerPrice.String(),
		WorkingType:  binancePerpWorkingType(req.PriceType),
		ReduceOnly:   true,
		ClientAlgoID: req.ClientOrderID,
	}
	resp, err := client.sdk.NewAlgoOrder(ctx, params)
	if err != nil {
		ack := binancePerpAck(exchange.OrderOperationPlace, canonical, "", req.ClientOrderID)
		_, mapped := binancePerpCommandErrorAck(operation, ack, err)
		return exchange.TriggerOrder{}, mapped
	}
	if resp == nil {
		return exchange.TriggerOrder{}, binancePerpAmbiguous(operation)
	}
	order, err := binancePerpTriggerOrder(*resp)
	if err != nil {
		// Sent and answered, but unreadable: the order may exist.
		return exchange.TriggerOrder{}, binancePerpAmbiguous(operation)
	}
	if order.NativeSymbol != native || order.ClientOrderID != req.ClientOrderID {
		return exchange.TriggerOrder{}, binancePerpAmbiguous(operation)
	}
	return order, nil
}

func (client *binancePerpClient) CancelTriggerOrder(ctx context.Context, req exchange.CancelOrderRequest) error {
	const operation = "CancelTriggerOrder"
	if err := client.binancePerpReady(ctx, operation); err != nil {
		return err
	}
	if _, _, err := binancePerpRequestSymbols(req.Instrument); err != nil {
		return binancePerpInvalidRequest(operation, err.Error())
	}
	algoID, err := strconv.ParseInt(req.OrderID, 10, 64)
	if err != nil || algoID <= 0 || strconv.FormatInt(algoID, 10) != req.OrderID {
		return binancePerpInvalidRequest(operation, "order id must be a positive decimal int64")
	}
	if _, err := client.sdk.CancelAlgoOrder(ctx, binanceperp.AlgoOrderLookupParams{AlgoID: algoID}); err != nil {
		return binancePerpNormalizeError(operation, err)
	}
	return nil
}

func (client *binancePerpClient) OpenTriggerOrders(ctx context.Context, req exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	const operation = "OpenTriggerOrders"
	if err := client.binancePerpReady(ctx, operation); err != nil {
		return nil, err
	}
	native := ""
	if req.Instrument != nil {
		_, symbol, err := binancePerpRequestSymbols(*req.Instrument)
		if err != nil {
			return nil, binancePerpInvalidRequest(operation, err.Error())
		}
		native = symbol
	}
	rows, err := client.sdk.QueryOpenAlgoOrders(ctx, binanceperp.QueryOpenAlgoOrdersParams{Symbol: native})
	if err != nil {
		return nil, binancePerpNormalizeError(operation, err)
	}
	out := make([]exchange.TriggerOrder, 0, len(rows))
	for _, row := range rows {
		order, err := binancePerpTriggerOrder(row)
		if err != nil {
			// Trailing stops and other algo shapes are not take-profit /
			// stop-loss orders: left to the venue's own interface.
			continue
		}
		if native != "" && order.NativeSymbol != native {
			return nil, binancePerpMalformed(operation, "response instrument mismatch")
		}
		out = append(out, order)
	}
	return out, nil
}

func (client *binancePerpClient) LookupTriggerOrder(ctx context.Context, instrument exchange.Instrument, clientOrderID string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	const operation = "LookupTriggerOrder"
	if err := client.binancePerpReady(ctx, operation); err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	_, native, err := binancePerpRequestSymbols(instrument)
	if err != nil {
		return exchange.TriggerOrder{}, "", binancePerpInvalidRequest(operation, err.Error())
	}
	if clientOrderID == "" {
		return exchange.TriggerOrder{}, "", binancePerpInvalidRequest(operation, "client order id is required")
	}
	resp, err := client.sdk.QueryAlgoOrder(ctx, binanceperp.AlgoOrderLookupParams{ClientAlgoID: clientOrderID})
	if binancePerpExactLookupNotFound(err) {
		return exchange.TriggerOrder{}, exchange.TriggerNotFound, nil
	}
	if err != nil {
		return exchange.TriggerOrder{}, "", binancePerpNormalizeError(operation, err)
	}
	if resp == nil {
		return exchange.TriggerOrder{}, "", binancePerpMalformed(operation, "missing algo order response")
	}
	order, err := binancePerpTriggerOrder(*resp)
	if err != nil {
		return exchange.TriggerOrder{}, "", binancePerpMalformed(operation, err.Error())
	}
	if order.NativeSymbol != native || order.ClientOrderID != clientOrderID {
		return exchange.TriggerOrder{}, "", binancePerpMalformed(operation, "response does not match the requested order")
	}
	return order, exchange.TriggerFound, nil
}

func binancePerpTriggerType(kind exchange.TriggerKind) string {
	if kind == exchange.TriggerTakeProfit {
		return "TAKE_PROFIT_MARKET"
	}
	return "STOP_MARKET"
}

func binancePerpWorkingType(priceType exchange.TriggerPriceType) string {
	if priceType == exchange.TriggerPriceMark {
		return "MARK_PRICE"
	}
	return "CONTRACT_PRICE"
}

func binancePerpTriggerOrder(resp binanceperp.AlgoOrderResponse) (exchange.TriggerOrder, error) {
	if resp.AlgoID <= 0 {
		return exchange.TriggerOrder{}, errors.New("invalid algo id")
	}
	canonical, _, err := binancePerpNativeSymbols(resp.Symbol)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	var kind exchange.TriggerKind
	switch resp.OrderType {
	case "TAKE_PROFIT_MARKET", "TAKE_PROFIT":
		kind = exchange.TriggerTakeProfit
	case "STOP_MARKET", "STOP":
		kind = exchange.TriggerStopLoss
	default:
		return exchange.TriggerOrder{}, errors.New("unsupported algo order type")
	}
	if resp.PositionSide != "" && resp.PositionSide != "BOTH" {
		return exchange.TriggerOrder{}, errors.New("hedge position side is not supported")
	}
	side, err := binancePerpExchangeSide(resp.Side)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	trigger, err := binancePerpNonNegativeDecimal(resp.TriggerPrice)
	if err != nil || !trigger.IsPositive() {
		return exchange.TriggerOrder{}, errors.New("invalid trigger price")
	}
	quantity, err := binancePerpNonNegativeDecimal(defaultString(resp.Quantity, "0"))
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	if quantity.IsZero() && !resp.ClosePosition {
		return exchange.TriggerOrder{}, errors.New("missing quantity")
	}
	priceType := exchange.TriggerPriceLast
	if resp.WorkingType == "MARK_PRICE" {
		priceType = exchange.TriggerPriceMark
	}
	order := exchange.TriggerOrder{
		NativeSymbol:  canonical.Identity.NativeSymbol,
		OrderID:       strconv.FormatInt(resp.AlgoID, 10),
		ClientOrderID: resp.ClientAlgoID,
		Side:          side,
		Kind:          kind,
		TriggerPrice:  trigger,
		PriceType:     priceType,
		Quantity:      quantity,
		ClosePosition: resp.ClosePosition,
		Status:        binancePerpTriggerStatus(resp.AlgoStatus),
	}
	if resp.CreateTime > 0 {
		order.CreatedAt = time.UnixMilli(resp.CreateTime).UTC()
	}
	if resp.UpdateTime > 0 {
		order.UpdatedAt = time.UnixMilli(resp.UpdateTime).UTC()
	}
	return order, nil
}

func binancePerpTriggerStatus(status string) string {
	switch status {
	case "NEW":
		return "open"
	case "TRIGGERING", "TRIGGERED":
		return "triggered"
	case "FINISHED":
		return "filled"
	case "CANCELED", "CANCELLED":
		return "canceled"
	case "EXPIRED":
		return "expired"
	case "REJECTED":
		return "rejected"
	}
	return strings.ToLower(status)
}

func binancePerpAmbiguous(operation string) error {
	return exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{
		Venue: binancePerpVenue, Product: binancePerpProduct, Operation: operation,
		SafeMessage: "order command outcome is unknown after possible send",
	})
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
