package hedger

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gwclient"
)

// Service wires the gateway stream to the engine and the market cache, and
// keeps subscriptions for every leg of every active group.
type Service struct {
	Engine *Engine
	Market *Market
	store  *Store
	client *gwclient.Client
	stream *gwclient.Stream

	mu        sync.Mutex
	accounts  map[string]bool
	listeners map[chan string]struct{}
}

func NewService(store *Store, client *gwclient.Client) *Service {
	stream := client.Stream()
	market := NewMarket(stream)
	engine := NewEngine(client, store, market)
	return &Service{
		Engine: engine, Market: market, store: store, client: client, stream: stream,
		accounts: map[string]bool{}, listeners: map[chan string]struct{}{},
	}
}

// Run starts everything and blocks until ctx ends.
func (service *Service) Run(ctx context.Context) error {
	go service.stream.Run(ctx)
	go service.consume(ctx)
	if err := service.Engine.Start(ctx); err != nil {
		return err
	}
	defer service.Engine.Stop()
	service.pin(ctx)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	// Funding is accrued from settled rates a minute after start, then every
	// five minutes (settled points reach the venues' history within minutes).
	funding := time.NewTimer(time.Minute)
	defer funding.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case groupID := <-service.Engine.Changes():
			service.broadcast(groupID)
		case <-ticker.C:
			service.pin(ctx)
			service.Market.Sweep()
			service.Engine.ReconcileVenue(ctx)
		case <-funding.C:
			go service.Engine.accrueFunding(ctx, service.client)
			funding.Reset(5 * time.Minute)
		}
	}
}

// pin subscribes the books, funding and accounts of every active group.
func (service *Service) pin(ctx context.Context) {
	groups, err := service.store.Groups(ctx, time.Time{})
	if err != nil {
		return
	}
	var topics []gwclient.Topic
	accounts := map[string]bool{}
	for _, group := range groups {
		if group.Status == StatusClosed || group.Status == StatusFailed {
			continue
		}
		for _, leg := range group.Legs {
			topics = append(topics, gwclient.Topic{Channel: "book", Instrument: leg.Instrument})
			if leg.Product == "perp" {
				topics = append(topics, gwclient.Topic{Channel: "funding", Instrument: leg.Instrument})
			}
			accounts[leg.AccountID] = true
		}
	}
	// Accounts are subscribed for the order/fill events of working actions
	// and the venue positions shown beside each leg.
	service.mu.Lock()
	for account := range service.accounts {
		accounts[account] = true // keep accounts once used: fills may arrive late
	}
	service.accounts = accounts
	service.mu.Unlock()
	for account := range accounts {
		topics = append(topics, gwclient.Topic{Channel: "account", Account: account})
	}
	service.Market.Pin(topics)
}

// UseAccount subscribes an account's stream before an action trades on it.
func (service *Service) UseAccount(accountID string) {
	service.mu.Lock()
	known := service.accounts[accountID]
	service.accounts[accountID] = true
	service.mu.Unlock()
	if !known {
		service.stream.Subscribe(gwclient.Topic{Channel: "account", Account: accountID})
	}
}

func (service *Service) consume(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case connected := <-service.stream.Connected():
			service.Market.SetConnected(connected)
		case message := <-service.stream.Messages():
			switch message.Channel {
			case "account.orders":
				var order gwclient.Order
				if json.Unmarshal(message.Data, &order) == nil {
					service.Engine.OnOrder(message.Account, order)
				}
			case "account.fills":
				var fill gwclient.Fill
				if json.Unmarshal(message.Data, &fill) == nil {
					service.Engine.OnFill(message.Account, fill)
				}
			default:
				if service.Market.Handle(message) && message.Channel == "account.positions" {
					service.broadcast("")
				}
			}
		}
	}
}

// Listen returns a channel of changed group ids ("" means any) for the UI
// stream, and a function to stop listening.
func (service *Service) Listen() (<-chan string, func()) {
	channel := make(chan string, 64)
	service.mu.Lock()
	service.listeners[channel] = struct{}{}
	service.mu.Unlock()
	return channel, func() {
		service.mu.Lock()
		delete(service.listeners, channel)
		service.mu.Unlock()
	}
}

func (service *Service) broadcast(groupID string) {
	service.mu.Lock()
	defer service.mu.Unlock()
	for listener := range service.listeners {
		select {
		case listener <- groupID:
		default:
		}
	}
}

// Ready reports whether the hedger can trade: database reachable and the
// gateway stream connected.
func (service *Service) Ready(ctx context.Context) error {
	if err := service.store.Ping(ctx); err != nil {
		return err
	}
	if !service.Market.Connected() {
		return errors.New("gateway stream is not connected")
	}
	return nil
}
