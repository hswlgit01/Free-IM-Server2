package msggateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	commonConfig "github.com/openimsdk/open-im-server/v3/pkg/common/config"
	pbAuth "github.com/openimsdk/open-im-server/v3/protocol/auth"
	"github.com/openimsdk/tools/apiresp"
	"github.com/openimsdk/tools/mcontext"
)

func TestClientIPFromRequest(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		xRealIP    string
		want       string
	}{
		{
			name:       "direct public peer cannot spoof forwarding headers",
			remoteAddr: "198.51.100.20:4567",
			xff:        "1.2.3.4",
			xRealIP:    "5.6.7.8",
			want:       "198.51.100.20",
		},
		{
			name:       "trusted proxy uses overwritten real IP",
			remoteAddr: "127.0.0.1:4567",
			xff:        "1.2.3.4, 203.0.113.9",
			xRealIP:    "203.0.113.9",
			want:       "203.0.113.9",
		},
		{
			name:       "spoofed XFF prefix is ignored",
			remoteAddr: "172.18.0.4:4567",
			xff:        "1.2.3.4, 203.0.113.10",
			want:       "203.0.113.10",
		},
		{
			name:       "trusted proxy hops are stripped from right",
			remoteAddr: "10.0.0.3:4567",
			xff:        "1.2.3.4, 203.0.113.11, 10.0.0.2",
			want:       "203.0.113.11",
		},
		{
			name:       "malformed real IP falls back to forwarded chain",
			remoteAddr: "10.0.0.3:4567",
			xff:        "bad, 203.0.113.12",
			xRealIP:    "not-an-ip",
			want:       "203.0.113.12",
		},
		{
			name:       "IPv6 remote address is normalized",
			remoteAddr: "[2001:db8::1]:4567",
			want:       "2001:db8::1",
		},
		{
			name:       "IPv4 mapped IPv6 is normalized",
			remoteAddr: "127.0.0.1:4567",
			xRealIP:    "::ffff:192.0.2.44",
			want:       "192.0.2.44",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://gateway/", nil)
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set("X-Forwarded-For", tt.xff)
			req.Header.Set("X-Real-IP", tt.xRealIP)
			if got := clientIPFromRequest(req); got != tt.want {
				t.Fatalf("clientIPFromRequest() = %q, want %q", got, tt.want)
			}
		})
	}

	if got := clientIPFromRequest(nil); got != "" {
		t.Fatalf("clientIPFromRequest(nil) = %q, want empty", got)
	}
}

func TestChatLoginCityChecker(t *testing.T) {
	t.Run("allowed request includes contract headers and body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != checkWSLoginCityPath {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("operationID"); got != "op-1" {
				t.Fatalf("operationID = %q", got)
			}
			if got := r.Header.Get(internalSecretHeader); got != "internal-secret" {
				t.Fatalf("internal secret header = %q", got)
			}
			var body checkWSLoginCityRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.UserID != "user-1" || body.IP != "203.0.113.20" {
				t.Fatalf("unexpected body: %+v", body)
			}
			_, _ = w.Write([]byte(`{"errCode":0,"data":{"allowed":true,"reason":"same_city","current_city":"A","bound_city":"A"}}`))
		}))
		defer server.Close()

		checker := newChatLoginCityChecker(server.URL, "internal-secret")
		result, err := checker.Check(context.Background(), "user-1", "203.0.113.20", "op-1")
		if err != nil {
			t.Fatal(err)
		}
		if !result.Allowed || result.Reason != "same_city" || result.CurrentCity != "A" || result.BoundCity != "A" {
			t.Fatalf("unexpected result: %+v", result)
		}
	})

	t.Run("redirect is not followed with internal secret", func(t *testing.T) {
		redirectTargetCalled := false
		redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			redirectTargetCalled = true
		}))
		defer redirectTarget.Close()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Location", redirectTarget.URL)
			w.WriteHeader(http.StatusFound)
		}))
		defer server.Close()

		checker := newChatLoginCityChecker(server.URL, "internal-secret")
		if _, err := checker.Check(context.Background(), "user-1", "203.0.113.20", "op-1"); err == nil {
			t.Fatal("redirect response should be treated as a fail-open checker error")
		}
		if redirectTargetCalled {
			t.Fatal("redirect target was called; internal secret could have leaked")
		}
	})

	t.Run("missing internal secret fails before request", func(t *testing.T) {
		checker := newChatLoginCityChecker("http://127.0.0.1:1", "")
		if _, err := checker.Check(context.Background(), "user-1", "203.0.113.20", "op-1"); err == nil {
			t.Fatal("missing internal secret should return an error")
		}
	})

	tests := []struct {
		name     string
		status   int
		response string
		denied   bool
		wantErr  bool
	}{
		{
			name:     "city mismatch is an explicit denial",
			status:   http.StatusOK,
			response: `{"errCode":11007,"errMsg":"login city mismatch","data":{"allowed":false,"reason":"city_mismatch","current_city":"B","bound_city":"A"}}`,
			denied:   true,
		},
		{name: "other business error is fail-open input", status: http.StatusOK, response: `{"errCode":1001,"errMsg":"bad request"}`, wantErr: true},
		{name: "HTTP error is fail-open input", status: http.StatusServiceUnavailable, response: `{}`, wantErr: true},
		{name: "malformed response is fail-open input", status: http.StatusOK, response: `{`, wantErr: true},
		{name: "missing data is fail-open input", status: http.StatusOK, response: `{"errCode":0}`, wantErr: true},
		{name: "allowed false without 11007 is fail-open input", status: http.StatusOK, response: `{"errCode":0,"data":{"allowed":false}}`, wantErr: true},
		{
			name:     "oversized response is fail-open input",
			status:   http.StatusOK,
			response: `{"errCode":0,"data":{"allowed":true,"reason":"` + strings.Repeat("x", maxLoginCityResponseSize) + `"}}`,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			checker := newChatLoginCityChecker(server.URL, "secret")
			result, err := checker.Check(context.Background(), "user-1", "203.0.113.20", "op-1")
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.denied && (result == nil || result.Allowed || result.Reason != "city_mismatch") {
				t.Fatalf("unexpected denial result: %+v", result)
			}
		})
	}
}

type fakeLoginCityChecker struct {
	result *loginCityCheckResult
	err    error
}

func (f *fakeLoginCityChecker) Check(context.Context, string, string, string) (*loginCityCheckResult, error) {
	return f.result, f.err
}

type fakeWSAuthClient struct {
	forceLogoutErr   error
	forceLogoutCalls int
	forceLogoutUser  string
	forceLogoutPID   int32
	forceLogoutAdmin string
	kickTokensCalls  int
}

func (f *fakeWSAuthClient) ParseToken(context.Context, string) (*pbAuth.ParseTokenResp, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeWSAuthClient) ForceLogout(ctx context.Context, userID string, platformID int32) error {
	f.forceLogoutCalls++
	f.forceLogoutUser = userID
	f.forceLogoutPID = platformID
	f.forceLogoutAdmin = mcontext.GetOpUserID(ctx)
	return f.forceLogoutErr
}

func (f *fakeWSAuthClient) KickTokens(context.Context, []string) error {
	f.kickTokensCalls++
	return nil
}

func (f *fakeWSAuthClient) InvalidateToken(context.Context, *pbAuth.InvalidateTokenReq) error {
	return nil
}

func TestEnforceLoginCity(t *testing.T) {
	newConnContext := func() *UserConnContext {
		query := url.Values{
			WsUserID:    {"user-1"},
			PlatformID:  {"2"},
			Token:       {"token-1"},
			OperationID: {"op-1"},
		}
		req := httptest.NewRequest(http.MethodGet, "http://gateway/?"+query.Encode(), nil)
		req.RemoteAddr = "198.51.100.30:4567"
		return newContext(httptest.NewRecorder(), req)
	}

	t.Run("checker failure is fail open", func(t *testing.T) {
		authClient := &fakeWSAuthClient{}
		ws := &WsServer{
			msgGatewayConfig: &Config{},
			authClient:       authClient,
			cityChecker:      &fakeLoginCityChecker{err: errors.New("chat unavailable")},
			clients:          newUserMap(),
		}
		if err := ws.enforceLoginCity(newConnContext()); err != nil {
			t.Fatalf("enforceLoginCity() error = %v", err)
		}
		if authClient.forceLogoutCalls != 0 || authClient.kickTokensCalls != 0 {
			t.Fatalf("unexpected auth calls: %+v", authClient)
		}
	})

	t.Run("mismatch force logs out platform and returns token kicked", func(t *testing.T) {
		authClient := &fakeWSAuthClient{}
		ws := &WsServer{
			msgGatewayConfig: &Config{Share: commonConfig.Share{IMAdminUserID: []string{"admin"}}},
			authClient:       authClient,
			cityChecker:      &fakeLoginCityChecker{result: &loginCityCheckResult{Allowed: false, Reason: "city_mismatch"}},
			clients:          newUserMap(),
		}
		err := ws.enforceLoginCity(newConnContext())
		if got := apiresp.ParseError(err).ErrCode; got != 1506 {
			t.Fatalf("error code = %d, want 1506 (err=%v)", got, err)
		}
		if authClient.forceLogoutCalls != 1 || authClient.kickTokensCalls != 0 {
			t.Fatalf("unexpected auth calls: %+v", authClient)
		}
		if authClient.forceLogoutUser != "user-1" || authClient.forceLogoutPID != 2 || authClient.forceLogoutAdmin != "admin" {
			t.Fatalf("unexpected force logout contract: %+v", authClient)
		}
	})

	t.Run("force logout failure invalidates current token", func(t *testing.T) {
		authClient := &fakeWSAuthClient{forceLogoutErr: errors.New("gateway unavailable")}
		ws := &WsServer{
			msgGatewayConfig: &Config{Share: commonConfig.Share{IMAdminUserID: []string{"admin"}}},
			authClient:       authClient,
			cityChecker:      &fakeLoginCityChecker{result: &loginCityCheckResult{Allowed: false}},
			clients:          newUserMap(),
		}
		if err := ws.enforceLoginCity(newConnContext()); err == nil {
			t.Fatal("enforceLoginCity() returned nil, want token kicked error")
		}
		if authClient.forceLogoutCalls != 1 || authClient.kickTokensCalls != 1 {
			t.Fatalf("unexpected auth calls: %+v", authClient)
		}
	})
}
