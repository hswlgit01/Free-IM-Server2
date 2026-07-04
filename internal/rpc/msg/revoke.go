// Copyright © 2023 OpenIM. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package msg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/model"

	"github.com/openimsdk/open-im-server/v3/pkg/authverify"
	"github.com/openimsdk/open-im-server/v3/pkg/common/servererrs"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msg"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/utils/datautil"
)

func (m *msgServer) RevokeMsg(ctx context.Context, req *msg.RevokeMsgReq) (*msg.RevokeMsgResp, error) {
	if req.UserID == "" {
		return nil, errs.ErrArgs.WrapMsg("user_id is empty")
	}
	if req.ConversationID == "" {
		return nil, errs.ErrArgs.WrapMsg("conversation_id is empty")
	}
	if req.Seq < 0 {
		return nil, errs.ErrArgs.WrapMsg("seq is invalid")
	}
	if err := authverify.CheckAccessV3(ctx, req.UserID, m.config.Share.IMAdminUserID); err != nil {
		return nil, err
	}
	user, err := m.UserLocalCache.GetUserInfo(ctx, req.UserID)
	if err != nil {
		return nil, err
	}
	// dawn 2026-07-04 修复"管理员撤不回别人旧消息"：原先用 GetMsgBySeqs 会把 seq 夹在撤回者自己的
	// userMinSeq/userMaxSeq 内，群主/管理员若入群较晚（个人 minSeq 高），撤更早的消息会被过滤成
	// "msg not found"。改用 GetMsgForRevoke 按会话 seq 直取，权限仍由下方群角色判断把关。
	revokeMsg, err := m.MsgDatabase.GetMsgForRevoke(ctx, req.ConversationID, req.UserID, req.Seq)
	if err != nil {
		return nil, err
	}
	if revokeMsg == nil {
		return nil, errs.ErrRecordNotFound.WrapMsg("msg not found")
	}
	if revokeMsg.ContentType == constant.MsgRevokeNotification {
		return nil, servererrs.ErrMsgAlreadyRevoke.WrapMsg("msg already revoke")
	}

	data, _ := json.Marshal(revokeMsg)
	log.ZDebug(ctx, "GetMsgForRevoke", "conversationID", req.ConversationID, "seq", req.Seq, "msg", string(data))
	var role int32
	if !authverify.IsAppManagerUid(ctx, m.config.Share.IMAdminUserID) {
		sessionType := revokeMsg.SessionType
		switch sessionType {
		case constant.SingleChatType:
			if err := authverify.CheckAccessV3(ctx, revokeMsg.SendID, m.config.Share.IMAdminUserID); err != nil {
				return nil, err
			}
			role = user.AppMangerLevel
		case constant.ReadGroupChatType:
			members, err := m.GroupLocalCache.GetGroupMemberInfoMap(ctx, revokeMsg.GroupID, datautil.Distinct([]string{req.UserID, revokeMsg.SendID}))
			if err != nil {
				return nil, err
			}
			if req.UserID != revokeMsg.SendID {
				switch members[req.UserID].RoleLevel {
				case constant.GroupOwner:
				case constant.GroupAdmin:
					if sendMember, ok := members[revokeMsg.SendID]; ok {
						if sendMember.RoleLevel != constant.GroupOrdinaryUsers {
							return nil, errs.ErrNoPermission.WrapMsg("no permission")
						}
					}
				default:
					return nil, errs.ErrNoPermission.WrapMsg("no permission")
				}
			}
			if member := members[req.UserID]; member != nil {
				role = member.RoleLevel
			}
		default:
			return nil, errs.ErrInternalServer.WrapMsg("msg sessionType not supported", "sessionType", sessionType)
		}
	}
	now := time.Now().UnixMilli()
	err = m.MsgDatabase.RevokeMsg(ctx, req.ConversationID, req.Seq, &model.RevokeModel{
		Role:     role,
		UserID:   req.UserID,
		Nickname: user.Nickname,
		Time:     now,
	})
	if err != nil {
		return nil, err
	}
	tips := buildRevokeMsgTips(req.UserID, req.ConversationID, revokeMsg, req.Seq, now, m.config.Share.IMAdminUserID)
	var recvID string
	if revokeMsg.SessionType == constant.ReadGroupChatType {
		recvID = revokeMsg.GroupID
	} else {
		recvID = revokeMsg.RecvID
	}
	m.notificationSender.NotificationWithSessionType(ctx, req.UserID, recvID, constant.MsgRevokeNotification, revokeMsg.SessionType, &tips)
	m.webhookAfterRevokeMsg(ctx, &m.config.WebhooksConfig.AfterRevokeMsg, req)
	return &msg.RevokeMsgResp{}, nil
}

func buildRevokeMsgTips(revokerUserID, conversationID string, source *sdkws.MsgData, seq, revokeTime int64, adminUserIDs []string) sdkws.RevokeMsgTips {
	return sdkws.RevokeMsgTips{
		RevokerUserID:  revokerUserID,
		ClientMsgID:    source.ClientMsgID,
		RevokeTime:     revokeTime,
		Seq:            seq,
		SesstionType:   source.SessionType,
		ConversationID: conversationID,
		IsAdminRevoke:  len(adminUserIDs) > 0 && datautil.Contain(revokerUserID, adminUserIDs...),
	}
}
