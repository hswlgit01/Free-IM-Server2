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
	"math/rand"
	"strconv"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/authverify"
	"github.com/openimsdk/open-im-server/v3/pkg/common/servererrs"
	thirdModel "github.com/openimsdk/open-im-server/v3/third/model"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/utils/datautil"
	"github.com/openimsdk/tools/utils/encrypt"
	"github.com/openimsdk/tools/utils/timeutil"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msg"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/errs"
)

var ExcludeContentType = []int{constant.HasReadReceipt}

type Validator interface {
	validate(pb *msg.SendMsgReq) (bool, int32, string)
}

type MessageRevoked struct {
	RevokerID                   string `json:"revokerID"`
	RevokerRole                 int32  `json:"revokerRole"`
	ClientMsgID                 string `json:"clientMsgID"`
	RevokerNickname             string `json:"revokerNickname"`
	RevokeTime                  int64  `json:"revokeTime"`
	SourceMessageSendTime       int64  `json:"sourceMessageSendTime"`
	SourceMessageSendID         string `json:"sourceMessageSendID"`
	SourceMessageSenderNickname string `json:"sourceMessageSenderNickname"`
	SessionType                 int32  `json:"sessionType"`
	Seq                         uint32 `json:"seq"`
}

func (m *msgServer) messageVerification(ctx context.Context, data *msg.SendMsgReq) error {
	// 屏蔽 imAdmin（及配置中的 IMAdminUserID）发出的消息，不写入、不推送，避免本地/试运行收到系统账号消息
	if datautil.Contain(data.MsgData.SendID, m.config.Share.IMAdminUserID...) {
		// Exception: group system notifications (e.g. group name update) must be delivered to apps.
		// Otherwise, app won't receive updates when ops are performed via IMAdmin/service account.
		if data.MsgData.ContentType >= constant.GroupNotificationBegin && data.MsgData.ContentType <= constant.GroupInfoSetNameNotification {
			return nil
		}
		return errs.ErrNoPermission.WrapMsg("messages from imAdmin are disabled and will not be sent")
	}
	// 单聊禁止自己给自己发消息，不该存在此类记录
	if data.MsgData.SessionType == constant.SingleChatType && data.MsgData.SendID == data.MsgData.RecvID {
		return errs.ErrNoPermission.WrapMsg("self-to-self messages are not allowed")
	}
	// 组织角色：发送文件、发送名片（单聊/群聊均校验发送方）
	if data.MsgData.SessionType == constant.SingleChatType || data.MsgData.SessionType == constant.ReadGroupChatType {
		if err := m.checkOrgContentSendPermission(ctx, data.MsgData); err != nil {
			return err
		}
	}
	switch data.MsgData.SessionType {
	case constant.SingleChatType:
		if data.MsgData.ContentType >= constant.NotificationBegin &&
			data.MsgData.ContentType <= constant.NotificationEnd {
			return nil
		}
		if err := m.webhookBeforeSendSingleMsg(ctx, &m.config.WebhooksConfig.BeforeSendSingleMsg, data); err != nil {
			return err
		}
		u, err := m.UserLocalCache.GetUserInfo(ctx, data.MsgData.SendID)
		recv, err := m.UserLocalCache.GetUserInfo(ctx, data.MsgData.RecvID)
		if err != nil {
			return err
		}
		// 检查系统账号权限或消息自由发送权限
		if authverify.CheckSystemAccount(ctx, u.AppMangerLevel) || u.CanSendFreeMsg == constant.MessageFreeLevel || recv.CanSendFreeMsg == constant.MessageFreeLevel {
			return nil
		}
		black, err := m.FriendLocalCache.IsBlack(ctx, data.MsgData.SendID, data.MsgData.RecvID)
		if err != nil {
			return err
		}
		if black {
			return servererrs.ErrBlockedByPeer.Wrap()
		}
		// 单聊自发自收已在函数开头统一拒绝，此处仅作冗余校验
		if m.config.RpcConfig.FriendVerify {
			friend, err := m.FriendLocalCache.IsFriend(ctx, data.MsgData.SendID, data.MsgData.RecvID)
			if err != nil {
				return err
			}
			if !friend {
				return servererrs.ErrNotPeersFriend.Wrap()
			}
			return nil
		}
		return nil
	case constant.ReadGroupChatType:
		groupInfo, err := m.GroupLocalCache.GetGroupInfo(ctx, data.MsgData.GroupID)
		if err != nil {
			return err
		}
		if groupInfo.Status == constant.GroupStatusDismissed &&
			data.MsgData.ContentType != constant.GroupDismissedNotification {
			return servererrs.ErrDismissedAlready.Wrap()
		}
		if groupInfo.GroupType == constant.SuperGroup {
			return nil
		}

		if data.MsgData.ContentType >= constant.NotificationBegin &&
			data.MsgData.ContentType <= constant.NotificationEnd {
			return nil
		}
		memberIDs, err := m.GroupLocalCache.GetGroupMemberIDMap(ctx, data.MsgData.GroupID)
		if err != nil {
			return err
		}
		if _, ok := memberIDs[data.MsgData.SendID]; !ok {
			return servererrs.ErrNotInGroupYet.Wrap()
		}

		groupMemberInfo, err := m.GroupLocalCache.GetGroupMember(ctx, data.MsgData.GroupID, data.MsgData.SendID)
		if err != nil {
			if errs.ErrRecordNotFound.Is(err) {
				return servererrs.ErrNotInGroupYet.WrapMsg(err.Error())
			}
			return err
		}
		if groupMemberInfo.RoleLevel == constant.GroupOwner {
			return nil
		} else {
			if groupMemberInfo.MuteEndTime >= time.Now().UnixMilli() {
				return servererrs.ErrMutedInGroup.Wrap()
			}
			if groupInfo.Status == constant.GroupStatusMuted && groupMemberInfo.RoleLevel != constant.GroupAdmin {
				return servererrs.ErrMutedGroup.Wrap()
			}
		}
		return nil
	default:
		return nil
	}
}

// needsUnreadCountExclusion checks if a custom message should be excluded from unread count
// based on its customType field
func needsUnreadCountExclusion(content []byte) bool {
	// Parse the custom message content to extract customType
	var customData map[string]interface{}
	if err := json.Unmarshal(content, &customData); err != nil {
		// Log parsing error with content preview
		contentPreview := string(content)
		if len(contentPreview) > 200 {
			contentPreview = contentPreview[:200] + "..."
		}
		// Note: removed debug log, parse error is expected for non-JSON content
		return false
	}

	// Check if content is nested (e.g., {"data": "{\"customType\": 200}"} or {"detail": "..."})
	// Common nested field names: "data", "detail", "content"
	originalKeys := make([]string, 0, len(customData))
	for k := range customData {
		originalKeys = append(originalKeys, k)
	}

	for _, fieldName := range []string{"data", "detail", "content"} {
		if field, ok := customData[fieldName]; ok {
			if fieldStr, isString := field.(string); isString {
				// Try to parse the nested JSON string
				var innerData map[string]interface{}
				if err := json.Unmarshal([]byte(fieldStr), &innerData); err == nil {
					// Successfully parsed nested content, use it
					customData = innerData
					break
				}
			}
		}
	}

	customType, ok := customData["customType"]
	if !ok {
		// CustomType not found - this is normal for non-call messages
		return false
	}

	// Convert customType to int (might be float64 from JSON or string)
	var typeInt int
	switch v := customType.(type) {
	case float64:
		typeInt = int(v)
	case int:
		typeInt = v
	case string:
		// Handle string customType (e.g., "200")
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return false
		}
		typeInt = parsed
	default:
		return false
	}

	// Custom message types that should NOT count as unread:
	// - 200-204: Call signaling (invite, accept, reject, cancel, hangup) - real-time control, no notification needed
	// - 2005: Sync call status - internal sync message
	// - 910-913: System notifications (blocked, deleted, removed from group, group disbanded)
	// - 500: Refund notification (product decision: exclude)
	//
	// Note: 901 (call record) is NOT in this list - it SHOULD count as unread
	// because users need to know about missed calls
	switch typeInt {
	case 200, 201, 202, 203, 204: // Call signaling - exclude from unread
		return true
	case 2005: // Sync call status - exclude from unread
		return true
	case 910, 911, 912, 913: // System notifications - exclude from unread
		return true
	case 500: // Refund notification - exclude from unread
		return true
	default:
		return false // Including 901 - will count as unread
	}
}

func (m *msgServer) encapsulateMsgData(msg *sdkws.MsgData) {
	log.ZDebug(context.Background(), "encapsulateMsgData called", "contentType", msg.ContentType, "sendID", msg.SendID)

	msg.ServerMsgID = GetMsgID(msg.SendID)
	if msg.SendTime == 0 {
		msg.SendTime = timeutil.GetCurrentTimestampByMill()
	}
	switch msg.ContentType {
	case constant.Text, constant.Picture, constant.Voice, constant.Video,
		constant.File, constant.AtText, constant.Merger, constant.Card,
		constant.Location, constant.Quote, constant.AdvancedText, constant.MarkdownText:
	case constant.Custom:
		// 限制自定义消息（如红包）Content 大小，避免 5000 人群全员推送时单条消息过大导致 websocket close 1009 (message too big)
		const maxCustomContentSize = 512 * 1024 // 512KB，客户端常见读缓冲远小于此
		if origLen := len(msg.Content); origLen > maxCustomContentSize {
			msg.Content = msg.Content[:maxCustomContentSize]
			log.ZWarn(context.Background(), "Custom message content trimmed to avoid push overflow", nil, "contentType", msg.ContentType, "originalLen", origLen, "maxLen", maxCustomContentSize)
		}
		// 确保Options已初始化
		if msg.Options == nil {
			msg.Options = make(map[string]bool, 10)
		}
		// 自定义信令消息(如语音/视频通话)不应同步给发送者
		// 避免自我会话查询错误
		datautil.SetSwitchFromOptions(msg.Options, constant.IsSenderSync, false)
		// 检查是否需要排除未读计数
		// 包括通话信令(200-204,2005)、系统通知(910-913)、退款通知(500)等
		if len(msg.Content) > 0 {
			shouldExclude := needsUnreadCountExclusion(msg.Content)
			if shouldExclude {
				datautil.SetSwitchFromOptions(msg.Options, constant.IsUnreadCount, false)
				datautil.SetSwitchFromOptions(msg.Options, constant.IsOfflinePush, false)
			}
		}
	case constant.Revoke:
		datautil.SetSwitchFromOptions(msg.Options, constant.IsUnreadCount, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsOfflinePush, false)
	case constant.HasReadReceipt:
		datautil.SetSwitchFromOptions(msg.Options, constant.IsConversationUpdate, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsSenderConversationUpdate, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsUnreadCount, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsOfflinePush, false)
	case constant.Typing:
		datautil.SetSwitchFromOptions(msg.Options, constant.IsHistory, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsPersistent, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsSenderSync, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsConversationUpdate, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsSenderConversationUpdate, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsUnreadCount, false)
		datautil.SetSwitchFromOptions(msg.Options, constant.IsOfflinePush, false)
	}
}

// checkOrgContentSendPermission 按 organization_role_permission 校验发送文件、名片（无 org 或未配置库则跳过）
func (m *msgServer) checkOrgContentSendPermission(ctx context.Context, msgData *sdkws.MsgData) error {
	if m.mongoDatabase == nil {
		return nil
	}
	var perm thirdModel.PermissionCode
	switch msgData.ContentType {
	case constant.File, constant.Picture, constant.Video, constant.Voice:
		perm = thirdModel.PermissionCodeSendFile
	case constant.Card:
		perm = thirdModel.PermissionCodeSendBusinessCard
	default:
		return nil
	}
	if msgData.ContentType >= constant.NotificationBegin && msgData.ContentType <= constant.NotificationEnd {
		return nil
	}
	if datautil.Contain(msgData.SendID, m.config.Share.IMAdminUserID...) {
		return nil
	}
	sender, err := m.UserLocalCache.GetUserInfo(ctx, msgData.SendID)
	if err != nil {
		return err
	}
	if sender.GetOrgId() == "" {
		return nil
	}
	orgID, err := primitive.ObjectIDFromHex(sender.GetOrgId())
	if err != nil {
		return nil
	}
	dao := thirdModel.NewOrganizationRolePermissionDao(m.mongoDatabase)
	ok, err := dao.ExistPermission(ctx, orgID, thirdModel.OrganizationUserRole(sender.GetOrgRole()), perm)
	if err != nil {
		return err
	}
	if !ok {
		return errs.ErrNoPermission.WrapMsg("no org permission")
	}
	return nil
}

func GetMsgID(sendID string) string {
	t := timeutil.GetCurrentTimeFormatted()
	return encrypt.Md5(t + "-" + sendID + "-" + strconv.Itoa(rand.Int()))
}

func (m *msgServer) modifyMessageByUserMessageReceiveOpt(ctx context.Context, userID, conversationID string, sessionType int, pb *msg.SendMsgReq) (bool, error) {
	opt, err := m.UserLocalCache.GetUserGlobalMsgRecvOpt(ctx, userID)
	if err != nil {
		return false, err
	}
	switch opt {
	case constant.ReceiveMessage:
	case constant.NotReceiveMessage:
		return false, nil
	case constant.ReceiveNotNotifyMessage:
		if pb.MsgData.Options == nil {
			pb.MsgData.Options = make(map[string]bool, 10)
		}
		datautil.SetSwitchFromOptions(pb.MsgData.Options, constant.IsOfflinePush, false)
		return true, nil
	}
	singleOpt, err := m.ConversationLocalCache.GetSingleConversationRecvMsgOpt(ctx, userID, conversationID)
	if errs.ErrRecordNotFound.Is(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	switch singleOpt {
	case constant.ReceiveMessage:
		return true, nil
	case constant.NotReceiveMessage:
		if datautil.Contain(int(pb.MsgData.ContentType), ExcludeContentType...) {
			return true, nil
		}
		return false, nil
	case constant.ReceiveNotNotifyMessage:
		if pb.MsgData.Options == nil {
			pb.MsgData.Options = make(map[string]bool, 10)
		}
		datautil.SetSwitchFromOptions(pb.MsgData.Options, constant.IsOfflinePush, false)
		return true, nil
	}
	return true, nil
}
