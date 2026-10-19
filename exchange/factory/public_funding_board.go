package factory

import (
	"context"
	"net/http"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/internal/factoryclient"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

// PublicFundingBoard keeps one served perpetual market's whole funding board,
// credential-free. It is built on the market's derivative feed (the same
// loaders and native streams the derivative feed owner uses), so every venue
// reports one convention: DerivativeFact.FundingRate is the fraction of
// notional paid at NextFundingTime (positive: longs pay shorts), and
// FundingIntervalHours is that instrument's own settlement period.
//
// Facts come out keyed by native identity; callers map them onto their own
// instrument ids. A board refreshes by polling the venue's whole-market read
// or, where the venue pushes every instrument at once, by its stream.
type PublicFundingBoard struct {
	scope  exchange.CatalogScope
	feed   exchange.DerivativeFeed
	poll   time.Duration
	stream bool
}

// nativeIdentityResolver projects native rows without a catalog: the board
// is keyed by native identity and the gateway maps it onto its instruments.
type nativeIdentityResolver struct {
	scope exchange.CatalogScope
}

func (resolver nativeIdentityResolver) ResolveDerivativeInstrument(_ context.Context, routeProfileID, nativeSymbol, nativeID string) (exchange.DerivativeFact, bool) {
	return exchange.DerivativeFact{
		InstrumentID:   nativeID + "|" + nativeSymbol,
		RouteProfileID: routeProfileID,
		Environment:    resolver.scope.Environment,
		Instrument: exchange.InstrumentIdentity{Venue: resolver.scope.Venue, Product: exchange.ProductPerp,
			SettleAsset: resolver.scope.Settlement, NativeSymbol: nativeSymbol, NativeID: nativeID},
	}, true
}

// fundingBoardStreams lists the venues whose native derivative stream pushes
// every instrument's funding (Hyperliquid's allDexsAssetCtxs covers every
// dex). Elsewhere a whole-market REST read is polled: per-instrument streams
// would cost a subscription per perpetual.
var fundingBoardStreams = map[exchange.Venue]bool{exchange.VenueHyperliquid: true}

// fundingBoardPoll is the REST cadence per venue; Gate's reply is large.
func fundingBoardPoll(venue exchange.Venue) time.Duration {
	if venue == exchange.VenueGate {
		return 30 * time.Second
	}
	return 15 * time.Second
}

// NewPublicFundingBoard builds the funding board of a served perpetual market.
// Construction performs no I/O.
func NewPublicFundingBoard(venue exchange.Venue, settlement string, environment Environment, httpClient *http.Client) (*PublicFundingBoard, error) {
	return newPublicFundingBoard(venue, settlement, httpClient, WithEnvironment(environment))
}

func newPublicFundingBoard(venue exchange.Venue, settlement string, httpClient *http.Client, options ...Option) (*PublicFundingBoard, error) {
	configName, ok := publicConnectorConfigName(venue, exchange.ProductPerp, settlement)
	if !ok || httpClient == nil {
		return nil, invalidConfig("public funding board profile is unsupported")
	}
	settings := newSettings(append([]Option{WithEnvironment(EnvironmentLive), WithHTTPClient(httpClient)}, options...))
	if err := settings.validate(); err != nil {
		return nil, err
	}
	scope, _, ok := catalogSourceForConfig(configName, settings)
	if !ok {
		return nil, invalidConfig("public funding board source is unavailable")
	}
	loader := derivativeSnapshotLoaderForConfig(configName, settings)
	if venue == exchange.VenueHyperliquid {
		// The stream pushes every dex's rates; the bootstrap only names them.
		loader = factoryclient.NewHyperliquidFundingDirectoryLoader(factorySettingsForConfig(configName, settings))
	}
	if loader == nil {
		return nil, invalidConfig("public funding board loader is unavailable")
	}
	profile, err := routeprofile.DefaultRegistry().ResolveLegacy(string(venue), string(exchange.ProductPerp), settlement)
	if err != nil {
		return nil, invalidConfig("public funding board route is unknown")
	}
	family := exchange.NativeMarketFamily(venue, exchange.ProductPerp, settlement)
	stream := fundingBoardStreams[venue]
	poll := fundingBoardPoll(venue)
	// The capability row selects the feed's transport: a whole-market stream
	// where the venue pushes one, otherwise polling at the board's cadence.
	capability := exchange.MarketCapability{
		Venue: venue, NativeMarketFamily: family, Dataset: exchange.DatasetFunding,
		Transport: exchange.TransportREST, Granularity: exchange.GranularityAllMarket, NominalCadence: poll,
	}
	if stream {
		capability.Transport, capability.NominalCadence = exchange.TransportWebSocket, 0
	}
	adapterSettings := factorySettingsForConfig(configName, settings)
	adapterSettings.MarketCapabilities = []exchange.MarketCapability{capability}
	feed := factoryclient.NewDerivativeFeed(scope, []string{profile.ID}, family, loader,
		nativeIdentityResolver{scope: scope}, adapterSettings)
	return &PublicFundingBoard{scope: scope, feed: feed, poll: poll, stream: stream}, nil
}

// Scope is the market this board answers for.
func (board *PublicFundingBoard) Scope() exchange.CatalogScope { return board.scope }

// Streams reports whether the board is pushed (Run's patches arrive as the
// venue sends them) rather than polled every Cadence.
func (board *PublicFundingBoard) Streams() bool { return board.stream }

// Cadence is how often a complete board arrives.
func (board *PublicFundingBoard) Cadence() time.Duration { return board.poll }

// Run delivers the board until ctx ends or the source fails: a complete
// snapshot first, then patches (complete snapshots when polled; partial
// field updates when streamed). Facts are keyed by native identity
// (Instrument.NativeSymbol and NativeID). Reconnecting is the caller's job.
func (board *PublicFundingBoard) Run(ctx context.Context, emit func(exchange.DerivativePatch)) error {
	snapshot, err := board.feed.Bootstrap(ctx)
	if err != nil {
		return err
	}
	emit(snapshot)
	return board.feed.Stream(ctx, emit)
}

// Close releases the board's stream.
func (board *PublicFundingBoard) Close() error { return board.feed.Close() }
