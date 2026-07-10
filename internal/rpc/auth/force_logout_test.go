package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msggateway"
	"google.golang.org/grpc"
)

type fakeAuthDatabase struct {
	tokens   map[string]int
	getErr   error
	setErr   error
	setCalls int
}

func (f *fakeAuthDatabase) GetTokensWithoutError(context.Context, string, int) (map[string]int, error) {
	return f.tokens, f.getErr
}

func (f *fakeAuthDatabase) CreateToken(context.Context, string, int) (string, error) {
	return "", errors.New("not implemented")
}

func (f *fakeAuthDatabase) BatchSetTokenMapByUidPid(context.Context, []string) error {
	return errors.New("not implemented")
}

func (f *fakeAuthDatabase) SetTokenMapByUidPid(_ context.Context, _ string, _ int, tokens map[string]int) error {
	f.setCalls++
	f.tokens = tokens
	return f.setErr
}

type fakeAuthDiscovery struct {
	conns []grpc.ClientConnInterface
	err   error
}

func (f *fakeAuthDiscovery) GetConn(context.Context, string, ...grpc.DialOption) (grpc.ClientConnInterface, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeAuthDiscovery) GetConns(context.Context, string, ...grpc.DialOption) ([]grpc.ClientConnInterface, error) {
	return f.conns, f.err
}

func (f *fakeAuthDiscovery) IsSelfNode(grpc.ClientConnInterface) bool { return false }

func TestForceKickOffAggregatesGatewayErrorsAndInvalidatesTokens(t *testing.T) {
	database := &fakeAuthDatabase{tokens: map[string]int{
		"token-1": constant.NormalToken,
		"token-2": constant.NormalToken,
	}}
	discovery := &fakeAuthDiscovery{conns: []grpc.ClientConnInterface{nil, nil, nil}}
	kickCalls := 0
	server := &authServer{
		authDatabase:   database,
		RegisterCenter: discovery,
		config:         &Config{},
		kickGateway: func(_ context.Context, _ grpc.ClientConnInterface, _ *msggateway.KickUserOfflineReq) error {
			call := kickCalls
			kickCalls++
			if call == 0 || call == 2 {
				return errors.New("gateway unavailable")
			}
			return nil
		},
	}

	err := server.forceKickOff(context.Background(), "user-1", 2)
	if err == nil {
		t.Fatal("forceKickOff() returned nil, want aggregate gateway error")
	}
	if !strings.Contains(err.Error(), "message gateway 0") || !strings.Contains(err.Error(), "message gateway 2") {
		t.Fatalf("aggregate error missing gateway failures: %v", err)
	}
	if kickCalls != 3 {
		t.Fatalf("kick calls = %d, want 3", kickCalls)
	}
	if database.setCalls != 1 {
		t.Fatalf("token set calls = %d, want 1", database.setCalls)
	}
	for token, status := range database.tokens {
		if status != constant.KickedToken {
			t.Fatalf("token %s status = %d, want KickedToken", token, status)
		}
	}
}

func TestForceKickOffInvalidatesTokensWhenDiscoveryFails(t *testing.T) {
	database := &fakeAuthDatabase{tokens: map[string]int{"token-1": constant.NormalToken}}
	server := &authServer{
		authDatabase: database,
		RegisterCenter: &fakeAuthDiscovery{
			err: errors.New("discovery unavailable"),
		},
		config: &Config{},
	}

	err := server.forceKickOff(context.Background(), "user-1", 2)
	if err == nil || !strings.Contains(err.Error(), "discovery unavailable") {
		t.Fatalf("forceKickOff() error = %v, want discovery error", err)
	}
	if database.setCalls != 1 || database.tokens["token-1"] != constant.KickedToken {
		t.Fatalf("token was not invalidated after discovery failure: calls=%d tokens=%v", database.setCalls, database.tokens)
	}
}

func TestMarkPlatformTokensKickedWritesOnce(t *testing.T) {
	database := &fakeAuthDatabase{tokens: map[string]int{
		"token-1": constant.NormalToken,
		"token-2": constant.NormalToken,
	}}
	if err := markPlatformTokensKicked(context.Background(), database, "user-1", 2); err != nil {
		t.Fatal(err)
	}
	if database.setCalls != 1 {
		t.Fatalf("SetTokenMapByUidPid calls = %d, want 1", database.setCalls)
	}
}
