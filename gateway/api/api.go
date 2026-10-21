// Package api is the gateway's HTTP and WebSocket surface (/v1).
package api

import (
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/exchange"
	"github.com/QuantProcessing/keeluni/gateway/account"
	"github.com/QuantProcessing/keeluni/gateway/funding"
	"github.com/QuantProcessing/keeluni/gateway/hub"
	"github.com/QuantProcessing/keeluni/gateway/market"
	"github.com/QuantProcessing/keeluni/gateway/optionchain"
	"github.com/QuantProcessing/keeluni/gateway/resident"
	"github.com/QuantProcessing/keeluni/gateway/tickers"
	"github.com/QuantProcessing/keeluni/gateway/venue"
)

const maxBodyBytes = 64 << 10

// Deps are the gateway's components the API serves.
type Deps struct {
	Catalog  *market.Catalog
	Accounts *account.Manager
	Feeds    *market.Feeds
	Funding  *funding.Dataset
	Options  *optionchain.Dataset
	Tickers  *tickers.Dataset
	Resident *resident.Registry
	// Hubs: public market streams, private account streams, resident datasets
	// (funding), option chains.
	Public, Private, Datasets, OptionChains *hub.Hub
}

type Server struct {
	token    []byte
	catalog  *market.Catalog
	accounts *account.Manager
	feeds    *market.Feeds
	funding  *funding.Dataset
	options  *optionchain.Dataset
	tickers  *tickers.Dataset
	resident *resident.Registry
	public   *hub.Hub
	private  *hub.Hub
	datasets *hub.Hub
	// optionHub serves the per-series option chain topics.
	optionHub *hub.Hub
	mux       *http.ServeMux
}

func NewServer(token []byte, deps Deps) *Server {
	server := &Server{
		token: token, catalog: deps.Catalog, accounts: deps.Accounts, feeds: deps.Feeds,
		funding: deps.Funding, options: deps.Options, tickers: deps.Tickers, resident: deps.Resident,
		public: deps.Public, private: deps.Private, datasets: deps.Datasets, optionHub: deps.OptionChains, mux: http.NewServeMux(),
	}
	m := server.mux
	m.HandleFunc("GET /v1/health", server.health)
	m.HandleFunc("GET /v1/venues", server.venues)
	m.HandleFunc("GET /v1/instruments", server.instruments)
	m.HandleFunc("GET /v1/orderbook", server.orderBook)
	m.HandleFunc("GET /v1/candles", server.candles)
	m.HandleFunc("GET /v1/ticker", server.ticker)
	m.HandleFunc("GET /v1/markets", server.marketList)
	m.HandleFunc("GET /v1/funding", server.fundingTable)
	m.HandleFunc("GET /v1/funding/history", server.fundingHistory)
	m.HandleFunc("GET /v1/options", server.optionSeries)
	m.HandleFunc("GET /v1/options/chain", server.optionChain)

	m.HandleFunc("GET /v1/accounts", server.listAccounts)
	m.HandleFunc("POST /v1/accounts", server.createAccount)
	m.HandleFunc("DELETE /v1/accounts/{id}", server.deleteAccount)
	m.HandleFunc("GET /v1/accounts/{id}/balances", server.balances)
	m.HandleFunc("GET /v1/accounts/{id}/margin", server.margin)
	m.HandleFunc("GET /v1/accounts/{id}/instruments", server.routes)
	m.HandleFunc("GET /v1/accounts/{id}/positions", server.positions)
	m.HandleFunc("GET /v1/accounts/{id}/orders", server.openOrders)
	m.HandleFunc("GET /v1/accounts/{id}/orders/lookup", server.lookupOrder)
	m.HandleFunc("POST /v1/accounts/{id}/orders", server.placeOrder)
	m.HandleFunc("DELETE /v1/accounts/{id}/orders/{orderId}", server.cancelOrder)
	m.HandleFunc("GET /v1/accounts/{id}/fills", server.fills)
	m.HandleFunc("PUT /v1/accounts/{id}/leverage", server.setLeverage)

	m.HandleFunc("GET /v1/ws", server.websocket)
	return server
}

// ServeHTTP enforces bearer authentication on everything but health.
func (server *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path != "/v1/health" && !server.authorized(r) {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid gateway token")
		return
	}
	started := time.Now()
	recorder := &statusRecorder{ResponseWriter: w, status: 200}
	server.mux.ServeHTTP(recorder, r)
	if r.URL.Path != "/v1/health" && r.URL.Path != "/v1/ws" {
		slog.Debug("http", "method", r.Method, "path", r.URL.Path, "status", recorder.status, "elapsed", time.Since(started).Round(time.Millisecond))
	}
}

func (server *Server) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), server.token) == 1
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController (and the WebSocket upgrade) reach the
// underlying writer.
func (recorder *statusRecorder) Unwrap() http.ResponseWriter { return recorder.ResponseWriter }

// ---------------------------------------------------------------------------
// public
// ---------------------------------------------------------------------------

func (server *Server) health(w http.ResponseWriter, _ *http.Request) {
	datasets := []resident.Status{}
	if server.resident != nil {
		datasets = server.resident.Statuses()
	}
	topics := server.public.Topics() + server.private.Topics()
	if server.datasets != nil {
		topics += server.datasets.Topics()
	}
	if server.optionHub != nil {
		topics += server.optionHub.Topics()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "markets": server.catalog.Status(), "datasets": datasets, "topics": topics})
}

func (server *Server) venues(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, server.accounts.Venues())
}

func (server *Server) instruments(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	writeJSON(w, http.StatusOK, nonNil(server.catalog.List(exchange.Venue(query.Get("venue")), exchange.Product(query.Get("product")))))
}

func (server *Server) orderBook(w http.ResponseWriter, r *http.Request) {
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	book, err := server.catalog.OrderBook(r.Context(), venue.InstrumentID(r.URL.Query().Get("inst")), depth)
	server.respond(w, book, err)
}

func (server *Server) candles(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	candles, err := server.catalog.Candles(r.Context(), venue.InstrumentID(query.Get("inst")), query.Get("interval"),
		parseMillis(query.Get("start")), parseMillis(query.Get("end")), limit)
	server.respond(w, candles, err)
}

func (server *Server) ticker(w http.ResponseWriter, r *http.Request) {
	stats, err := server.feeds.Stats(r.Context(), venue.InstrumentID(r.URL.Query().Get("inst")))
	server.respond(w, stats, err)
}

// marketList serves every spot and perpetual instrument's last price, 24h
// change and value, funding and open interest, by 24h value.
func (server *Server) marketList(w http.ResponseWriter, r *http.Request) {
	if server.tickers == nil {
		writeJSON(w, http.StatusOK, []tickers.Row{})
		return
	}
	rows := server.tickers.List()
	// ~13k rows: 1.6 MB of JSON, ~15% of that gzipped.
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Vary", "Accept-Encoding")
		zipped := gzip.NewWriter(w)
		_ = json.NewEncoder(zipped).Encode(rows)
		_ = zipped.Close()
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// fundingTable serves the resident funding table, optionally filtered by
// venue and base asset; ?inst= returns one quote.
func (server *Server) fundingTable(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if inst := query.Get("inst"); inst != "" {
		quote, err := server.funding.Get(venue.InstrumentID(inst))
		server.respond(w, quote, err)
		return
	}
	writeJSON(w, http.StatusOK, server.funding.Table(exchange.Venue(query.Get("venue")), query.Get("base")))
}

func (server *Server) fundingHistory(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	points, err := server.funding.History(r.Context(), venue.InstrumentID(query.Get("inst")), parseMillis(query.Get("since")), parseMillis(query.Get("until")))
	server.respond(w, nonNil(points), err)
}

// optionSeries lists every loaded option series (an underlying in one
// premium asset) with its front ATM IV.
func (server *Server) optionSeries(w http.ResponseWriter, _ *http.Request) {
	if server.options == nil {
		writeJSON(w, http.StatusOK, []optionchain.Series{})
		return
	}
	writeJSON(w, http.StatusOK, nonNil(server.options.List()))
}

// optionChain serves one series: ?series=deribit:BTC:BTC[&expiry=ms].
func (server *Server) optionChain(w http.ResponseWriter, r *http.Request) {
	if server.options == nil {
		writeError(w, http.StatusNotFound, "NO_OPTIONS", "options are not served")
		return
	}
	query := r.URL.Query()
	expiry, _ := strconv.ParseInt(query.Get("expiry"), 10, 64)
	chain, err := server.options.Get(query.Get("series"), expiry)
	server.respond(w, chain, err)
}

// ---------------------------------------------------------------------------
// accounts
// ---------------------------------------------------------------------------

func (server *Server) listAccounts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, server.accounts.List())
}

func (server *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var request account.CreateRequest
	if !decodeBody(w, r, &request) {
		return
	}
	view, err := server.accounts.Create(r.Context(), request)
	if err != nil {
		server.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (server *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := server.accounts.Delete(r.PathValue("id")); err != nil {
		server.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (server *Server) balances(w http.ResponseWriter, r *http.Request) {
	balances, err := server.accounts.Balances(r.Context(), r.PathValue("id"))
	server.respond(w, balances, err)
}

// margin returns the account's shared margin pool; null for accounts without one.
func (server *Server) margin(w http.ResponseWriter, r *http.Request) {
	margin, err := server.accounts.Margin(r.Context(), r.PathValue("id"))
	server.respond(w, margin, err)
}

func (server *Server) routes(w http.ResponseWriter, r *http.Request) {
	routes, err := server.accounts.Routes(r.Context(), r.PathValue("id"))
	server.respond(w, routes, err)
}

func (server *Server) positions(w http.ResponseWriter, r *http.Request) {
	positions, err := server.accounts.Positions(r.Context(), r.PathValue("id"))
	server.respond(w, positions, err)
}

func (server *Server) openOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := server.accounts.OpenOrders(r.Context(), r.PathValue("id"), venue.InstrumentID(r.URL.Query().Get("inst")))
	server.respond(w, orders, err)
}

func (server *Server) fills(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	fills, err := server.accounts.Fills(r.Context(), r.PathValue("id"), venue.InstrumentID(query.Get("inst")), parseMillis(query.Get("start")), limit)
	server.respond(w, fills, err)
}

func (server *Server) placeOrder(w http.ResponseWriter, r *http.Request) {
	var request account.PlaceRequest
	if !decodeBody(w, r, &request) {
		return
	}
	result, err := server.accounts.Place(r.Context(), r.PathValue("id"), request)
	if err != nil {
		server.fail(w, err)
		return
	}
	status := http.StatusOK
	switch result.Status {
	case account.OutcomeRejected:
		status = http.StatusUnprocessableEntity
	case account.OutcomeUnknown:
		status = http.StatusAccepted
	}
	writeJSON(w, status, result)
}

func (server *Server) lookupOrder(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	result, err := server.accounts.Lookup(r.Context(), r.PathValue("id"), venue.InstrumentID(query.Get("inst")), query.Get("clientOrderId"), query.Get("orderId"))
	server.respond(w, result, err)
}

func (server *Server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	err := server.accounts.Cancel(r.Context(), r.PathValue("id"), venue.InstrumentID(r.URL.Query().Get("inst")), r.PathValue("orderId"))
	if err != nil {
		server.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (server *Server) setLeverage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Instrument venue.InstrumentID `json:"inst"`
		Leverage   int                `json:"leverage"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	effective, err := server.accounts.SetLeverage(r.Context(), r.PathValue("id"), request.Instrument, request.Leverage)
	server.respond(w, map[string]int{"leverage": effective}, err)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (server *Server) respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		server.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

// fail maps errors to HTTP. Venue failures are classified once, the same way
// for every venue (see account.Category), so clients need no venue knowledge.
// Only credential-free text leaves the process: our own messages and the
// venue's rejection text.
func (server *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, account.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "account not found")
		return
	case errors.Is(err, market.ErrUnknownInstrument):
		writeError(w, http.StatusNotFound, "UNKNOWN_INSTRUMENT", err.Error())
		return
	case errors.Is(err, account.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	case errors.Is(err, account.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, "UNSUPPORTED", "operation is not supported for this venue")
		return
	case errors.Is(err, funding.ErrNotPerpetual), errors.Is(err, funding.ErrInvalidWindow):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	case errors.Is(err, funding.ErrNoQuote):
		writeError(w, http.StatusNotFound, "NO_FUNDING", err.Error())
		return
	case errors.Is(err, funding.ErrNoHistory):
		writeError(w, http.StatusNotImplemented, "UNSUPPORTED", err.Error())
		return
	case errors.Is(err, optionchain.ErrUnknownSeries):
		writeError(w, http.StatusNotFound, "UNKNOWN_SERIES", err.Error())
		return
	case errors.Is(err, optionchain.ErrNoChain):
		writeError(w, http.StatusServiceUnavailable, "NO_CHAIN", err.Error())
		return
	}
	failure := account.Classify(err)
	status := map[account.Category]int{
		account.CategoryNetwork:     http.StatusBadGateway,
		account.CategoryRejected:    http.StatusUnprocessableEntity,
		account.CategoryCredentials: http.StatusUnprocessableEntity,
		account.CategoryRateLimited: http.StatusTooManyRequests,
		account.CategoryInvalid:     http.StatusBadRequest,
		account.CategoryUnsupported: http.StatusNotImplemented,
		account.CategoryVenue:       http.StatusBadGateway,
	}[failure.Category]
	if failure.Category == account.CategoryVenue {
		slog.Warn("venue error", "error", err)
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code":         strings.ToUpper(string(failure.Category)),
		"category":     failure.Category,
		"message":      failure.Message(),
		"venueCode":    failure.Code,
		"venueMessage": failure.VenueMessage,
		"retryable":    failure.Retryable,
	}})
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body is not valid JSON for this endpoint")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func parseMillis(value string) time.Time {
	millis, err := strconv.ParseInt(value, 10, 64)
	if err != nil || millis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
