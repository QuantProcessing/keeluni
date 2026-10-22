package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/market"
	"github.com/QuantProcessing/keeluni/gateway/venue"
	"github.com/shopspring/decimal"
)

// Take-profit and stop-loss: reduce-only market orders resting on the venue
// until their trigger price is crossed. Only venues whose client implements
// exchange.TriggerOrderREST offer them; on the others a position closes by
// a limit (or market) order only.

// TriggerRequest places one take-profit or stop-loss. Side is the closing
// side (sell for a long). PriceType is "last" (default) or "mark".
type TriggerRequest struct {
	Instrument    venue.InstrumentID `json:"inst"`
	Side          string             `json:"side"`
	Kind          string             `json:"kind"` // take_profit | stop_loss
	TriggerPrice  string             `json:"triggerPrice"`
	PriceType     string             `json:"priceType,omitempty"`
	Quantity      string             `json:"qty"`
	ClientOrderID string             `json:"clientOrderId"`
}

// Trigger is a resting take-profit or stop-loss.
type Trigger struct {
	Instrument    venue.InstrumentID `json:"inst"`
	OrderID       string             `json:"orderId"`
	ClientOrderID string             `json:"clientOrderId,omitempty"`
	Side          string             `json:"side"`
	Kind          string             `json:"kind"`
	TriggerPrice  string             `json:"triggerPrice"`
	PriceType     string             `json:"priceType"`
	Quantity      string             `json:"qty,omitempty"`
	// ClosePosition: closes the whole position whatever its size (placed on
	// the venue's own interface).
	ClosePosition bool   `json:"closePosition,omitempty"`
	Status        string `json:"status"`
	CreatedAt     int64  `json:"createdAt,omitempty"`
}

// TriggerResult is a placement's outcome, with the order rules of Place.
type TriggerResult struct {
	Status       string   `json:"status"`
	Trigger      *Trigger `json:"trigger,omitempty"`
	Category     Category `json:"category,omitempty"`
	VenueCode    string   `json:"venueCode,omitempty"`
	VenueMessage string   `json:"venueMessage,omitempty"`
	Message      string   `json:"message,omitempty"`
}

// TriggerLookup answers "does this trigger order exist?".
type TriggerLookup struct {
	Status  string   `json:"status"` // found | not_found
	Trigger *Trigger `json:"trigger,omitempty"`
}

// TriggerSupport is what a market's trigger orders accept: the trigger
// prices the venue compares with. Empty when the market has none.
type TriggerSupport struct {
	PriceTypes []string `json:"priceTypes,omitempty"`
}

func (client routeClient) triggers() (exchange.TriggerOrderREST, bool) {
	trigger, ok := client.orders().(exchange.TriggerOrderREST)
	return trigger, ok
}

func triggerSupport(client routeClient) *TriggerSupport {
	trigger, ok := client.triggers()
	if !ok {
		return nil
	}
	support := &TriggerSupport{}
	for _, priceType := range trigger.TriggerPriceTypes() {
		support.PriceTypes = append(support.PriceTypes, string(priceType))
	}
	if len(support.PriceTypes) == 0 {
		return nil
	}
	return support
}

func (request TriggerRequest) native(instrument market.Instrument, support *TriggerSupport) (exchange.TriggerOrderRequest, error) {
	invalid := func(format string, args ...any) (exchange.TriggerOrderRequest, error) {
		return exchange.TriggerOrderRequest{}, fmt.Errorf("%w: "+format, append([]any{ErrInvalidRequest}, args...)...)
	}
	id, err := strconv.ParseUint(request.ClientOrderID, 10, 64)
	if err != nil || id == 0 || id > maxClientOrderID || strings.HasPrefix(request.ClientOrderID, "0") {
		return invalid("clientOrderId must be a decimal integer in [1, 2^48)")
	}
	quantity, err := decimal.NewFromString(request.Quantity)
	if err != nil || !quantity.IsPositive() {
		return invalid("qty must be a positive decimal")
	}
	trigger, err := decimal.NewFromString(request.TriggerPrice)
	if err != nil || !trigger.IsPositive() {
		return invalid("triggerPrice must be a positive decimal")
	}
	native := exchange.TriggerOrderRequest{Instrument: instrument.Native(), ClientOrderID: request.ClientOrderID, TriggerPrice: trigger, Quantity: quantity}
	switch request.Side {
	case "buy":
		native.Side = exchange.SideBuy
	case "sell":
		native.Side = exchange.SideSell
	default:
		return invalid("side must be buy or sell")
	}
	switch request.Kind {
	case "take_profit":
		native.Kind = exchange.TriggerTakeProfit
	case "stop_loss":
		native.Kind = exchange.TriggerStopLoss
	default:
		return invalid("kind must be take_profit or stop_loss")
	}
	priceType := request.PriceType
	if priceType == "" {
		priceType = support.PriceTypes[0]
	}
	allowed := false
	for _, item := range support.PriceTypes {
		allowed = allowed || item == priceType
	}
	if !allowed {
		return invalid("priceType must be one of %s on this market", strings.Join(support.PriceTypes, ", "))
	}
	native.PriceType = exchange.TriggerPriceType(priceType)
	if err := native.Validate(); err != nil {
		return invalid("%s", safeMessage(err))
	}
	return native, nil
}

func (rt *runtime) triggerClient(instrument market.Instrument) (routeClient, exchange.TriggerOrderREST, error) {
	client, err := rt.clientFor(instrument)
	if err != nil {
		return routeClient{}, nil, err
	}
	trigger, ok := client.triggers()
	if !ok {
		return routeClient{}, nil, fmt.Errorf("%w: take-profit / stop-loss orders on %s", ErrUnsupported, instrument.ID)
	}
	return client, trigger, nil
}

// PlaceTrigger sends one take-profit or stop-loss. Like Place it never
// retries: an "unknown" outcome must be looked up by clientOrderId.
func (rt *runtime) PlaceTrigger(ctx context.Context, instrument market.Instrument, request TriggerRequest) (TriggerResult, error) {
	client, trigger, err := rt.triggerClient(instrument)
	if err != nil {
		return TriggerResult{}, err
	}
	if client.broker {
		return TriggerResult{}, fmt.Errorf("%w: take-profit / stop-loss through a broker", ErrUnsupported)
	}
	native, err := request.native(instrument, triggerSupport(client))
	if err != nil {
		return TriggerResult{}, err
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	started := time.Now()
	order, placeErr := trigger.PlaceTriggerOrder(callCtx, native)
	result := classifyTrigger(instrument.ID, order, placeErr)
	slog.Info("trigger placed", "account", rt.id, "inst", instrument.ID, "side", request.Side, "kind", request.Kind,
		"trigger", request.TriggerPrice, "qty", request.Quantity, "clientOrderId", request.ClientOrderID, "status", result.Status,
		"venueCode", result.VenueCode, "elapsed", time.Since(started).Round(time.Millisecond), "error", placeErr)
	return result, nil
}

// classifyTrigger maps a placement onto accepted / rejected / unknown with
// the order rules: only proof of non-placement may yield "rejected".
func classifyTrigger(id venue.InstrumentID, order exchange.TriggerOrder, err error) TriggerResult {
	if err == nil {
		projected := projectTrigger(id, order)
		return TriggerResult{Status: OutcomeAccepted, Trigger: &projected}
	}
	var normalized *exchange.Error
	isNormalized := errors.As(err, &normalized)
	failure := Classify(err)
	rejected := TriggerResult{Status: OutcomeRejected, Category: failure.Category, VenueCode: failure.Code, VenueMessage: failure.VenueMessage, Message: failure.Message()}
	switch {
	case isNormalized && normalized.Details().CommandNotSent:
		return rejected
	case errors.Is(err, exchange.ErrVenueRejected), errors.Is(err, exchange.ErrAuthentication), errors.Is(err, exchange.ErrPermission):
		return rejected
	case isNormalized && normalized.Details().HTTPStatus == 0 &&
		(errors.Is(err, exchange.ErrInvalidRequest) || errors.Is(err, exchange.ErrUnsupported) || errors.Is(err, exchange.ErrInvalidConfig)):
		return rejected
	case errors.Is(err, exchange.ErrRateLimit) && isNormalized && normalized.Details().HTTPStatus == 0:
		return rejected
	}
	return TriggerResult{Status: OutcomeUnknown, Category: failure.Category, Message: "Order outcome unknown — checking with the exchange before anything else."}
}

func (rt *runtime) CancelTrigger(ctx context.Context, instrument market.Instrument, orderID string) error {
	_, trigger, err := rt.triggerClient(instrument)
	if err != nil {
		return err
	}
	if strings.TrimSpace(orderID) == "" {
		return fmt.Errorf("%w: orderId is required", ErrInvalidRequest)
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	err = trigger.CancelTriggerOrder(callCtx, exchange.CancelOrderRequest{Instrument: instrument.Native(), OrderID: orderID})
	slog.Info("trigger cancel", "account", rt.id, "inst", instrument.ID, "orderId", orderID, "error", err)
	return err
}

func (rt *runtime) LookupTrigger(ctx context.Context, instrument market.Instrument, clientOrderID string) (TriggerLookup, error) {
	_, trigger, err := rt.triggerClient(instrument)
	if err != nil {
		return TriggerLookup{}, err
	}
	if clientOrderID == "" {
		return TriggerLookup{}, fmt.Errorf("%w: clientOrderId is required", ErrInvalidRequest)
	}
	order, status, err := trigger.LookupTriggerOrder(ctx, instrument.Native(), clientOrderID)
	if err != nil {
		return TriggerLookup{}, err
	}
	if status != exchange.TriggerFound {
		return TriggerLookup{Status: "not_found"}, nil
	}
	projected := projectTrigger(instrument.ID, order)
	return TriggerLookup{Status: "found", Trigger: &projected}, nil
}

// OpenTriggers lists resting take-profit / stop-loss orders, for one
// instrument or every market of the account that has them.
func (rt *runtime) OpenTriggers(ctx context.Context, instrument *market.Instrument) ([]Trigger, error) {
	var clients []routeClient
	var native *exchange.Instrument
	if instrument != nil {
		client, _, err := rt.triggerClient(*instrument)
		if err != nil {
			return nil, err
		}
		clients = []routeClient{client}
		value := instrument.Native()
		native = &value
	} else {
		for _, client := range rt.clients {
			if _, ok := client.triggers(); ok && !client.broker {
				clients = append(clients, client)
			}
		}
	}
	out := []Trigger{}
	var errs []error
	for _, client := range clients {
		trigger, _ := client.triggers()
		orders, err := trigger.OpenTriggerOrders(ctx, exchange.OpenOrdersRequest{Instrument: native})
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", client.key, err))
			continue
		}
		for _, item := range orders {
			id := rt.instrumentID(client, item.NativeSymbol, item.NativeID, instrument)
			if id == "" {
				continue
			}
			out = append(out, projectTrigger(id, item))
		}
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

func projectTrigger(id venue.InstrumentID, order exchange.TriggerOrder) Trigger {
	result := Trigger{
		Instrument: id, OrderID: order.OrderID, ClientOrderID: order.ClientOrderID,
		Side: string(order.Side), Kind: string(order.Kind), TriggerPrice: order.TriggerPrice.String(),
		PriceType: string(order.PriceType), ClosePosition: order.ClosePosition, Status: order.Status,
	}
	if order.Quantity.IsPositive() {
		result.Quantity = order.Quantity.String()
	}
	if !order.CreatedAt.IsZero() {
		result.CreatedAt = order.CreatedAt.UnixMilli()
	}
	return result
}
