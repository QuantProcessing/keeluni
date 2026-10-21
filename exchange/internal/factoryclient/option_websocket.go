package factoryclient

import (
	"context"
	"errors"
	"sync"

	"github.com/QuantProcessing/keeluni/exchange"
)

// optionQuoteBackend streams one option's quote.
type optionQuoteBackend interface {
	StartOptionQuote(context.Context, exchange.Instrument, streamCallbacks[exchange.OptionQuote]) (func() error, error)
}

// optionWebSocket is the account socket of an options client: the shared
// public and private topics, plus positions and option quotes.
type optionWebSocket struct {
	*spotWebSocket
	positionBackend perpPrivateWSBackend
	quoteBackend    optionQuoteBackend
	positions       map[string]*websocketTopic[exchange.PositionEvent]
	quotes          map[string]*websocketTopic[exchange.OptionQuote]
	closeOnce       sync.Once
	closeErr        error
}

func newOptionWebSocket(meta clientMeta, public publicWSBackend, private perpPrivateWSBackend, quotes optionQuoteBackend) *optionWebSocket {
	return &optionWebSocket{
		spotWebSocket:   newSpotWebSocket(newPublicWebSocket(meta, public), private),
		positionBackend: private,
		quoteBackend:    quotes,
		positions:       make(map[string]*websocketTopic[exchange.PositionEvent]),
		quotes:          make(map[string]*websocketTopic[exchange.OptionQuote]),
	}
}

func (socket *optionWebSocket) WatchPositions(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.PositionEvent], error) {
	if socket == nil || socket.positionBackend == nil {
		return nil, websocketError(socketMeta(nil), "WatchPositions", exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	if err := socket.private.validateWatch(ctx, "WatchPositions", request); err != nil {
		return nil, err
	}
	key := websocketInstrumentKey(request.Instrument)
	return watchPrivateWebSocketTopic(socket.publicWebSocket, ctx, "WatchPositions", "positions", key, key, request.Options.Buffer, socket.positions,
		func(startCtx context.Context, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
			return socket.positionBackend.StartPositions(startCtx, request.Instrument, callbacks)
		})
}

func (socket *optionWebSocket) WatchOptionQuote(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.OptionQuote], error) {
	if socket == nil || socket.quoteBackend == nil {
		return nil, websocketError(socketMeta(nil), "WatchOptionQuote", exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	if err := socket.validateWatch(ctx, "WatchOptionQuote", request); err != nil {
		return nil, err
	}
	var latest exchange.OptionQuote
	var have bool
	var mu sync.Mutex
	process := func(quote exchange.OptionQuote) (bool, error) {
		mu.Lock()
		latest, have = quote, true
		mu.Unlock()
		return true, nil
	}
	replay := func() (exchange.OptionQuote, bool) {
		mu.Lock()
		defer mu.Unlock()
		return latest, have
	}
	return watchWebSocketTopic(socket.publicWebSocket, ctx, "WatchOptionQuote", "option-quote", websocketInstrumentKey(request.Instrument), request,
		socket.quotes, socket.quoteBackend.StartOptionQuote, process, replay)
}

func (socket *optionWebSocket) Close() error {
	if socket == nil {
		return nil
	}
	socket.closeOnce.Do(func() {
		socket.mu.Lock()
		var closes []func() error
		for _, topic := range socket.positions {
			for _, subscription := range topic.snapshot() {
				closes = append(closes, subscription.Close)
			}
		}
		for _, topic := range socket.quotes {
			for _, subscription := range topic.snapshot() {
				closes = append(closes, subscription.Close)
			}
		}
		socket.mu.Unlock()
		var errs []error
		for _, closeSubscription := range closes {
			if err := closeSubscription(); err != nil {
				errs = append(errs, err)
			}
		}
		if err := socket.spotWebSocket.Close(); err != nil {
			errs = append(errs, err)
		}
		socket.closeErr = errors.Join(errs...)
	})
	return socket.closeErr
}

var (
	_ exchange.OptionWebSocket          = (*optionWebSocket)(nil)
	_ exchange.AccountWebSocket         = (*optionWebSocket)(nil)
	_ exchange.AccountInstrumentIndexer = (*optionWebSocket)(nil)
)
