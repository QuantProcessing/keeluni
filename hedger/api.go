package hedger

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/gwclient"
	"github.com/coder/websocket"
	"github.com/shopspring/decimal"
)

// API is the hedger's HTTP surface (/v1). It sits behind the trader, which
// authenticates the operator and forwards with the hedger's bearer token.
type API struct {
	service *Service
	token   []byte
	mux     *http.ServeMux
}

func NewAPI(service *Service, token string) *API {
	api := &API{service: service, token: []byte(token), mux: http.NewServeMux()}
	m := api.mux
	m.HandleFunc("GET /v1/health", api.health)
	m.HandleFunc("GET /v1/groups", api.groups)
	m.HandleFunc("GET /v1/groups/{id}", api.group)
	m.HandleFunc("POST /v1/preview", api.preview)
	m.HandleFunc("POST /v1/groups", api.open)
	m.HandleFunc("PATCH /v1/groups/{id}", api.edit)
	m.HandleFunc("POST /v1/groups/{id}/close", api.close)
	m.HandleFunc("POST /v1/groups/{id}/cancel", api.cancel)
	m.HandleFunc("POST /v1/groups/{id}/rehedge", api.rehedge)
	m.HandleFunc("POST /v1/groups/{id}/trim", api.trim)
	m.HandleFunc("POST /v1/groups/{id}/unwind", api.unwind)
	m.HandleFunc("GET /v1/ws", api.websocket)
	return api
}

func (api *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path != "/v1/health" && !api.authorized(r) {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid hedger token")
		return
	}
	api.mux.ServeHTTP(w, r)
}

func (api *API) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	return strings.HasPrefix(header, prefix) && subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), api.token) == 1
}

func (api *API) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := api.service.Ready(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "database": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// views builds views for groups, with funding totals in one query.
func (api *API) views(ctx context.Context, groups []Group) ([]GroupView, error) {
	ids := make([]string, len(groups))
	for i, group := range groups {
		ids[i] = group.ID
	}
	totals, err := api.service.store.FundingTotals(ctx, ids)
	if err != nil {
		return nil, err
	}
	views := make([]GroupView, 0, len(groups))
	for _, group := range groups {
		view, err := api.service.Engine.View(ctx, group, totals[group.ID])
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

// groups lists active groups plus those finished in the last ?days (default 7).
func (api *API) groups(w http.ResponseWriter, r *http.Request) {
	days := 7
	if value := r.URL.Query().Get("days"); value != "" {
		if parsed, err := decimal.NewFromString(value); err == nil && parsed.IsPositive() {
			days = int(parsed.IntPart())
		}
	}
	groups, err := api.service.store.Groups(r.Context(), time.Now().Add(-time.Duration(days)*24*time.Hour))
	if err != nil {
		api.fail(w, err)
		return
	}
	views, err := api.views(r.Context(), groups)
	if err != nil {
		api.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nonNil(views))
}

// GroupDetail is one group with its history.
type GroupDetail struct {
	GroupView
	Actions []Action         `json:"actions"`
	Orders  []Order          `json:"orders"`
	Fills   []Fill           `json:"fills"`
	Events  []Event          `json:"events"`
	Funding []FundingAccrual `json:"funding"`
}

func (api *API) group(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	group, err := api.service.store.Group(ctx, r.PathValue("id"))
	if err != nil {
		api.fail(w, err)
		return
	}
	views, err := api.views(ctx, []Group{group})
	if err != nil {
		api.fail(w, err)
		return
	}
	detail := GroupDetail{GroupView: views[0]}
	if detail.Actions, err = api.service.store.Actions(ctx, group.ID); err == nil {
		if detail.Orders, err = api.service.store.Orders(ctx, group.ID); err == nil {
			if detail.Fills, err = api.service.store.Fills(ctx, group.ID); err == nil {
				if detail.Events, err = api.service.store.Events(ctx, group.ID, 200); err == nil {
					detail.Funding, err = api.service.store.FundingLog(ctx, group.ID)
				}
			}
		}
	}
	if err != nil {
		api.fail(w, err)
		return
	}
	detail.Actions, detail.Orders, detail.Fills, detail.Events, detail.Funding = nonNil(detail.Actions), nonNil(detail.Orders), nonNil(detail.Fills), nonNil(detail.Events), nonNil(detail.Funding)
	writeJSON(w, http.StatusOK, detail)
}

// Preview is what an open would do, computed from the live books: sized
// quantities, the cost of crossing each book, fees are not included (they
// depend on the account's tier).
type Preview struct {
	SizePlan
	Price         [2]decimal.Decimal        `json:"price"`
	Sweep         [2]Sweep                  `json:"sweep"`
	SpreadBps     decimal.Decimal           `json:"spreadBps"`
	Funding       [2]*gwclient.FundingQuote `json:"funding"`
	NetFundingApr decimal.Decimal           `json:"netFundingApr"`
}

func (api *API) preview(w http.ResponseWriter, r *http.Request) {
	var request OpenRequest
	if !decodeBody(w, r, &request) {
		return
	}
	ctx := r.Context()
	instruments, accounts, err := api.service.Engine.catalog(ctx)
	if err != nil {
		api.fail(w, err)
		return
	}
	var legs [2]Leg
	for i := range legs {
		if legs[i], err = api.service.Engine.resolveLeg(ctx, i, request.Legs[i], instruments, accounts, false); err != nil {
			api.fail(w, err)
			return
		}
	}
	var preview Preview
	var books [2][2]BookSide
	for i, leg := range legs {
		bids, asks, ok := api.service.Market.Book(ctx, leg.Instrument, 5*time.Second)
		if !ok {
			api.fail(w, invalidf("no order book for %s yet", leg.Instrument))
			return
		}
		books[i] = [2]BookSide{bids, asks}
		preview.Price[i] = api.service.Market.TradePrice(leg.Instrument, leg.Side)
	}
	prices := preview.Price
	if request.Mode == ModeLimit && (request.Maker == 0 || request.Maker == 1) && request.Price.IsPositive() {
		prices[request.Maker] = request.Price
	}
	plan, err := PlanSize(SizeInput{Size: request.Size, Unit: request.Unit, Match: request.Match, Price: prices, Ratio: request.Ratio}, legs)
	if err != nil {
		api.fail(w, err)
		return
	}
	preview.SizePlan = plan
	for i, leg := range legs {
		if request.Mode == ModeLimit && request.Maker == i {
			continue // the maker leg does not cross the book
		}
		side := books[i][1]
		if leg.Side == "sell" {
			side = books[i][0]
		}
		preview.Sweep[i] = SweepBook(side, plan.Qty[i], leg.Side == "buy")
	}
	group := Group{Match: request.Match, Ratio: plan.Ratio, Legs: legs}
	preview.SpreadBps = SpreadBps(group, prices[0], prices[1])
	earn, notional := decimal.Zero, decimal.Zero
	for i, leg := range legs {
		if leg.Product != "perp" {
			continue
		}
		if quote, ok := api.service.Market.Funding(leg.Instrument); ok {
			copied := quote
			preview.Funding[i] = &copied
			apr, _ := decimal.NewFromString(quote.Apr)
			earn = earn.Add(apr.Mul(plan.Notional[i]).Mul(fundingSign(leg)))
		}
	}
	notional = plan.Notional[0].Add(plan.Notional[1])
	if notional.IsPositive() {
		preview.NetFundingApr = earn.Div(notional.Div(decimal.NewFromInt(2))).Round(6)
	}
	writeJSON(w, http.StatusOK, preview)
}

func (api *API) open(w http.ResponseWriter, r *http.Request) {
	var request OpenRequest
	if !decodeBody(w, r, &request) {
		return
	}
	for _, leg := range request.Legs {
		api.service.UseAccount(leg.AccountID)
	}
	group, err := api.service.Engine.Open(r.Context(), api.service.Market, request)
	if err != nil {
		api.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, group)
}

func (api *API) edit(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Label string `json:"label"`
		Note  string `json:"note"`
	}
	if !decodeBody(w, r, &request) {
		return
	}
	if len(request.Label) > 80 || len(request.Note) > 2000 {
		api.fail(w, invalidf("label or note is too long"))
		return
	}
	if err := api.service.store.UpdateGroupText(r.Context(), r.PathValue("id"), strings.TrimSpace(request.Label), strings.TrimSpace(request.Note)); err != nil {
		api.fail(w, err)
		return
	}
	api.service.broadcast(r.PathValue("id"))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (api *API) close(w http.ResponseWriter, r *http.Request) {
	var request CloseRequest
	if !decodeBody(w, r, &request) {
		return
	}
	action, err := api.service.Engine.Close(r.Context(), r.PathValue("id"), request)
	api.respond(w, action, err)
}

func (api *API) cancel(w http.ResponseWriter, r *http.Request) {
	err := api.service.Engine.Cancel(r.PathValue("id"))
	api.respond(w, map[string]bool{"ok": true}, err)
}

func (api *API) rehedge(w http.ResponseWriter, r *http.Request) {
	action, err := api.service.Engine.Rehedge(r.Context(), r.PathValue("id"))
	api.respond(w, action, err)
}

func (api *API) trim(w http.ResponseWriter, r *http.Request) {
	action, err := api.service.Engine.Trim(r.Context(), r.PathValue("id"))
	api.respond(w, action, err)
}

func (api *API) unwind(w http.ResponseWriter, r *http.Request) {
	action, err := api.service.Engine.Unwind(r.Context(), r.PathValue("id"))
	api.respond(w, action, err)
}

// websocket pushes {"ch":"groups","data":[GroupView...]} whenever any group
// changes, at most every 250 ms, plus every 2 s while groups are active so
// marks, spreads and PnL stay current.
func (api *API) websocket(w http.ResponseWriter, r *http.Request) {
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer socket.CloseNow()
	socket.SetReadLimit(4 << 10)
	ctx := socket.CloseRead(r.Context())
	changes, stop := api.service.Listen()
	defer stop()
	push := func() error {
		groups, err := api.service.store.Groups(ctx, time.Now().Add(-7*24*time.Hour))
		if err != nil {
			return err
		}
		views, err := api.views(ctx, groups)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"ch": "groups", "data": nonNil(views)})
		writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return socket.Write(writeCtx, websocket.MessageText, payload)
	}
	if err := push(); err != nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	dirty := false
	throttle := time.NewTimer(time.Hour)
	defer throttle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			if !dirty {
				dirty = true
				throttle.Reset(250 * time.Millisecond)
			}
		case <-throttle.C:
			dirty = false
			if err := push(); err != nil {
				return
			}
		case <-ticker.C:
			if err := push(); err != nil {
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (api *API) respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		api.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (api *API) fail(w http.ResponseWriter, err error) {
	var gatewayErr *gwclient.Error
	switch {
	case IsInvalid(err):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.As(err, &gatewayErr):
		writeError(w, http.StatusBadGateway, "GATEWAY", gatewayErr.Error())
	default:
		slog.Error("hedger request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the hedger could not complete the request")
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
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

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
