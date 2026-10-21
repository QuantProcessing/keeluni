package trader

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(r.Context()))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(gateway.Close)
	target, _ := url.Parse(gateway.URL)
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(Config{GatewayURL: target, GatewayToken: "gw-secret", Password: hash}), gateway, &seen
}

func request(server *Server, method, path, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://keeluni.local"+path, strings.NewReader(body))
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

func sameSite(r *http.Request) {
	r.Header.Set("Origin", "http://keeluni.local")
	r.Header.Set("X-KeelUni", "1")
}

func login(t *testing.T, server *Server) *http.Cookie {
	t.Helper()
	response := request(server, "POST", "/api/auth/login", `{"password":"correct horse"}`, sameSite)
	if response.Code != 200 {
		t.Fatalf("login = %d %s", response.Code, response.Body)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == sessionCookie {
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("cookie flags: %+v", cookie)
			}
			return cookie
		}
	}
	t.Fatal("no session cookie")
	return nil
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("pw")
	if err != nil {
		t.Fatal(err)
	}
	if !hash.Verify("pw") || hash.Verify("PW") || hash.Verify("") || PasswordHash("garbage").Verify("pw") {
		t.Fatal("verify mismatch")
	}
}

func TestProxyRequiresSessionAndInjectsGatewayToken(t *testing.T) {
	server, _, seen := newTestServer(t)
	if got := request(server, "GET", "/api/gw/v1/accounts", "", nil).Code; got != 401 {
		t.Fatalf("anonymous = %d", got)
	}
	cookie := login(t, server)
	response := request(server, "GET", "/api/gw/v1/accounts?x=1", "", func(r *http.Request) {
		r.AddCookie(cookie)
		r.Header.Set("Authorization", "Bearer attacker")
	})
	if response.Code != 200 {
		t.Fatalf("proxied = %d %s", response.Code, response.Body)
	}
	upstream := (*seen)[len(*seen)-1]
	if upstream.URL.Path != "/v1/accounts" || upstream.URL.RawQuery != "x=1" {
		t.Fatalf("upstream url = %s", upstream.URL)
	}
	if upstream.Header.Get("Authorization") != "Bearer gw-secret" || upstream.Header.Get("Cookie") != "" {
		t.Fatalf("upstream headers = %v", upstream.Header)
	}
}

func TestMutationsRequireSameOriginAndHeader(t *testing.T) {
	server, _, _ := newTestServer(t)
	cookie := login(t, server)
	cases := map[string]func(*http.Request){
		"no origin": func(r *http.Request) { r.AddCookie(cookie); r.Header.Set("X-KeelUni", "1") },
		"foreign origin": func(r *http.Request) {
			r.AddCookie(cookie)
			r.Header.Set("X-KeelUni", "1")
			r.Header.Set("Origin", "https://evil.example")
		},
		"no header": func(r *http.Request) { r.AddCookie(cookie); r.Header.Set("Origin", "http://keeluni.local") },
	}
	for name, mutate := range cases {
		if got := request(server, "POST", "/api/gw/v1/accounts/acc_x/orders", `{}`, mutate).Code; got != 403 {
			t.Errorf("%s = %d", name, got)
		}
	}
	ok := request(server, "POST", "/api/gw/v1/accounts/acc_x/orders", `{}`, func(r *http.Request) { r.AddCookie(cookie); sameSite(r) })
	if ok.Code != 200 {
		t.Fatalf("same-origin = %d", ok.Code)
	}
}

func TestLoginThrottle(t *testing.T) {
	server, _, _ := newTestServer(t)
	now := time.Now()
	server.throttle.now = func() time.Time { return now }
	if got := request(server, "POST", "/api/auth/login", `{"password":"nope"}`, sameSite).Code; got != 401 {
		t.Fatalf("bad password = %d", got)
	}
	if got := request(server, "POST", "/api/auth/login", `{"password":"correct horse"}`, sameSite).Code; got != 429 {
		t.Fatalf("throttled = %d", got)
	}
	now = now.Add(2 * time.Second)
	if got := request(server, "POST", "/api/auth/login", `{"password":"correct horse"}`, sameSite).Code; got != 200 {
		t.Fatalf("after backoff = %d", got)
	}
}

func TestSessionExpiry(t *testing.T) {
	sessions := NewSessions()
	now := time.Now()
	sessions.now = func() time.Time { return now }
	token, _ := sessions.Create()
	now = now.Add(sessionIdle - time.Minute)
	if !sessions.Valid(token) {
		t.Fatal("session expired early")
	}
	now = now.Add(sessionIdle + time.Minute)
	if sessions.Valid(token) {
		t.Fatal("idle session still valid")
	}
}

func TestSecurityHeaders(t *testing.T) {
	server, _, _ := newTestServer(t)
	response := request(server, "GET", "/api/auth/session", "", nil)
	for _, name := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy"} {
		if response.Header().Get(name) == "" {
			t.Errorf("missing %s", name)
		}
	}
}

func TestHedgerIsForwardedOnlyWhenConfigured(t *testing.T) {
	server, _, _ := newTestServer(t)
	cookie := login(t, server)
	if got := request(server, "GET", "/api/hedge/v1/groups", "", func(r *http.Request) { r.AddCookie(cookie) }).Code; got != 404 {
		t.Fatalf("hedger without config = %d", got)
	}
	apps := request(server, "GET", "/api/apps", "", func(r *http.Request) { r.AddCookie(cookie) })
	if apps.Code != 200 || !strings.Contains(apps.Body.String(), `"hedge":false`) {
		t.Fatalf("apps = %d %s", apps.Code, apps.Body)
	}

	var seen *http.Request
	hedger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(hedger.Close)
	target, _ := url.Parse(hedger.URL)
	server.config.HedgerURL, server.config.HedgerToken = target, "hedger-secret"
	server = NewServer(server.config)
	cookie = login(t, server)
	if got := request(server, "GET", "/api/hedge/v1/groups", "", nil).Code; got != 401 {
		t.Fatalf("anonymous = %d", got)
	}
	response := request(server, "GET", "/api/hedge/v1/groups?days=3", "", func(r *http.Request) { r.AddCookie(cookie) })
	if response.Code != 200 || seen == nil || seen.URL.Path != "/v1/groups" || seen.URL.RawQuery != "days=3" {
		t.Fatalf("forward = %d %v", response.Code, seen)
	}
	if seen.Header.Get("Authorization") != "Bearer hedger-secret" || seen.Header.Get("Cookie") != "" {
		t.Fatalf("hedger headers = %v", seen.Header)
	}
	if got := request(server, "POST", "/api/hedge/v1/groups", `{}`, func(r *http.Request) { r.AddCookie(cookie) }).Code; got != 403 {
		t.Fatalf("cross-site mutation = %d", got)
	}
}

func TestOptionerIsForwardedOnlyWhenConfigured(t *testing.T) {
	server, _, _ := newTestServer(t)
	cookie := login(t, server)
	if got := request(server, "GET", "/api/options/v1/risk", "", func(r *http.Request) { r.AddCookie(cookie) }).Code; got != 404 {
		t.Fatalf("optioner without config = %d", got)
	}
	var seen *http.Request
	optioner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(optioner.Close)
	target, _ := url.Parse(optioner.URL)
	server.config.OptionerURL, server.config.OptionerToken = target, "optioner-secret"
	server = NewServer(server.config)
	cookie = login(t, server)
	apps := request(server, "GET", "/api/apps", "", func(r *http.Request) { r.AddCookie(cookie) })
	if !strings.Contains(apps.Body.String(), `"options":true`) {
		t.Fatalf("apps = %s", apps.Body)
	}
	response := request(server, "GET", "/api/options/v1/risk?hedges=1", "", func(r *http.Request) { r.AddCookie(cookie) })
	if response.Code != 200 || seen == nil || seen.URL.Path != "/v1/risk" || seen.URL.RawQuery != "hedges=1" || seen.Header.Get("Authorization") != "Bearer optioner-secret" {
		t.Fatalf("forward = %d %v", response.Code, seen)
	}
}
