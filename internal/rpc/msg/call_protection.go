package msg

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/openimsdk/open-im-server/v3/pkg/common/chatapi"
	"github.com/openimsdk/open-im-server/v3/pkg/common/servererrs"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/mcontext"
)

const (
	callInviteCustomType       = 200
	checkUserProtectionAPIPath = "/third_admin/organization/internal/check_user_protection"
)

type callProtectionChecker interface {
	HasProtection(ctx context.Context, userID, operationID string) (bool, error)
}

type chatCallProtectionChecker struct {
	client *chatapi.Client
}

type checkUserProtectionData struct {
	UserID        string `json:"user_id"`
	HasProtection *bool  `json:"has_protection"`
}

type callInvitationData struct {
	InviterUserID     string   `json:"inviterUserID"`
	InviteeUserIDList []string `json:"inviteeUserIDList"`
}

func newChatCallProtectionChecker(chatAPIURL, internalSecret string) *chatCallProtectionChecker {
	return &chatCallProtectionChecker{client: chatapi.New(chatAPIURL, internalSecret)}
}

func (c *chatCallProtectionChecker) HasProtection(
	ctx context.Context,
	userID,
	operationID string,
) (bool, error) {
	resp, err := c.client.Do(
		ctx,
		http.MethodGet,
		checkUserProtectionAPIPath,
		url.Values{"user_id": []string{userID}},
		nil,
		operationID,
	)
	if err != nil {
		return false, err
	}
	if resp.ErrCode != 0 {
		return false, fmt.Errorf("check user protection returned errCode %d: %s %s", resp.ErrCode, resp.ErrMsg, resp.ErrDlt)
	}
	var data checkUserProtectionData
	if err := resp.DecodeData(&data); err != nil {
		return false, err
	}
	if data.UserID != userID || data.HasProtection == nil {
		return false, fmt.Errorf("check user protection returned incomplete or mismatched data")
	}
	return *data.HasProtection, nil
}

func (m *msgServer) checkCallInviteProtection(ctx context.Context, msgData *sdkws.MsgData) error {
	if msgData == nil || msgData.SessionType != constant.SingleChatType || msgData.ContentType != constant.Custom {
		return nil
	}
	custom, ok := parseCustomMessage(msgData.Content)
	if !ok {
		return nil
	}
	if custom.ConflictingWrapper {
		return errs.ErrArgs.WrapMsg("custom message wrapper type conflicts with payload type")
	}
	if custom.Type != callInviteCustomType {
		return nil
	}

	var invitation callInvitationData
	if len(custom.Data) == 0 || string(custom.Data) == "null" {
		return errs.ErrArgs.WrapMsg("call invitation data is empty")
	}
	if err := json.Unmarshal(custom.Data, &invitation); err != nil {
		return errs.ErrArgs.WrapMsg("call invitation data is invalid", "error", err.Error())
	}
	if invitation.InviterUserID == "" || invitation.InviterUserID != msgData.SendID {
		return errs.ErrArgs.WrapMsg("call invitation inviter does not match sendID")
	}
	if msgData.RecvID == "" || len(invitation.InviteeUserIDList) != 1 || invitation.InviteeUserIDList[0] != msgData.RecvID {
		return errs.ErrArgs.WrapMsg("call invitation invitee does not match recvID")
	}
	if m.callProtectionChecker == nil {
		log.ZError(ctx, "call protection checker is not configured", nil,
			"sendID", msgData.SendID, "recvID", msgData.RecvID)
		return servererrs.ErrInternalServer.WrapMsg("unable to verify call protection")
	}

	hasProtection, err := m.callProtectionChecker.HasProtection(ctx, msgData.RecvID, mcontext.GetOperationID(ctx))
	if err != nil {
		// Official-account protection is a security boundary. A dependency error
		// must reject only the new invitation rather than silently bypass policy.
		log.ZError(ctx, "check call recipient official protection failed", err,
			"sendID", msgData.SendID, "recvID", msgData.RecvID)
		return servererrs.ErrInternalServer.WrapMsg("unable to verify call protection")
	}
	if hasProtection {
		return servererrs.ErrOfficialAccountProtected.WrapMsg("recipient is protected from audio/video calls")
	}
	return nil
}
