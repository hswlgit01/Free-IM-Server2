package msggateway

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/common/chatapi"
)

const (
	checkWSLoginCityPath        = "/third_admin/organization/internal/check_ws_login_city"
	internalSecretHeader        = chatapi.InternalSecretHeader
	loginCityMismatchErrorCode  = 11007
	loginCityForceLogoutTimeout = 5 * time.Second
	maxLoginCityResponseSize    = chatapi.MaxResponseSize
)

type loginCityChecker interface {
	Check(ctx context.Context, userID, clientIP, operationID string) (*loginCityCheckResult, error)
}

type loginCityCheckResult struct {
	Allowed     bool
	Reason      string
	CurrentCity string
	BoundCity   string
}

type chatLoginCityChecker struct {
	client *chatapi.Client
}

type checkWSLoginCityRequest struct {
	UserID string `json:"user_id"`
	IP     string `json:"ip"`
}

type checkWSLoginCityData struct {
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason"`
	CurrentCity string `json:"current_city"`
	BoundCity   string `json:"bound_city"`
}

func newChatLoginCityChecker(chatAPIURL, internalSecret string) *chatLoginCityChecker {
	return &chatLoginCityChecker{
		client: chatapi.New(chatAPIURL, internalSecret),
	}
}

func (c *chatLoginCityChecker) Check(
	ctx context.Context,
	userID,
	clientIP,
	operationID string,
) (*loginCityCheckResult, error) {
	resp, err := c.client.Do(
		ctx,
		http.MethodPost,
		checkWSLoginCityPath,
		nil,
		checkWSLoginCityRequest{UserID: userID, IP: clientIP},
		operationID,
	)
	if err != nil {
		return nil, err
	}

	if resp.ErrCode == loginCityMismatchErrorCode {
		decision := &loginCityCheckResult{Allowed: false}
		var data checkWSLoginCityData
		if err := resp.DecodeData(&data); err == nil {
			decision.Reason = data.Reason
			decision.CurrentCity = data.CurrentCity
			decision.BoundCity = data.BoundCity
		}
		return decision, nil
	}
	if resp.ErrCode != 0 {
		return nil, fmt.Errorf("websocket login city endpoint returned errCode %d: %s %s", resp.ErrCode, resp.ErrMsg, resp.ErrDlt)
	}

	var data checkWSLoginCityData
	if err := resp.DecodeData(&data); err != nil {
		return nil, err
	}
	if !data.Allowed {
		return nil, fmt.Errorf("websocket login city endpoint returned errCode 0 with allowed=false")
	}

	return &loginCityCheckResult{
		Allowed:     true,
		Reason:      data.Reason,
		CurrentCity: data.CurrentCity,
		BoundCity:   data.BoundCity,
	}, nil
}

// clientIPFromRequest returns the address the reverse proxy identified as the
// client, without a source port. Forwarding headers are ignored for direct
// public peers. For trusted local/private proxies, X-Real-IP is preferred
// because the deployment proxy overwrites it; X-Forwarded-For is walked from
// right to left because proxy_add_x_forwarded_for preserves spoofed prefixes.
func clientIPFromRequest(req *http.Request) string {
	if req == nil {
		return ""
	}

	peerIP := normalizeClientIP(req.RemoteAddr)
	// Forwarded headers are only meaningful when the immediate peer is a local
	// or private-network reverse proxy. A public client connecting directly to
	// msggateway must not be able to spoof its city with these headers.
	if isTrustedProxyPeer(peerIP) {
		if realIP := normalizeClientIP(req.Header.Get("X-Real-IP")); realIP != "" {
			return realIP
		}
		if forwardedIP := clientIPFromForwardedFor(req.Header.Get("X-Forwarded-For")); forwardedIP != "" {
			return forwardedIP
		}
	}
	return peerIP
}

func clientIPFromForwardedFor(value string) string {
	parts := strings.Split(value, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := normalizeClientIP(parts[i])
		if ip == "" || isTrustedProxyPeer(ip) {
			continue
		}
		return ip
	}
	return ""
}

func isTrustedProxyPeer(value string) bool {
	ip := net.ParseIP(value)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func normalizeClientIP(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if value == "" {
		return ""
	}

	if ip := net.ParseIP(strings.Trim(value, "[]")); ip != nil {
		return ip.String()
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			return ip.String()
		}
	}
	return ""
}
