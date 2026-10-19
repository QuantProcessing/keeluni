package hedger

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gwclient"
	"github.com/shopspring/decimal"
)

// Market keeps the gateway's live data the hedger needs: order books and
// funding of every leg instrument in use, and venue positions of every
// account in use. It holds one gateway WebSocket; topics are subscribed on
// demand and released two minutes after their last use unless pinned by an
// active group.
type Market struct {
	stream *gwclient.Stream
	now    func() time.Time

	mu        sync.Mutex
	books     map[string]bookState
	funding   map[string]gwclient.FundingQuote
	positions map[string][]gwclient.Position // by account
	accountUp map[string]bool
	used      map[gwclient.Topic]time.Time
	pinned    map[gwclient.Topic]bool
	waiters   map[string][]chan struct{}
	connected bool
}

type bookState struct {
	bids, asks BookSide
	at         time.Time
}

const marketLinger = 2 * time.Minute

func NewMarket(stream *gwclient.Stream) *Market {
	return &Market{
		stream: stream, now: time.Now,
		books: map[string]bookState{}, funding: map[string]gwclient.FundingQuote{}, positions: map[string][]gwclient.Position{},
		accountUp: map[string]bool{}, used: map[gwclient.Topic]time.Time{}, pinned: map[gwclient.Topic]bool{}, waiters: map[string][]chan struct{}{},
	}
}

// Handle consumes one stream message; it returns true for messages it owns.
func (market *Market) Handle(message gwclient.Message) bool {
	switch message.Channel {
	case "book":
		var book struct {
			Bids [][2]string `json:"bids"`
			Asks [][2]string `json:"asks"`
		}
		if json.Unmarshal(message.Data, &book) != nil {
			return true
		}
		state := bookState{bids: parseLevels(book.Bids), asks: parseLevels(book.Asks), at: market.now()}
		market.mu.Lock()
		market.books[message.Instrument] = state
		waiters := market.waiters[message.Instrument]
		delete(market.waiters, message.Instrument)
		market.mu.Unlock()
		for _, waiter := range waiters {
			close(waiter)
		}
		return true
	case "funding":
		var quote gwclient.FundingQuote
		if json.Unmarshal(message.Data, &quote) == nil && quote.Instrument != "" {
			market.mu.Lock()
			if quote.Rate == "" { // {"inst":..., "gone":true}
				delete(market.funding, quote.Instrument)
			} else {
				market.funding[quote.Instrument] = quote
			}
			market.mu.Unlock()
		}
		return true
	case "account.positions":
		var positions []gwclient.Position
		if json.Unmarshal(message.Data, &positions) == nil {
			market.mu.Lock()
			market.positions[message.Account] = positions
			market.mu.Unlock()
		}
		return true
	case "account.status":
		var status struct {
			Connected bool `json:"connected"`
		}
		if json.Unmarshal(message.Data, &status) == nil {
			market.mu.Lock()
			market.accountUp[message.Account] = status.Connected
			market.mu.Unlock()
		}
		return true
	}
	return false
}

func parseLevels(levels [][2]string) BookSide {
	result := make(BookSide, 0, len(levels))
	for _, level := range levels {
		price, err1 := decimal.NewFromString(level[0])
		qty, err2 := decimal.NewFromString(level[1])
		if err1 == nil && err2 == nil && price.IsPositive() && qty.IsPositive() {
			result = append(result, [2]decimal.Decimal{price, qty})
		}
	}
	return result
}

// SetConnected records the stream state; a disconnect invalidates books.
func (market *Market) SetConnected(connected bool) {
	market.mu.Lock()
	defer market.mu.Unlock()
	market.connected = connected
	if !connected {
		market.books = map[string]bookState{}
	}
}

func (market *Market) use(topic gwclient.Topic) {
	market.mu.Lock()
	_, existed := market.used[topic]
	market.used[topic] = market.now()
	market.mu.Unlock()
	if !existed && market.stream != nil {
		market.stream.Subscribe(topic)
	}
}

// Pin keeps exactly these topics subscribed indefinitely (active groups).
func (market *Market) Pin(topics []gwclient.Topic) {
	next := make(map[gwclient.Topic]bool, len(topics))
	for _, topic := range topics {
		next[topic] = true
		market.use(topic)
	}
	market.mu.Lock()
	market.pinned = next
	market.mu.Unlock()
}

// Sweep releases topics unused for marketLinger.
func (market *Market) Sweep() {
	market.mu.Lock()
	var release []gwclient.Topic
	for topic, at := range market.used {
		if !market.pinned[topic] && market.now().Sub(at) > marketLinger {
			release = append(release, topic)
			delete(market.used, topic)
			if topic.Channel == "book" {
				delete(market.books, topic.Instrument)
			}
		}
	}
	market.mu.Unlock()
	for _, topic := range release {
		if market.stream != nil {
			market.stream.Unsubscribe(topic)
		}
	}
}

// Book returns an instrument's book, subscribing and waiting up to wait for
// the first snapshot when it is not cached.
func (market *Market) Book(ctx context.Context, instrument string, wait time.Duration) (BookSide, BookSide, bool) {
	market.use(gwclient.Topic{Channel: "book", Instrument: instrument})
	market.mu.Lock()
	state, ok := market.books[instrument]
	var waiter chan struct{}
	if !ok {
		waiter = make(chan struct{})
		market.waiters[instrument] = append(market.waiters[instrument], waiter)
	}
	market.mu.Unlock()
	if !ok {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-waiter:
		case <-timer.C:
		case <-ctx.Done():
		}
		market.mu.Lock()
		state, ok = market.books[instrument]
		market.mu.Unlock()
	}
	return state.bids, state.asks, ok
}

// Top is the cached best bid and ask (zero when unknown). It never waits.
func (market *Market) Top(instrument string) (decimal.Decimal, decimal.Decimal) {
	market.mu.Lock()
	defer market.mu.Unlock()
	state := market.books[instrument]
	var bid, ask decimal.Decimal
	if len(state.bids) > 0 {
		bid = state.bids[0][0]
	}
	if len(state.asks) > 0 {
		ask = state.asks[0][0]
	}
	return bid, ask
}

// TradePrice is the price a market order of side would start at.
func (market *Market) TradePrice(instrument, side string) decimal.Decimal {
	bid, ask := market.Top(instrument)
	if side == "buy" {
		return ask
	}
	return bid
}

// Funding returns an instrument's cached funding quote, subscribing to it.
func (market *Market) Funding(instrument string) (gwclient.FundingQuote, bool) {
	market.use(gwclient.Topic{Channel: "funding", Instrument: instrument})
	market.mu.Lock()
	defer market.mu.Unlock()
	quote, ok := market.funding[instrument]
	return quote, ok
}

// Position is the account's venue position in an instrument, if any.
func (market *Market) Position(accountID, instrument string) (gwclient.Position, bool, bool) {
	market.mu.Lock()
	defer market.mu.Unlock()
	positions, known := market.positions[accountID]
	for _, position := range positions {
		if position.Instrument == instrument {
			return position, true, true
		}
	}
	return gwclient.Position{}, false, known
}

// AccountLive reports whether an account's private stream is connected.
func (market *Market) AccountLive(accountID string) bool {
	market.mu.Lock()
	defer market.mu.Unlock()
	return market.accountUp[accountID]
}

// Topics lists current subscriptions (for diagnostics).
func (market *Market) Topics() []gwclient.Topic {
	market.mu.Lock()
	defer market.mu.Unlock()
	topics := make([]gwclient.Topic, 0, len(market.used))
	for topic := range market.used {
		topics = append(topics, topic)
	}
	sort.Slice(topics, func(i, j int) bool {
		return topics[i].Channel+topics[i].Instrument+topics[i].Account < topics[j].Channel+topics[j].Instrument+topics[j].Account
	})
	return topics
}
