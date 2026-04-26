package msg

import (
	"encoding/json"
	"testing"

	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
)

func TestRevokeTipsBackwardCompat(t *testing.T) {
	source := &sdkws.MsgData{
		ClientMsgID: "client-1",
		SessionType: constant.SingleChatType,
	}
	tips := buildRevokeMsgTips(
		"admin-1",
		"single_user-a_user-b",
		source,
		42,
		1700000000000,
		[]string{"admin-1"},
	)

	data, err := json.Marshal(tips)
	if err != nil {
		t.Fatalf("marshal RevokeMsgTips: %v", err)
	}

	var legacy struct {
		RevokerUserID  string `json:"revokerUserID"`
		ClientMsgID    string `json:"clientMsgID"`
		RevokeTime     int64  `json:"revokeTime"`
		SesstionType   int32  `json:"sesstionType"`
		Seq            int64  `json:"seq"`
		ConversationID string `json:"conversationID"`
		IsAdminRevoke  bool   `json:"isAdminRevoke"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatalf("unmarshal legacy revoke tips shape: %v", err)
	}

	if legacy.RevokerUserID != "admin-1" {
		t.Fatalf("revokerUserID mismatch: %q", legacy.RevokerUserID)
	}
	if legacy.ClientMsgID != "client-1" {
		t.Fatalf("clientMsgID mismatch: %q", legacy.ClientMsgID)
	}
	if legacy.RevokeTime != 1700000000000 {
		t.Fatalf("revokeTime mismatch: %d", legacy.RevokeTime)
	}
	if legacy.SesstionType != constant.SingleChatType {
		t.Fatalf("sesstionType mismatch: %d", legacy.SesstionType)
	}
	if legacy.Seq != 42 {
		t.Fatalf("seq mismatch: %d", legacy.Seq)
	}
	if legacy.ConversationID != "single_user-a_user-b" {
		t.Fatalf("conversationID mismatch: %q", legacy.ConversationID)
	}
	if !legacy.IsAdminRevoke {
		t.Fatal("isAdminRevoke should remain available for legacy SDKs")
	}

	var incompatible struct {
		RevokerID   string `json:"revokerID"`
		SessionType int32  `json:"sessionType"`
	}
	if err := json.Unmarshal(data, &incompatible); err != nil {
		t.Fatalf("unmarshal incompatible shape: %v", err)
	}
	if incompatible.RevokerID != "" || incompatible.SessionType != 0 {
		t.Fatalf("revoke notification unexpectedly used MessageRevokedContent shape: %+v", incompatible)
	}
}
