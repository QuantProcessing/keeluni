package factory

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/internal/factoryclient"
)

// PublicFundingBoard reads one perpetual market's whole funding board,
// credential-free. Each read is one or a few REST calls; Hyperliquid also
// offers a push stream covering every dex (StreamFundingBoard).
type PublicFundingBoard struct {
	scope  exchange.CatalogScope
	reader factoryclient.FundingBoardReader
	stream *factoryclient.HyperliquidFundingStream
}

// NewPublicFundingBoard builds the funding board of a served perpetual market.
// Construction performs no I/O.
func NewPublicFundingBoard(venue exchange.Venue, settlement string, environment Environment, httpClient *http.Client) (*PublicFundingBoard, error) {
	configName, ok := publicConnectorConfigName(venue, exchange.ProductPerp, settlement)
	if !ok || httpClient == nil {
		return nil, invalidConfig("public funding board profile is unsupported")
	}
	settings := newSettings([]Option{WithEnvironment(environment), WithHTTPClient(httpClient)})
	if err := settings.validate(); err != nil {
		return nil, err
	}
	scope, _, ok := catalogSourceForConfig(configName, settings)
	if !ok {
		return nil, invalidConfig("public funding board source is unavailable")
	}
	configured := factorySettingsForConfig(configName, settings)
	reader := factoryclient.NewFundingBoardReader(scope, configured)
	if reader == nil {
		return nil, invalidConfig("public funding board is unsupported for this venue")
	}
	board := &PublicFundingBoard{scope: scope, reader: reader}
	if venue == exchange.VenueHyperliquid {
		board.stream = factoryclient.NewHyperliquidFundingStream(scope, configured)
	}
	return board, nil
}

// Scope is the market this board answers for.
func (board *PublicFundingBoard) Scope() exchange.CatalogScope { return board.scope }

// Load reads the whole board once and validates it.
func (board *PublicFundingBoard) Load(ctx context.Context) (exchange.FundingBoard, error) {
	result, err := board.reader.LoadFundingBoard(ctx)
	if err != nil {
		return exchange.FundingBoard{}, err
	}
	if err := result.Validate(board.scope.Venue); err != nil {
		return exchange.FundingBoard{}, err
	}
	return result, nil
}

// Streams reports whether the venue pushes complete boards (see Stream).
func (board *PublicFundingBoard) Streams() bool { return board.stream != nil }

// Stream pushes complete, validated boards until ctx ends or the stream
// fails; reconnecting is the caller's job. It fails immediately for venues
// without a push stream.
func (board *PublicFundingBoard) Stream(ctx context.Context, emit func(exchange.FundingBoard)) error {
	if board.stream == nil {
		return exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: board.scope.Venue, Operation: "FundingBoard.Stream", SafeMessage: "venue has no funding board stream"})
	}
	return board.stream.StreamFundingBoard(ctx, func(result exchange.FundingBoard) {
		if result.Validate(board.scope.Venue) == nil {
			emit(result)
		}
	})
}
