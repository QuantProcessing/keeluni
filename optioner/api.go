package optioner

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/coder/websocket"
)

// API is the options app's HTTP surface (/v1), behind the trader.
//
//	GET  /v1/health
//	GET  /v1/risk?accounts=a,b&hedges=1&days=1        portfolio risk
//	POST /v1/preview      {legs}                       resolved prices, no order
//	POST /v1/executions   {label, legs}                send a package
//	GET  /v1/executions                                 last day
//	POST /v1/executions/{id}/cancel
//	GET  /v1/hedge?underlying=BTC&account=acc&...      delta hedge suggestion
//	GET  /v1/ws           {"ch":"risk"|"executions"}   pushed on change
type API struct {
	service *Service
	token   []byte
	mux     *http.ServeMux
}

func NewAPI(service *Service, token string) *API {
	api := &API{service: service, token: []byte(token), mux: http.NewServeMux()}
	m := api.mux
	m.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, map[string]bool{"ok": true}) })
	m.HandleFunc("GET /v1/risk", api.risk)
	m.HandleFunc("POST /v1/preview", api.preview)
	m.HandleFunc("GET /v1/executions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, nonNil(service.Executions.List()))
	})
	m.HandleFunc("POST /v1/executions", api.submit)
	m.HandleFunc("POST /v1/executions/{id}/cancel", api.cancel)
	m.HandleFunc("GET /v1/hedge", api.hedge)
	m.HandleFunc("GET /v1/ws", api.websocket)
	return api
}

func (api *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path != "/v1/health" && !api.authorized(r) {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid optioner token")
		return
	}
	api.mux.ServeHTTP(w, r)
}

func (api *API) authorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	return strings.HasPrefix(header, prefix) && subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), api.token) == 1
}

func riskOptions(r *http.Request) RiskOptions {
	query := r.URL.Query()
	options := RiskOptions{IncludeHedges: query.Get("hedges") != "0"}
	if list := strings.TrimSpace(query.Get("accounts")); list != "" {
		options.Accounts = map[string]bool{}
		for _, id := range strings.Split(list, ",") {
			options.Accounts[strings.TrimSpace(id)] = true
		}
	}
	if days, err := strconv.ParseFloat(query.Get("days"), 64); err == nil && days >= 0 && days <= 365 {
		options.HorizonDays = days
	}
	return options
}

func (api *API) risk(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, api.service.Risk(riskOptions(r)))
}

func (api *API) preview(w http.ResponseWriter, r *http.Request) {
	var request ExecRequest
	if !decodeBody(w, r, &request) {
		return
	}
	legs, err := api.service.Executions.Prepare(request)
	api.respond(w, legs, err)
}

func (api *API) submit(w http.ResponseWriter, r *http.Request) {
	var request ExecRequest
	if !decodeBody(w, r, &request) {
		return
	}
	execution, err := api.service.Executions.Submit(r.Context(), request)
	api.respond(w, execution, err)
}

func (api *API) cancel(w http.ResponseWriter, r *http.Request) {
	execution, err := api.service.Executions.Cancel(r.Context(), r.PathValue("id"))
	if err != nil && errors.Is(err, ErrNotFound) {
		api.fail(w, err)
		return
	}
	if err != nil {
		slog.Warn("execution cancel incomplete", "execution", r.PathValue("id"), "error", err)
	}
	writeJSON(w, http.StatusOK, execution)
}

func (api *API) hedge(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	suggestion, err := api.service.SuggestHedge(query.Get("underlying"), query.Get("account"), riskOptions(r))
	api.respond(w, suggestion, err)
}

// websocket pushes {"ch":"risk"} and {"ch":"executions"} on every change
// (at most every 250 ms) and every 2 s for live marks. The client chooses
// the risk options once in the query string.
func (api *API) websocket(w http.ResponseWriter, r *http.Request) {
	socket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer socket.CloseNow()
	socket.SetReadLimit(4 << 10)
	options := riskOptions(r)
	ctx := socket.CloseRead(r.Context())
	changes, stop := api.service.Listen()
	defer stop()
	push := func() error {
		for _, message := range []map[string]any{
			{"ch": "risk", "data": api.service.Risk(options)},
			{"ch": "executions", "data": nonNil(api.service.Executions.List())},
		} {
			payload, _ := json.Marshal(message)
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := socket.Write(writeCtx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				return err
			}
		}
		return nil
	}
	if push() != nil {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	throttle := time.NewTimer(time.Hour)
	defer throttle.Stop()
	dirty := false
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
			if push() != nil {
				return
			}
		case <-ticker.C:
			if push() != nil {
				return
			}
		}
	}
}

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
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", strings.ReplaceAll(err.Error(), ErrInvalid.Error()+": ", ""))
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.As(err, &gatewayErr):
		writeError(w, http.StatusBadGateway, "GATEWAY", gatewayErr.Error())
	default:
		slog.Error("optioner request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL", "the options app could not complete the request")
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
