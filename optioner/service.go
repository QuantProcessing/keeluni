package optioner

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/shopspring/decimal"
)

// Service keeps the state current from the gateway and runs executions.
type Service struct {
	gateway    Gateway
	stream     *gwclient.Stream
	state      *state
	Executions *Executions

	mu        sync.Mutex
	listeners map[chan struct{}]struct{}
}

func NewService(client *gwclient.Client) *Service {
	return newService(client, client.Stream())
}

func newService(gateway Gateway, stream *gwclient.Stream) *Service {
	state := newState()
	return &Service{gateway: gateway, stream: stream, state: state, Executions: newExecutions(gateway, state), listeners: map[chan struct{}]struct{}{}}
}

// Run loads the catalog and subscribes every option series and every
// account, then follows the stream until ctx ends.
func (service *Service) Run(ctx context.Context) error {
	if service.stream != nil {
		go service.stream.Run(ctx)
		go service.consume(ctx)
	}
	// Until the gateway answers (it may start after this app), retry every
	// few seconds; then refresh every minute.
	loaded := service.refresh(ctx)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !loaded || time.Since(last) >= time.Minute {
				loaded, last = service.refresh(ctx), time.Now()
			}
		case <-service.Executions.changes:
			service.broadcast()
		}
	}
}

// refresh reloads instruments, accounts and the series list, subscribing
// new ones. Positions arrive on account topics; the first read is REST so
// risk is right before the stream's snapshot.
func (service *Service) refresh(ctx context.Context) bool {
	if items, err := service.gateway.Instruments(ctx); err == nil {
		service.state.setInstruments(items)
	} else {
		slog.Warn("instruments unavailable", "error", err)
		return false
	}
	if items, err := service.gateway.InstrumentsOf(ctx, "option"); err == nil {
		service.state.setInstruments(items)
	}
	accounts, err := service.gateway.Accounts(ctx)
	if err == nil {
		service.state.setAccounts(accounts)
		for _, account := range accounts {
			if !account.Active {
				continue
			}
			if positions, err := service.gateway.Positions(ctx, account.ID); err == nil {
				service.state.setPositions(account.ID, positions)
			}
			if service.stream != nil {
				service.stream.Subscribe(gwclient.Topic{Channel: "account", Account: account.ID})
			}
		}
	}
	series, err := service.gateway.OptionSeries(ctx)
	if err != nil {
		return false
	}
	for _, item := range series {
		service.state.mu.RLock()
		_, loaded := service.state.chains[item.Series]
		service.state.mu.RUnlock()
		if !loaded {
			if chain, err := service.gateway.OptionChain(ctx, item.Series); err == nil {
				service.state.setChain(chain)
			}
		}
		if service.stream != nil {
			service.stream.Subscribe(gwclient.Topic{Channel: "options", Instrument: item.Series})
		}
	}
	service.broadcast()
	// The gateway lists series once its first chain read lands.
	return len(series) > 0
}

func (service *Service) consume(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case connected := <-service.stream.Connected():
			service.state.mu.Lock()
			service.state.connected = connected
			service.state.mu.Unlock()
		case message := <-service.stream.Messages():
			service.state.apply(message)
			if message.Channel == "account.orders" {
				var order gwclient.Order
				if json.Unmarshal(message.Data, &order) == nil {
					service.Executions.OnOrder(order)
				}
			}
			if message.Channel == "account.positions" {
				service.broadcast()
			}
		}
	}
}

// Listen returns a channel signalled whenever risk or executions change.
func (service *Service) Listen() (<-chan struct{}, func()) {
	channel := make(chan struct{}, 1)
	service.mu.Lock()
	service.listeners[channel] = struct{}{}
	service.mu.Unlock()
	return channel, func() {
		service.mu.Lock()
		delete(service.listeners, channel)
		service.mu.Unlock()
	}
}

func (service *Service) broadcast() {
	service.mu.Lock()
	defer service.mu.Unlock()
	for channel := range service.listeners {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}

func (service *Service) Risk(options RiskOptions) Risk {
	return service.state.risk(options, time.Now())
}

// ---------------------------------------------------------------------------
// Delta hedge
// ---------------------------------------------------------------------------

// HedgeSuggestion is the perp order that flattens an underlying's delta on
// one account, sized to the instrument's step.
type HedgeSuggestion struct {
	Underlying string     `json:"underlying"`
	Delta      float64    `json:"delta"`
	Leg        LegRequest `json:"leg"`
	// Residual is the delta left after the hedge (step rounding).
	Residual float64 `json:"residual"`
}

// SuggestHedge picks the account's linear perp on the underlying (USDC or
// USDT margined, most liquid first) and the order that brings delta to zero.
func (service *Service) SuggestHedge(underlying, accountID string, options RiskOptions) (HedgeSuggestion, error) {
	underlying = strings.ToUpper(strings.TrimSpace(underlying))
	risk := service.Risk(options)
	var delta float64
	found := false
	for _, item := range risk.Underlyings {
		if item.Underlying == underlying {
			delta, found = item.Greeks.Delta, true
		}
	}
	if !found {
		return HedgeSuggestion{}, invalid("no %s options are held", underlying)
	}
	account, ok := service.state.account(accountID)
	if !ok {
		return HedgeSuggestion{}, invalid("unknown account")
	}
	markets := map[string]bool{}
	for _, market := range account.Markets {
		markets[market] = true
	}
	service.state.mu.RLock()
	var candidates []gwclient.Instrument
	for _, instrument := range service.state.instruments {
		if instrument.Product != "perp" || normalizeBase(instrument.Base) != underlying || !markets[marketOf(instrument)] || strings.Contains(instrument.Symbol, ":") {
			continue
		}
		candidates = append(candidates, instrument)
	}
	service.state.mu.RUnlock()
	if len(candidates) == 0 {
		return HedgeSuggestion{}, invalid("account trades no %s perpetual", underlying)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return settleRank(candidates[i]) < settleRank(candidates[j]) || (settleRank(candidates[i]) == settleRank(candidates[j]) && candidates[i].ID < candidates[j].ID)
	})
	perp := candidates[0]
	step := decimal.RequireFromString(nonEmpty(perp.StepSize, "1"))
	size := decimal.NewFromFloat(math.Abs(delta)).Div(step).Round(0).Mul(step)
	if !size.IsPositive() {
		return HedgeSuggestion{}, invalid("delta %.6f is below the hedge's step %s", delta, step)
	}
	side := "sell"
	if delta < 0 {
		side = "buy"
	}
	signed := size.InexactFloat64()
	if side == "sell" {
		signed = -signed
	}
	return HedgeSuggestion{Underlying: underlying, Delta: round(delta, 6), Residual: round(delta+signed, 6),
		Leg: LegRequest{Account: accountID, Instrument: perp.ID, Side: side, Quantity: size.String(), Type: "market"}}, nil
}

func settleRank(instrument gwclient.Instrument) int {
	switch strings.ToUpper(nonEmpty(instrument.Settle, instrument.Quote)) {
	case "USDC":
		return 0
	case "USDT":
		return 1
	}
	return 2
}

func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
