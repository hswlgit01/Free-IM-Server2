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

package msgprocessor

import (
	"sort"
	"strings"

	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/errs"
	"google.golang.org/protobuf/proto"
)

func IsGroupConversationID(conversationID string) bool {
	return strings.HasPrefix(conversationID, "g_") || strings.HasPrefix(conversationID, "sg_")
}

func GetNotificationConversationIDByMsg(msg *sdkws.MsgData) string {
	switch msg.SessionType {
	case constant.SingleChatType:
		l := []string{msg.SendID, msg.RecvID}
		sort.Strings(l)
		return "n_" + strings.Join(l, "_")
	case constant.WriteGroupChatType:
		return "n_" + msg.GroupID
	case constant.ReadGroupChatType:
		return "n_" + msg.GroupID
	case constant.NotificationChatType:
		l := []string{msg.SendID, msg.RecvID}
		sort.Strings(l)
		return "n_" + strings.Join(l, "_")
	}
	return ""
}

func GetChatConversationIDByMsg(msg *sdkws.MsgData) string {
	switch msg.SessionType {
	case constant.SingleChatType:
		l := []string{msg.SendID, msg.RecvID}
		sort.Strings(l)
		return "si_" + strings.Join(l, "_")
	case constant.WriteGroupChatType:
		return "g_" + msg.GroupID
	case constant.ReadGroupChatType:
		return "sg_" + msg.GroupID
	case constant.NotificationChatType:
		l := []string{msg.SendID, msg.RecvID}
		sort.Strings(l)
		return "sn_" + strings.Join(l, "_")
	}

	return ""
}

func GetConversationIDByMsg(msg *sdkws.MsgData) string {
	options := Options(msg.Options)
	switch msg.SessionType {
	case constant.SingleChatType:
		l := []string{msg.SendID, msg.RecvID}
		sort.Strings(l)
		if !options.IsNotNotification() {
			return "n_" + strings.Join(l, "_")
		}
		return "si_" + strings.Join(l, "_") // single chat
	case constant.WriteGroupChatType:
		if !options.IsNotNotification() {
			return "n_" + msg.GroupID // group chat
		}
		return "g_" + msg.GroupID // group chat
	case constant.ReadGroupChatType:
		if !options.IsNotNotification() {
			return "n_" + msg.GroupID // super group chat
		}
		return "sg_" + msg.GroupID // super group chat
	case constant.NotificationChatType:
		l := []string{msg.SendID, msg.RecvID}
		sort.Strings(l)
		if !options.IsNotNotification() {
			return "n_" + strings.Join(l, "_")
		}
		return "sn_" + strings.Join(l, "_")
	}
	return ""
}

func GetConversationIDBySessionType(sessionType int, ids ...string) string {
	sort.Strings(ids)
	if len(ids) > 2 || len(ids) < 1 {
		return ""
	}
	switch sessionType {
	case constant.SingleChatType:
		return "si_" + strings.Join(ids, "_") // single chat
	case constant.WriteGroupChatType:
		return "g_" + ids[0] // group chat
	case constant.ReadGroupChatType:
		return "sg_" + ids[0] // super group chat
	case constant.NotificationChatType:
		return "sn_" + ids[0] // server notification chat
	}
	return ""
}

func IsNotification(conversationID string) bool {
	return strings.HasPrefix(conversationID, "n_")
}

func IsNotificationByMsg(msg *sdkws.MsgData) bool {
	return !Options(msg.Options).IsNotNotification()
}

type MsgBySeq []*sdkws.MsgData

func (s MsgBySeq) Len() int {
	return len(s)
}

func (s MsgBySeq) Less(i, j int) bool {
	return s[i].Seq < s[j].Seq
}

func (s MsgBySeq) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func Pb2String(pb proto.Message) (string, error) {
	s, err := proto.Marshal(pb)
	if err != nil {
		return "", errs.Wrap(err)
	}
	return string(s), nil
}

func String2Pb(s string, pb proto.Message) error {
	return proto.Unmarshal([]byte(s), pb)
}

// ShouldDeliverSystemMsgToChat 判断系统产生的通知类消息是否要写入会话并下发给客户端。
// 返回 false 时：不写入聊天缓存/DB、不推送给客户端，用于减少群聊/单聊里大量系统推送刷屏且不影响正常功能。
//
// 始终下发：
//   - HasReadReceipt / MsgRevokeNotification / DeleteMsgsNotification（已读、撤回、删除）
//   - GroupCreated / MemberInvited / MemberEnter / GroupDismissed（建群/入群/解散）
//   - 普通用户消息
//
// 不下发：其余系统通知（组织/权限变更等），避免刷屏。
//
// dawn 2026-07-09 修"组织后台/App 建群后消息列表不出现群"：
// online_history_msg_handler.categorizeMessageLists 用
//   IsSendMsg && ShouldDeliverSystemMsgToChat
// 决定是否把通知 clone 成聊天消息进 storageMsgList。
// 只有 storageMsgList → handleMsg 才会：
//   1) 写入 sg_<groupID> 会话消息并分配 seq
//   2) isNewConversation 时调用 CreateGroupChatConversations
// 原先白名单只有已读/撤回/删除，GroupCreated(1501) 等一律 false，
// 导致即使 isSendMsg=true + history=true，通知只进 n_<gid> 通知通道，
// 永远不建 sg_ 会话 → 客户端消息列表看不到新群，要有人发第一条真实消息才出现。
// 线上实测群 3200041810(666)：history/persistent 已 true，但 conversationID=n_3200041810，
// conversation 表 0 行、sg_ seq 不存在。
func ShouldDeliverSystemMsgToChat(msg *sdkws.MsgData) bool {
	if msg == nil {
		return true
	}
	if msg.MsgFrom != constant.SysMsgType {
		return true
	}
	if msg.ContentType < constant.NotificationBegin || msg.ContentType > constant.NotificationEnd {
		return true
	}
	switch msg.ContentType {
	case constant.HasReadReceipt:
		return true
	case constant.MsgRevokeNotification, constant.DeleteMsgsNotification:
		return true
	// 建群 / 被拉入群 / 入群 / 解散：必须进聊天会话，否则客户端无从建会话行
	case constant.GroupCreatedNotification,
		constant.MemberInvitedNotification,
		constant.MemberEnterNotification,
		constant.GroupDismissedNotification:
		return true
	default:
		return false
	}
}
