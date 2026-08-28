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
	"os"
	"strings"
	"context"
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
	// dawn 2026-05-09 修复群消息删除实时通知：单聊自发自收仅禁止普通消息，删除等系统通知需允许定向推送给本人。
	isSingleSelfChat := data.MsgData.SessionType == constant.SingleChatType && data.MsgData.SendID == data.MsgData.RecvID
	isNotification := data.MsgData.ContentType >= constant.NotificationBegin && data.MsgData.ContentType <= constant.NotificationEnd
	if isSingleSelfChat && !isNotification {
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
		// 单聊的收发权限判断统一放在 singleChatSendAllowed 里。
		// 之前这段逻辑在这里和 preflightSingleChatMsg 里各有一份，
		// 结果修好友校验时只改到了这一份 —— 而单聊真正决定成败的是那份同步预检
		// （这里是异步 goroutine 里跑的，错误返回不到客户端）。合并成一处，杜绝再次漂移。
		if err := m.singleChatSendAllowed(ctx, data.MsgData.SendID, data.MsgData.RecvID); err != nil {
			return err
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
	custom, ok := parseCustomMessage(content)
	if !ok {
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
	switch custom.Type {
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

// isPrivilegedOrgRole reports whether an OrgRole should bypass the
// stranger/friend check when sending a single-chat message. Roles kept in sync
// with chat svc `organization_user.go`: SuperAdmin, BackendAdmin, GroupManager,
// TermManager (团队长), Normal. "Normal" and unset values are NOT privileged.
func isPrivilegedOrgRole(role string) bool {
	switch role {
	case "SuperAdmin", "BackendAdmin", "GroupManager", "TermManager":
		return true
	default:
		return false
	}
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

// privilegedBypassAllows 判断本次单聊是否可豁免好友校验。
//
// 由环境变量 PRIVILEGED_ROLE_FRIEND_BYPASS 控制，取值 none(默认) / sender / both，
// 语义见调用处注释。默认 none：特权角色也需为好友，符合「解除好友即不能聊天」的预期。
// singleChatSendAllowed 判断一条单聊消息是否允许发出。
//
// 【为什么要单独抽出来】这段逻辑原本在 messageVerification 和
// preflightSingleChatMsg 里各写了一份。单聊的 messageVerification 是在异步
// goroutine 里跑的，错误返回不到客户端；真正决定客户端成败的是同步的
// preflightSingleChatMsg。两份一旦不同步，就会出现「改了校验却毫无效果」
// —— 修双向好友校验时就正好踩了这个坑。合并成一处。
func (m *msgServer) singleChatSendAllowed(ctx context.Context, sendID, recvID string) error {
	u, err := m.UserLocalCache.GetUserInfo(ctx, sendID)
	if err != nil {
		return err
	}
	recv, err := m.UserLocalCache.GetUserInfo(ctx, recvID)
	if err != nil {
		return err
	}
	// 系统账号与「消息自由发送」用户不受好友关系限制
	if authverify.CheckSystemAccount(ctx, u.AppMangerLevel) ||
		u.CanSendFreeMsg == constant.MessageFreeLevel ||
		recv.CanSendFreeMsg == constant.MessageFreeLevel {
		return nil
	}
	// 特权组织角色（SuperAdmin / BackendAdmin / GroupManager / TermManager）
	// 对好友校验的豁免范围，由 PRIVILEGED_ROLE_FRIEND_BYPASS 控制。
	//
	// dawn 2026-08-28 客户反馈「解除好友后仍能继续聊天」属 BUG，故默认改为 none。
	//
	// 【为什么做成开关】这条判断已经来回调整两次，且三种模式各自会牺牲一些东西：
	//   both   —— 原行为：收发任一方为特权角色即双方免校验。
	//             管理员可主动触达，用户也能回复；但解除好友后仍能聊。
	//   sender —— 仅发送方特权时放行。用户无法主动私聊非好友的管理员，
	//             也无法回复管理员发来的消息（会话变单向），客服流程会受影响。
	//   none   —— 默认。特权角色同样需要是好友才能私聊。
	//             代价：管理员无法主动联系非好友用户。
	if privilegedBypassAllows(u.OrgRole, recv.OrgRole) {
		return nil
	}
	black, err := m.FriendLocalCache.IsBlack(ctx, sendID, recvID)
	if err != nil {
		return err
	}
	if black {
		return servererrs.ErrBlockedByPeer.Wrap()
	}
	if !m.config.RpcConfig.FriendVerify {
		return nil
	}
	// 【为什么要求双向好友】客户反馈「业务员添加了好友，解除好友以后，
	// 还是可以继续聊天」。根因是好友记录有方向，而两处判断对不上：
	//
	//   IsFriend(sendID, recvID) 问的是「发送方是否在接收方的好友列表里」，
	//   而 DeleteFriend 只删发起方一侧的记录（按 owner_user_id 过滤）。
	//
	// 于是业务员把用户删掉后，用户列表里还留着业务员，校验依然通过 ——
	// 谁删的好友，谁反而还能继续发。要求两个方向都成立，
	// 「解除好友」才对双方同时生效。
	//
	// 开关 FRIEND_VERIFY_MUTUAL=off 可退回原来的单边判断。
	var friend bool
	if mutualFriendRequired() {
		friend, err = m.FriendLocalCache.IsMutualFriend(ctx, sendID, recvID)
	} else {
		friend, err = m.FriendLocalCache.IsFriend(ctx, sendID, recvID)
	}
	if err != nil {
		return err
	}
	if !friend {
		return servererrs.ErrNotPeersFriend.Wrap()
	}
	return nil
}

// mutualFriendRequired 私聊是否要求双向好友。
//
// 默认开启：客户明确要求「解除好友后不能再聊天」，而单边判断做不到这一点。
// 留开关是因为这会收紧现有行为——若某个部署依赖「对方删了我，我还能发」
// 的旧语义，可用 FRIEND_VERIFY_MUTUAL=off 退回。
func mutualFriendRequired() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FRIEND_VERIFY_MUTUAL"))) {
	case "off", "false", "0":
		return false
	default:
		return true
	}
}

func privilegedBypassAllows(senderRole, recvRole string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PRIVILEGED_ROLE_FRIEND_BYPASS"))) {
	case "both":
		return isPrivilegedOrgRole(senderRole) || isPrivilegedOrgRole(recvRole)
	case "sender":
		return isPrivilegedOrgRole(senderRole)
	default: // none
		return false
	}
}
