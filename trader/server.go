// Package trader is the manual-trading application: a thin, stateless
// backend-for-frontend that authenticates the operator and forwards to the
// gateway. It has no database; every trading fact comes from the gateway.
package trader

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type Config struct {
	GatewayURL   *url.URL // e.g. http://127.0.0.1:8081
	GatewayToken string
	// HedgerURL is the hedge app (optional); its API is served at /api/hedge.
	HedgerURL    *url.URL
	HedgerToken  string
	Password     PasswordHash
	WebDir       string // built SPA; empty disables static serving
	SecureCookie bool   // set when served over HTTPS
}

type Server struct {
	config   Config
	sessions *Sessions
	throttle *Throttle
	proxy    *httputil.ReverseProxy
	hedger   *httputil.ReverseProxy
	mux      *http.ServeMux
}

// backendProxy forwards a prefix of /api to a bearer-authenticated backend.
func backendProxy(target *url.URL, token, prefix, name string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.URL.Path = strings.TrimPrefix(request.In.URL.Path, prefix)
			request.Out.URL.RawPath = ""
			request.Out.Header.Del("Cookie")
			request.Out.Header.Del("X-KeelUni")
			request.Out.Header.Set("Authorization", "Bearer "+token)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn(name+" unreachable", "error", err)
			writeError(w, http.StatusBadGateway, strings.ToUpper(name)+"_UNAVAILABLE", name+" is unreachable")
		},
	}
}

func NewServer(config Config) *Server {
	server := &Server{config: config, sessions: NewSessions(), throttle: NewThrottle(), mux: http.NewServeMux()}
	server.proxy = backendProxy(config.GatewayURL, config.GatewayToken, "/api/gw", "gateway")
	server.mux.HandleFunc("POST /api/auth/login", server.login)
	server.mux.HandleFunc("POST /api/auth/logout", server.logout)
	server.mux.HandleFunc("GET /api/auth/session", server.session)
	server.mux.HandleFunc("GET /api/apps", server.authenticated(server.apps))
	server.mux.HandleFunc("GET /api/ws", server.authenticated(func(w http.ResponseWriter, r *http.Request) {
		server.relay(w, r, config.GatewayURL, config.GatewayToken, "/v1/ws")
	}))
	server.mux.HandleFunc("/api/gw/v1/", server.authenticated(server.forward))
	if config.HedgerURL != nil {
		server.hedger = backendProxy(config.HedgerURL, config.HedgerToken, "/api/hedge", "hedger")
		server.mux.HandleFunc("GET /api/hedge/ws", server.authenticated(func(w http.ResponseWriter, r *http.Request) {
			server.relay(w, r, config.HedgerURL, config.HedgerToken, "/v1/ws")
		}))
		server.mux.HandleFunc("/api/hedge/v1/", server.authenticated(server.hedger.ServeHTTP))
	}
	server.mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "unknown endpoint")
	})
	if config.WebDir != "" {
		server.mux.HandleFunc("/", server.static)
	}
	return server
}

func (server *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	if server.config.SecureCookie {
		header.Set("Strict-Transport-Security", "max-age=31536000")
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		header.Set("Cache-Control", "no-store")
		// CSRF: every state-changing API call must come from our own origin
		// and carry a header a cross-site form cannot set.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !sameOrigin(r) || r.Header.Get("X-KeelUni") != "1" {
				writeError(w, http.StatusForbidden, "CSRF", "cross-origin request refused")
				return
			}
		}
	}
	server.mux.ServeHTTP(w, r)
}

// sameOrigin requires an Origin (or, failing that, Referer) whose host equals
// the request Host. Browsers always send Origin on cross-origin and on
// non-GET same-origin fetches.
func sameOrigin(r *http.Request) bool {
	source := r.Header.Get("Origin")
	if source == "" {
		source = r.Header.Get("Referer")
	}
	if source == "" {
		return false
	}
	parsed, err := url.Parse(source)
	return err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host)
}

func (server *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || !server.sessions.Valid(cookie.Value) {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "sign in required")
			return
		}
		next(w, r)
	}
}

func (server *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if wait := server.throttle.Wait(ip); wait > 0 {
		w.Header().Set("Retry-After", retryAfterHeader(wait))
		writeError(w, http.StatusTooManyRequests, "THROTTLED", "too many attempts; try again later")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "invalid request")
		return
	}
	if body.Password == "" || !server.config.Password.Verify(body.Password) {
		server.throttle.Fail(ip)
		slog.Warn("login failed", "ip", ip)
		writeError(w, http.StatusUnauthorized, "INVALID_PASSWORD", "incorrect password")
		return
	}
	server.throttle.Succeed(ip)
	token, err := server.sessions.Create()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "could not create session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: server.config.SecureCookie,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessionAbsolute / time.Second),
	})
	slog.Info("login succeeded", "ip", ip)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (server *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		server.sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: server.config.SecureCookie, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (server *Server) session(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookie)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": err == nil && server.sessions.Valid(cookie.Value)})
}

func (server *Server) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/gw/v1/ws" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "use /api/ws")
		return
	}
	server.proxy.ServeHTTP(w, r)
}

// apps lists the optional apps this trader serves, so the web app shows
// only the menus it can use.
func (server *Server) apps(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"hedge": server.hedger != nil})
}

// relay bridges one browser socket to one backend socket. Messages are
// relayed verbatim in both directions; the backend validates subscriptions.
func (server *Server) relay(w http.ResponseWriter, r *http.Request, backend *url.URL, token, path string) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "CSRF", "cross-origin websocket refused")
		return
	}
	browser, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer browser.CloseNow()
	browser.SetReadLimit(4 << 10)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upstreamURL := *backend
	upstreamURL.Scheme = map[string]string{"https": "wss"}[upstreamURL.Scheme]
	if upstreamURL.Scheme == "" {
		upstreamURL.Scheme = "ws"
	}
	upstreamURL.Path = path
	dialCtx, stopDial := context.WithTimeout(ctx, 10*time.Second)
	upstream, _, err := websocket.Dial(dialCtx, upstreamURL.String(), &websocket.DialOptions{
		HTTPHeader:      http.Header{"Authorization": {"Bearer " + token}},
		CompressionMode: websocket.CompressionContextTakeover,
	})
	stopDial()
	if err != nil {
		slog.Warn("backend websocket unreachable", "backend", backend.Host, "error", err)
		browser.Close(websocket.StatusTryAgainLater, "backend unavailable")
		return
	}
	defer upstream.CloseNow()
	upstream.SetReadLimit(16 << 20)

	relay := func(from, to *websocket.Conn) {
		defer cancel()
		for {
			kind, data, err := from.Read(ctx)
			if err != nil {
				return
			}
			writeCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			err = to.Write(writeCtx, kind, data)
			stop()
			if err != nil {
				return
			}
		}
	}
	go relay(browser, upstream)
	relay(upstream, browser)
}

func (server *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clean := path.Clean("/" + r.URL.Path)
	file := filepath.Join(server.config.WebDir, filepath.FromSlash(clean))
	if info, err := os.Stat(file); err == nil && !info.IsDir() {
		if strings.HasPrefix(clean, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, file)
		return
	}
	// SPA fallback.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(server.config.WebDir, "index.html"))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}
