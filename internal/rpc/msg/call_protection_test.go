package msg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openimsdk/open-im-server/v3/pkg/common/chatapi"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	pbmsg "github.com/openimsdk/open-im-server/v3/protocol/msg"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/apiresp"
	"github.com/openimsdk/tools/mcontext"
)

type fakeCallProtectionChecker struct {
	protected   bool
	err         error
	calls       int
	userID      string
	operationID string
}

func (f *fakeCallProtectionChecker) HasProtection(_ context.Context, userID, operationID string) (bool, error) {
	f.calls++
	f.userID = userID
	f.operationID = operationID
	return f.protected, f.err
}

func callInviteMsg(content string) *sdkws.MsgData {
	return &sdkws.MsgData{
		SessionType: constant.SingleChatType,
		ContentType: constant.Custom,
		SendID:      "caller",
		RecvID:      "protected-user",
		Content:     []byte(content),
	}
}

func TestCheckCallInviteProtection(t *testing.T) {
	validInvite := `{"customType":200,"data":{"inviterUserID":"caller","inviteeUserIDList":["protected-user"],"roomID":"room-1"}}`

	t.Run("unprotected recipient is allowed", func(t *testing.T) {
		checker := &fakeCallProtectionChecker{}
		server := &msgServer{callProtectionChecker: checker}
		ctx := mcontext.SetOperationID(context.Background(), "op-1")
		if err := server.checkCallInviteProtection(ctx, callInviteMsg(validInvite)); err != nil {
			t.Fatal(err)
		}
		if checker.calls != 1 || checker.userID != "protected-user" || checker.operationID != "op-1" {
			t.Fatalf("unexpected checker call: %+v", checker)
		}
	})

	t.Run("protected recipient is rejected", func(t *testing.T) {
		server := &msgServer{callProtectionChecker: &fakeCallProtectionChecker{protected: true}}
		err := server.checkCallInviteProtection(context.Background(), callInviteMsg(validInvite))
		if got := apiresp.ParseError(err).ErrCode; got != 1208 {
			t.Fatalf("error code = %d, want 1208 (err=%v)", got, err)
		}
	})

	t.Run("Chat failure is fail closed", func(t *testing.T) {
		server := &msgServer{callProtectionChecker: &fakeCallProtectionChecker{err: errors.New("chat unavailable")}}
		err := server.checkCallInviteProtection(context.Background(), callInviteMsg(validInvite))
		if got := apiresp.ParseError(err).ErrCode; got != 500 {
			t.Fatalf("error code = %d, want 500 (err=%v)", got, err)
		}
	})

	t.Run("missing checker is fail closed", func(t *testing.T) {
		err := (&msgServer{}).checkCallInviteProtection(context.Background(), callInviteMsg(validInvite))
		if got := apiresp.ParseError(err).ErrCode; got != 500 {
			t.Fatalf("error code = %d, want 500 (err=%v)", got, err)
		}
	})

	invalidInvites := []struct {
		name    string
		content string
	}{
		{name: "missing data", content: `{"customType":200}`},
		{name: "malformed data", content: `{"customType":200,"data":"bad"}`},
		{name: "inviter mismatch", content: `{"customType":200,"data":{"inviterUserID":"other","inviteeUserIDList":["protected-user"]}}`},
		{name: "invitee mismatch", content: `{"customType":200,"data":{"inviterUserID":"caller","inviteeUserIDList":["other"]}}`},
		{name: "multiple invitees in single chat", content: `{"customType":200,"data":{"inviterUserID":"caller","inviteeUserIDList":["protected-user","other"]}}`},
	}
	for _, tt := range invalidInvites {
		t.Run(tt.name, func(t *testing.T) {
			checker := &fakeCallProtectionChecker{}
			server := &msgServer{callProtectionChecker: checker}
			err := server.checkCallInviteProtection(context.Background(), callInviteMsg(tt.content))
			if got := apiresp.ParseError(err).ErrCode; got != 1001 {
				t.Fatalf("error code = %d, want 1001 (err=%v)", got, err)
			}
			if checker.calls != 0 {
				t.Fatalf("checker called for invalid payload: %+v", checker)
			}
		})
	}

	t.Run("non-invite call signals are not blocked", func(t *testing.T) {
		checker := &fakeCallProtectionChecker{protected: true}
		server := &msgServer{callProtectionChecker: checker}
		msgData := callInviteMsg(`{"customType":201,"data":{}}`)
		if err := server.checkCallInviteProtection(context.Background(), msgData); err != nil {
			t.Fatal(err)
		}
		if checker.calls != 0 {
			t.Fatalf("checker called for customType 201: %+v", checker)
		}
	})

	t.Run("group invitation is outside the single-call policy", func(t *testing.T) {
		checker := &fakeCallProtectionChecker{protected: true}
		server := &msgServer{callProtectionChecker: checker}
		msgData := callInviteMsg(validInvite)
		msgData.SessionType = constant.ReadGroupChatType
		if err := server.checkCallInviteProtection(context.Background(), msgData); err != nil {
			t.Fatal(err)
		}
		if checker.calls != 0 {
			t.Fatalf("checker called for group invite: %+v", checker)
		}
	})

	t.Run("standard SDK custom wrapper is inspected", func(t *testing.T) {
		checker := &fakeCallProtectionChecker{protected: true}
		server := &msgServer{callProtectionChecker: checker}
		msgData := callInviteMsg(`{"data":"{\"customType\":200,\"data\":{\"inviterUserID\":\"caller\",\"inviteeUserIDList\":[\"protected-user\"]}}","extension":"","description":""}`)
		err := server.checkCallInviteProtection(context.Background(), msgData)
		if got := apiresp.ParseError(err).ErrCode; got != 1208 {
			t.Fatalf("error code = %d, want 1208 (err=%v)", got, err)
		}
	})

	t.Run("conflicting wrapper cannot disguise an invite", func(t *testing.T) {
		checker := &fakeCallProtectionChecker{}
		server := &msgServer{callProtectionChecker: checker}
		msgData := callInviteMsg(`{"customType":201,"data":"{\"customType\":200,\"data\":{\"inviterUserID\":\"caller\",\"inviteeUserIDList\":[\"protected-user\"]}}"}`)
		err := server.checkCallInviteProtection(context.Background(), msgData)
		if got := apiresp.ParseError(err).ErrCode; got != 1001 {
			t.Fatalf("error code = %d, want 1001 (err=%v)", got, err)
		}
		if checker.calls != 0 {
			t.Fatalf("checker called for conflicting wrapper: %+v", checker)
		}
	})

	t.Run("invalid outer type cannot hide a wrapped invite", func(t *testing.T) {
		server := &msgServer{callProtectionChecker: &fakeCallProtectionChecker{}}
		msgData := callInviteMsg(`{"customType":"invalid","data":"{\"customType\":200,\"data\":{\"inviterUserID\":\"caller\",\"inviteeUserIDList\":[\"protected-user\"]}}"}`)
		err := server.checkCallInviteProtection(context.Background(), msgData)
		if got := apiresp.ParseError(err).ErrCode; got != 1001 {
			t.Fatalf("error code = %d, want 1001 (err=%v)", got, err)
		}
	})
}

func TestPreflightSingleChatRejectsProtectedInviteSynchronously(t *testing.T) {
	checker := &fakeCallProtectionChecker{protected: true}
	server := &msgServer{callProtectionChecker: checker}
	req := &pbmsg.SendMsgReq{MsgData: callInviteMsg(
		`{"customType":"200","data":{"inviterUserID":"caller","inviteeUserIDList":["protected-user"]}}`,
	)}

	err := server.preflightSingleChatMsg(context.Background(), req)
	if got := apiresp.ParseError(err).ErrCode; got != 1208 {
		t.Fatalf("error code = %d, want 1208 (err=%v)", got, err)
	}
	if checker.calls != 1 {
		t.Fatalf("checker calls = %d, want 1", checker.calls)
	}
}

func TestChatCallProtectionCheckerContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != checkUserProtectionAPIPath {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("user_id"); got != "protected-user" {
			t.Errorf("user_id = %q", got)
		}
		if got := r.Header.Get(chatapi.InternalSecretHeader); got != "secret" {
			t.Errorf("internal secret header = %q", got)
		}
		if got := r.Header.Get("operationID"); got != "op-1" {
			t.Errorf("operationID = %q", got)
		}
		_, _ = w.Write([]byte(`{"errCode":0,"data":{"user_id":"protected-user","has_protection":true}}`))
	}))
	defer server.Close()

	checker := newChatCallProtectionChecker(server.URL, "secret")
	protected, err := checker.HasProtection(context.Background(), "protected-user", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if !protected {
		t.Fatal("HasProtection() = false, want true")
	}
}

func TestChatCallProtectionCheckerErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		response string
	}{
		{name: "HTTP failure", status: http.StatusServiceUnavailable, response: `{}`},
		{name: "business failure", status: http.StatusOK, response: `{"errCode":1001,"errMsg":"bad request"}`},
		{name: "malformed response", status: http.StatusOK, response: `{`},
		{name: "missing data", status: http.StatusOK, response: `{"errCode":0}`},
		{name: "missing protection field", status: http.StatusOK, response: `{"errCode":0,"data":{"user_id":"user"}}`},
		{name: "mismatched user", status: http.StatusOK, response: `{"errCode":0,"data":{"user_id":"other","has_protection":false}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			checker := newChatCallProtectionChecker(server.URL, "secret")
			if _, err := checker.HasProtection(context.Background(), "user", "op"); err == nil {
				t.Fatal("HasProtection() error = nil, want error")
			}
		})
	}
}
