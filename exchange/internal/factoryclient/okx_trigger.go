package factoryclient

import (
	"context"
	"strings"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/sdk/okx"
	"github.com/shopspring/decimal"
)

// OKX swap take-profit / stop-loss: a one-way conditional algo order
// (POST /api/v5/trade/order-algo, ordType conditional) carrying either
// tpTriggerPx or slTriggerPx with order price -1 (market), reduce-only, in
// cross margin (as ordinary orders here). Sizes are in contracts. The trigger
// price type is last or mark. algoClOrdId is the client order id.

var _ exchange.TriggerOrderREST = (*okxPerpClient)(nil)

const okxConditional = "conditional"

func (client *okxPerpClient) TriggerPriceTypes() []exchange.TriggerPriceType {
	return []exchange.TriggerPriceType{exchange.TriggerPriceLast, exchange.TriggerPriceMark}
}

func (client *okxPerpClient) PlaceTriggerOrder(ctx context.Context, req exchange.TriggerOrderRequest) (exchange.TriggerOrder, error) {
	const operation = "PlaceTriggerOrder"
	product := exchange.ProductPerp
	if err := okxReady(ctx, product, operation, client.sdk); err != nil {
		return exchange.TriggerOrder{}, err
	}
	if err := req.Validate(); err != nil {
		return exchange.TriggerOrder{}, okxInvalid(product, operation, safeTriggerMessage(err))
	}
	meta, err := client.okxPerpMeta(ctx, operation, req.Instrument)
	if err != nil {
		return exchange.TriggerOrder{}, err
	}
	contracts := req.Quantity.Div(meta.contractValue)
	if !contracts.Mod(meta.contractIncrement).IsZero() {
		return exchange.TriggerOrder{}, okxInvalid(product, operation, "quantity must align to OKX contract lot size")
	}
	trigger := req.TriggerPrice.String()
	priceType := okxTriggerPxType(req.PriceType)
	market := "-1"
	reduceOnly := true
	request := &okx.AlgoOrderRequest{
		InstId: req.Instrument.Identity.NativeSymbol, TdMode: okxCrossMode, Side: string(req.Side),
		OrdType: okxConditional, Sz: contracts.String(), AlgoClOrdId: okxStringPtr(req.ClientOrderID), ReduceOnly: &reduceOnly,
	}
	if req.Kind == exchange.TriggerTakeProfit {
		request.TpTriggerPx, request.TpOrdPx, request.TpTriggerPxType = &trigger, &market, &priceType
	} else {
		request.SlTriggerPx, request.SlOrdPx, request.SlTriggerPxType = &trigger, &market, &priceType
	}
	rows, err := client.sdk.PlaceAlgoOrder(ctx, request)
	if err != nil {
		_, mapped := okxCommandTransportAck(product, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, err)
		return exchange.TriggerOrder{}, withExchangeOperation(mapped, operation)
	}
	if len(rows) != 1 || rows[0].SCode == "" {
		return exchange.TriggerOrder{}, okxAmbiguous(product, operation)
	}
	if rows[0].SCode != "0" {
		return exchange.TriggerOrder{}, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, Code: rows[0].SCode, SafeMessage: "OKX rejected order command", VenueMessage: rows[0].SMsg})
	}
	if rows[0].AlgoId == "" || (rows[0].AlgoClOrdId != "" && rows[0].AlgoClOrdId != req.ClientOrderID) {
		return exchange.TriggerOrder{}, okxAmbiguous(product, operation)
	}
	return exchange.TriggerOrder{
		NativeSymbol: req.Instrument.Identity.NativeSymbol, OrderID: rows[0].AlgoId, ClientOrderID: req.ClientOrderID,
		Side: req.Side, Kind: req.Kind, TriggerPrice: req.TriggerPrice, PriceType: req.PriceType, Quantity: req.Quantity, Status: "open",
	}, nil
}

func (client *okxPerpClient) CancelTriggerOrder(ctx context.Context, req exchange.CancelOrderRequest) error {
	const operation = "CancelTriggerOrder"
	product := exchange.ProductPerp
	if err := okxReady(ctx, product, operation, client.sdk); err != nil {
		return err
	}
	native, err := okxSwapNativeInstrument(operation, req.Instrument)
	if err != nil {
		return err
	}
	if strings.TrimSpace(req.OrderID) == "" {
		return okxInvalid(product, operation, "order id is required")
	}
	rows, err := client.sdk.CancelAlgoOrders(ctx, []okx.AlgoCancelRequest{{AlgoId: req.OrderID, InstId: native}})
	if err != nil {
		return okxNormalizeErr(product, operation, err)
	}
	if len(rows) != 1 {
		return okxMalformed(product, operation, "cancel response must contain exactly one item")
	}
	if rows[0].SCode != "" && rows[0].SCode != "0" {
		return exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, Code: rows[0].SCode, SafeMessage: "OKX rejected cancel", VenueMessage: rows[0].SMsg})
	}
	return nil
}

func (client *okxPerpClient) OpenTriggerOrders(ctx context.Context, req exchange.OpenOrdersRequest) ([]exchange.TriggerOrder, error) {
	const operation = "OpenTriggerOrders"
	product := exchange.ProductPerp
	if err := okxReady(ctx, product, operation, client.sdk); err != nil {
		return nil, err
	}
	instID := ""
	if req.Instrument != nil {
		native, err := okxSwapNativeInstrument(operation, *req.Instrument)
		if err != nil {
			return nil, err
		}
		instID = native
	}
	// One-way TP/SL placed here are "conditional"; those set on the venue's
	// position panel are "oco" (both legs) or "conditional" as well.
	var out []exchange.TriggerOrder
	for _, ordType := range []string{okxConditional, "oco"} {
		rows, err := client.sdk.GetPendingAlgoOrders(ctx, okxSwapType, instID, ordType, "", "")
		if err != nil {
			return nil, okxNormalizeErr(product, operation, err)
		}
		for _, row := range rows {
			if instID != "" && row.InstId != instID {
				return nil, okxMalformed(product, operation, "mixed instrument row")
			}
			meta, err := client.okxPerpMetaByNative(ctx, operation, row.InstId)
			if err != nil {
				continue
			}
			out = append(out, okxTriggerOrders(row, meta)...)
		}
	}
	if out == nil {
		out = []exchange.TriggerOrder{}
	}
	return out, nil
}

func (client *okxPerpClient) LookupTriggerOrder(ctx context.Context, instrument exchange.Instrument, clientOrderID string) (exchange.TriggerOrder, exchange.TriggerLookupStatus, error) {
	const operation = "LookupTriggerOrder"
	product := exchange.ProductPerp
	if err := okxReady(ctx, product, operation, client.sdk); err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	native, err := okxSwapNativeInstrument(operation, instrument)
	if err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	if clientOrderID == "" {
		return exchange.TriggerOrder{}, "", okxInvalid(product, operation, "client order id is required")
	}
	rows, err := client.sdk.GetAlgoOrder(ctx, "", clientOrderID)
	if okxIsExactOrderNotFound(err) {
		return exchange.TriggerOrder{}, exchange.TriggerNotFound, nil
	}
	if err != nil {
		return exchange.TriggerOrder{}, "", okxNormalizeErr(product, operation, err)
	}
	if len(rows) == 0 {
		return exchange.TriggerOrder{}, exchange.TriggerNotFound, nil
	}
	if rows[0].InstId != native {
		return exchange.TriggerOrder{}, "", okxMalformed(product, operation, "order belongs to another instrument")
	}
	meta, err := client.okxPerpMeta(ctx, operation, instrument)
	if err != nil {
		return exchange.TriggerOrder{}, "", err
	}
	orders := okxTriggerOrders(rows[0], meta)
	if len(orders) == 0 {
		return exchange.TriggerOrder{}, "", okxMalformed(product, operation, "algo order is not a take-profit / stop-loss")
	}
	return orders[0], exchange.TriggerFound, nil
}

// okxTriggerOrders projects one algo order: a conditional order is one
// trigger; an oco order is two (take-profit and stop-loss) sharing an id.
func okxTriggerOrders(row okx.AlgoOrder, meta okxContractMeta) []exchange.TriggerOrder {
	side, err := okxExchangeSide(string(row.Side))
	if err != nil || row.AlgoId == "" {
		return nil
	}
	contracts, _ := okxNonNegativeDecimal(row.Sz)
	quantity := contracts.Mul(meta.contractValue)
	closePosition := row.CloseFraction == "1" || contracts.IsZero()
	base := exchange.TriggerOrder{
		NativeSymbol: row.InstId, OrderID: row.AlgoId, ClientOrderID: row.AlgoClOrdId, Side: side,
		Quantity: quantity, ClosePosition: closePosition, Status: okxTriggerStatus(row.State),
	}
	if closePosition {
		base.Quantity = decimal.Zero
	}
	if created, err := okxMillis(row.CTime); err == nil {
		base.CreatedAt = created
	}
	if updated, err := okxMillis(row.UTime); err == nil {
		base.UpdatedAt = updated
	}
	var out []exchange.TriggerOrder
	add := func(kind exchange.TriggerKind, rawPrice, rawOrderPx, rawType string) {
		price, err := okxPositiveDecimal(rawPrice)
		if err != nil {
			return
		}
		// A limit leg (order price set) is not a market TP/SL.
		if rawOrderPx != "" && rawOrderPx != "-1" {
			return
		}
		order := base
		order.Kind, order.TriggerPrice = kind, price
		order.PriceType = exchange.TriggerPriceLast
		if rawType == "mark" {
			order.PriceType = exchange.TriggerPriceMark
		}
		if rawType == "index" {
			return
		}
		out = append(out, order)
	}
	add(exchange.TriggerTakeProfit, row.TpTriggerPx, row.TpOrdPx, row.TpTriggerPxType)
	add(exchange.TriggerStopLoss, row.SlTriggerPx, row.SlOrdPx, row.SlTriggerPxType)
	return out
}

func okxTriggerPxType(priceType exchange.TriggerPriceType) string {
	if priceType == exchange.TriggerPriceMark {
		return "mark"
	}
	return "last"
}

func okxTriggerStatus(state string) string {
	switch state {
	case "live", "pause", "partially_effective":
		return "open"
	case "effective":
		return "triggered"
	case "canceled":
		return "canceled"
	case "order_failed":
		return "rejected"
	}
	return state
}

func okxAmbiguous(product exchange.Product, operation string) error {
	return exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: okxVenue, Product: product, Operation: operation, SafeMessage: "order command outcome is unknown after possible send"})
}

