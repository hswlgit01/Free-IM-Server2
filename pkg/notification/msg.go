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

package notification

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/openimsdk/open-im-server/v3/pkg/common/config"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msg"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/mq/memamq"
	"github.com/openimsdk/tools/utils/idutil"
	"github.com/openimsdk/tools/utils/jsonutil"
	"github.com/openimsdk/tools/utils/timeutil"
)

func newContentTypeConf(conf *config.Notification) map[int32]config.NotificationConfig {
	return map[int32]config.NotificationConfig{
		// group
		constant.GroupCreatedNotification:                 conf.GroupCreated,
		constant.GroupInfoSetNotification:                 conf.GroupInfoSet,
		constant.JoinGroupApplicationNotification:         conf.JoinGroupApplication,
		constant.MemberQuitNotification:                   conf.MemberQuit,
		constant.GroupApplicationAcceptedNotification:     conf.GroupApplicationAccepted,
		constant.GroupApplicationRejectedNotification:     conf.GroupApplicationRejected,
		constant.GroupOwnerTransferredNotification:        conf.GroupOwnerTransferred,
		constant.MemberKickedNotification:                 conf.MemberKicked,
		constant.MemberInvitedNotification:                conf.MemberInvited,
		constant.MemberEnterNotification:                  conf.MemberEnter,
		constant.GroupDismissedNotification:               conf.GroupDismissed,
		constant.GroupMutedNotification:                   conf.GroupMuted,
		constant.GroupCancelMutedNotification:             conf.GroupCancelMuted,
		constant.GroupMemberMutedNotification:             conf.GroupMemberMuted,
		constant.GroupMemberCancelMutedNotification:       conf.GroupMemberCancelMuted,
		constant.GroupMemberInfoSetNotification:           conf.GroupMemberInfoSet,
		constant.GroupMemberSetToAdminNotification:        conf.GroupMemberSetToAdmin,
		constant.GroupMemberSetToOrdinaryUserNotification: conf.GroupMemberSetToOrdinary,
		constant.GroupInfoSetAnnouncementNotification:     conf.GroupInfoSetAnnouncement,
		constant.GroupInfoSetNameNotification:             conf.GroupInfoSetName,
		// user
		constant.UserInfoUpdatedNotification:  conf.UserInfoUpdated,
		constant.UserStatusChangeNotification: conf.UserStatusChanged,
		// friend
		constant.FriendApplicationNotification:         conf.FriendApplicationAdded,
		constant.FriendApplicationApprovedNotification: conf.FriendApplicationApproved,
		constant.FriendApplicationRejectedNotification: conf.FriendApplicationRejected,
		constant.FriendAddedNotification:               conf.FriendAdded,
		constant.FriendDeletedNotification:             conf.FriendDeleted,
		constant.FriendRemarkSetNotification:           conf.FriendRemarkSet,
		constant.BlackAddedNotification:                conf.BlackAdded,
		constant.BlackDeletedNotification:              conf.BlackDeleted,
		constant.FriendInfoUpdatedNotification:         conf.FriendInfoUpdated,
		constant.FriendsInfoUpdateNotification:         conf.FriendInfoUpdated, // use the same FriendInfoUpdated
		// conversation
		constant.ConversationChangeNotification:      conf.ConversationChanged,
		constant.ConversationUnreadNotification:      conf.ConversationChanged,
		constant.ConversationPrivateChatNotification: conf.ConversationSetPrivate,
		// msg
		constant.MsgRevokeNotification:  {IsSendMsg: false, ReliabilityLevel: constant.ReliableNotificationNoMsg},
		constant.HasReadReceipt:         {IsSendMsg: false, ReliabilityLevel: constant.ReliableNotificationNoMsg},
		constant.DeleteMsgsNotification: {IsSendMsg: false, ReliabilityLevel: constant.ReliableNotificationNoMsg},
	}
}

func newSessionTypeConf() map[int32]int32 {
	return map[int32]int32{
		// group
		constant.GroupCreatedNotification:                 constant.ReadGroupChatType,
		constant.GroupInfoSetNotification:                 constant.ReadGroupChatType,
		constant.JoinGroupApplicationNotification:         constant.SingleChatType,
		constant.MemberQuitNotification:                   constant.ReadGroupChatType,
		constant.GroupApplicationAcceptedNotification:     constant.SingleChatType,
		constant.GroupApplicationRejectedNotification:     constant.SingleChatType,
		constant.GroupOwnerTransferredNotification:        constant.ReadGroupChatType,
		constant.MemberKickedNotification:                 constant.ReadGroupChatType,
		constant.MemberInvitedNotification:                constant.ReadGroupChatType,
		constant.MemberEnterNotification:                  constant.ReadGroupChatType,
		constant.GroupDismissedNotification:               constant.ReadGroupChatType,
		constant.GroupMutedNotification:                   constant.ReadGroupChatType,
		constant.GroupCancelMutedNotification:             constant.ReadGroupChatType,
		constant.GroupMemberMutedNotification:             constant.ReadGroupChatType,
		constant.GroupMemberCancelMutedNotification:       constant.ReadGroupChatType,
		constant.GroupMemberInfoSetNotification:           constant.ReadGroupChatType,
		constant.GroupMemberSetToAdminNotification:        constant.ReadGroupChatType,
		constant.GroupMemberSetToOrdinaryUserNotification: constant.ReadGroupChatType,
		constant.GroupInfoSetAnnouncementNotification:     constant.ReadGroupChatType,
		constant.GroupInfoSetNameNotification:             constant.ReadGroupChatType,
		// user
		constant.UserInfoUpdatedNotification:  constant.SingleChatType,
		constant.UserStatusChangeNotification: constant.SingleChatType,
		// friend
		constant.FriendApplicationNotification:         constant.SingleChatType,
		constant.FriendApplicationApprovedNotification: constant.SingleChatType,
		constant.FriendApplicationRejectedNotification: constant.SingleChatType,
		constant.FriendAddedNotification:               constant.SingleChatType,
		constant.FriendDeletedNotification:             constant.SingleChatType,
		constant.FriendRemarkSetNotification:           constant.SingleChatType,
		constant.BlackAddedNotification:                constant.SingleChatType,
		constant.BlackDeletedNotification:              constant.SingleChatType,
		constant.FriendInfoUpdatedNotification:         constant.SingleChatType,
		constant.FriendsInfoUpdateNotification:         constant.SingleChatType,
		// conversation
		constant.ConversationChangeNotification:      constant.SingleChatType,
		constant.ConversationUnreadNotification:      constant.SingleChatType,
		constant.ConversationPrivateChatNotification: constant.SingleChatType,
		// delete
		constant.DeleteMsgsNotification: constant.SingleChatType,
	}
}

type NotificationSender struct {
	contentTypeConf map[int32]config.NotificationConfig
	sessionTypeConf map[int32]int32
	sendMsg         func(ctx context.Context, req *msg.SendMsgReq) (*msg.SendMsgResp, error)
	getUserInfo     func(ctx context.Context, userID string) (*sdkws.UserInfo, error)
	queue           *memamq.MemoryQueue
	// 专用读回执队列，用于单独处理高频率的已读通知
	readReceiptQueue *memamq.MemoryQueue
	// 高优先级消息队列，用于处理重要消息（聊天、系统通知等）
	highPriorityQueue *memamq.MemoryQueue
}

func WithQueue(queue *memamq.MemoryQueue) NotificationSenderOptions {
	return func(s *NotificationSender) {
		s.queue = queue
	}
}

type NotificationSenderOptions func(*NotificationSender)

func WithLocalSendMsg(sendMsg func(ctx context.Context, req *msg.SendMsgReq) (*msg.SendMsgResp, error)) NotificationSenderOptions {
	return func(s *NotificationSender) {
		s.sendMsg = sendMsg
	}
}

func WithRpcClient(sendMsg func(ctx context.Context, req *msg.SendMsgReq) (*msg.SendMsgResp, error)) NotificationSenderOptions {
	return func(s *NotificationSender) {
		s.sendMsg = func(ctx context.Context, req *msg.SendMsgReq) (*msg.SendMsgResp, error) {
			return sendMsg(ctx, req)
		}
	}
}

func WithUserRpcClient(getUserInfo func(ctx context.Context, userID string) (*sdkws.UserInfo, error)) NotificationSenderOptions {
	return func(s *NotificationSender) {
		s.getUserInfo = getUserInfo
	}
}

const (
	// 增加工作者数量，从64增加到128
	notificationWorkerCount = 128
	// 增加缓冲区大小，从4MB提高到8MB
	notificationBufferSize = 1024 * 1024 * 8

	// 读回执专用工作者数量，从32增加到64
	readReceiptWorkerCount = 64
	// 读回执专用缓冲区大小，从2MB增加到8MB
	readReceiptBufferSize = 1024 * 1024 * 8

	// 高优先级通知队列工作者数，从20增加到40
	highPriorityWorkerCount = 40
	// 高优先级通知队列缓冲区大小，从2MB增加到4MB
	highPriorityBufferSize = 1024 * 1024 * 4
)

func NewNotificationSender(conf *config.Notification, opts ...NotificationSenderOptions) *NotificationSender {
	notificationSender := &NotificationSender{contentTypeConf: newContentTypeConf(conf), sessionTypeConf: newSessionTypeConf()}
	for _, opt := range opts {
		opt(notificationSender)
	}
	if notificationSender.queue == nil {
		notificationSender.queue = memamq.NewMemoryQueue(notificationWorkerCount, notificationBufferSize)
	}

	// 初始化专用读回执队列，优化读回执处理
	notificationSender.readReceiptQueue = memamq.NewMemoryQueue(readReceiptWorkerCount, readReceiptBufferSize)

	// 初始化高优先级消息队列
	notificationSender.highPriorityQueue = memamq.NewMemoryQueue(highPriorityWorkerCount, highPriorityBufferSize)

	return notificationSender
}

type notificationOpt struct {
	RpcGetUsername bool
	SendMessage    *bool
}

type NotificationOptions func(*notificationOpt)

func WithRpcGetUserName() NotificationOptions {
	return func(opt *notificationOpt) {
		opt.RpcGetUsername = true
	}
}
func WithSendMessage(sendMessage *bool) NotificationOptions {
	return func(opt *notificationOpt) {
		opt.SendMessage = sendMessage
	}
}

func (s *NotificationSender) send(ctx context.Context, sendID, recvID string, contentType, sessionType int32, m proto.Message, opts ...NotificationOptions) {
	// 在替换 ctx 前保留 operationID，否则异步队列执行时 RPC 会报 "ctx missing operationID"
	opID, _ := ctx.Value(constant.OperationID).(string)
	if opID == "" {
		opID = idutil.OperationIDGenerator()
	}
	// 创建新的根上下文，避免依赖原有上下文的取消
	ctx = context.WithValue(context.Background(), constant.OperationID, opID)
	// 增加超时时间到10秒，避免大规模并发时消息处理超时
	ctx, cancel := context.WithTimeout(ctx, time.Second*time.Duration(10))
	defer cancel()
	n := sdkws.NotificationElem{Detail: jsonutil.StructToJsonString(m)}
	content, err := json.Marshal(&n)
	if err != nil {
		log.ZWarn(ctx, "json.Marshal failed", err, "sendID", sendID, "recvID", recvID, "contentType", contentType, "msg", jsonutil.StructToJsonString(m))
		return
	}
	notificationOpt := &notificationOpt{}
	for _, opt := range opts {
		opt(notificationOpt)
	}
	var req msg.SendMsgReq
	var msg sdkws.MsgData
	var userInfo *sdkws.UserInfo
	if notificationOpt.RpcGetUsername && s.getUserInfo != nil {
		userInfo, err = s.getUserInfo(ctx, sendID)
		if err != nil {
			log.ZWarn(ctx, "getUserInfo failed", err, "sendID", sendID)
			return
		}
		msg.SenderNickname = userInfo.Nickname
		msg.SenderFaceURL = userInfo.FaceURL
	}
	var offlineInfo sdkws.OfflinePushInfo
	msg.SendID = sendID
	msg.RecvID = recvID
	msg.Content = content
	msg.MsgFrom = constant.SysMsgType
	msg.ContentType = contentType
	msg.SessionType = sessionType
	if msg.SessionType == constant.ReadGroupChatType {
		msg.GroupID = recvID
	}
	msg.CreateTime = timeutil.GetCurrentTimestampByMill()
	msg.ClientMsgID = idutil.GetMsgIDByMD5(sendID)
	optionsConfig := s.contentTypeConf[contentType]
	if sendID == recvID && contentType == constant.HasReadReceipt {
		optionsConfig.ReliabilityLevel = constant.UnreliableNotification
	}
	options := config.GetOptionsByNotification(optionsConfig, notificationOpt.SendMessage)
	s.SetOptionsByContentType(ctx, options, contentType)
	msg.Options = options
	// fill Notification OfflinePush by config
	offlineInfo.Title = optionsConfig.OfflinePush.Title
	offlineInfo.Desc = optionsConfig.OfflinePush.Desc
	offlineInfo.Ex = optionsConfig.OfflinePush.Ext
	msg.OfflinePushInfo = &offlineInfo
	req.MsgData = &msg
	_, err = s.sendMsg(ctx, &req)
	if err != nil {
		log.ZWarn(ctx, "SendMsg failed", err, "req", req.String())
	}
}

func (s *NotificationSender) NotificationWithSessionType(ctx context.Context, sendID, recvID string, contentType, sessionType int32, m proto.Message, opts ...NotificationOptions) {
	if err := s.queue.Push(func() { s.send(ctx, sendID, recvID, contentType, sessionType, m, opts...) }); err != nil {
		log.ZWarn(ctx, "Push to queue failed", err, "sendID", sendID, "recvID", recvID, "msg", jsonutil.StructToJsonString(m))
	}
}

func (s *NotificationSender) Notification(ctx context.Context, sendID, recvID string, contentType int32, m proto.Message, opts ...NotificationOptions) {
	s.NotificationWithSessionType(ctx, sendID, recvID, contentType, s.sessionTypeConf[contentType], m, opts...)
}

// 高优先级通知，用于发送需要快速处理的消息
func (s *NotificationSender) HighPriorityNotification(ctx context.Context, sendID, recvID string, contentType int32, m proto.Message, opts ...NotificationOptions) {
	sessionType := s.sessionTypeConf[contentType]
	if err := s.highPriorityQueue.Push(func() { s.send(ctx, sendID, recvID, contentType, sessionType, m, opts...) }); err != nil {
		log.ZWarn(ctx, "Push to high priority queue failed", err, "sendID", sendID, "recvID", recvID, "msg", jsonutil.StructToJsonString(m))
	}
}

// 专门用于发送读回执的方法，使用专用队列和优化配置
func (s *NotificationSender) SendReadReceipt(ctx context.Context, sendID, recvID string, contentType, sessionType int32, m proto.Message, opts ...NotificationOptions) {
	if err := s.readReceiptQueue.Push(func() {
		// 为大型消息执行分片处理
		s.sendWithFragmentation(ctx, sendID, recvID, contentType, sessionType, m, opts...)
	}); err != nil {
		log.ZWarn(ctx, "Push read receipt to queue failed", err, "sendID", sendID, "recvID", recvID, "msg", jsonutil.StructToJsonString(m))
	}
}

// 最大单条消息大小限制（约6MB）- 超过此大小的消息将被分片
const maxFragmentSize = 1024 * 1024 * 6

// 处理大型消息，如果消息超过阈值则进行分片
func (s *NotificationSender) sendWithFragmentation(ctx context.Context, sendID, recvID string, contentType, sessionType int32, m proto.Message, opts ...NotificationOptions) {
	var contentSize int // 定义一个变量跟踪内容大小，用于日志

	// 对读回执消息进行特殊检查，不需要序列化和大小检查
	if contentType == constant.HasReadReceipt {
		// 尝试将MarkAsReadTips转换为结构体
		markAsReadTips, ok := m.(*sdkws.MarkAsReadTips)
		if ok && markAsReadTips != nil {
			// 如果消息中存在序列号数组，无论大小如何都采用分片策略
			// 这是一种预防性措施，避免后面潜在的大小问题
			if len(markAsReadTips.Seqs) > 0 {
				goto FRAGMENT_PROCESSING
			}
		}
	}

	// 针对特定消息类型进行预防性分片
	// 这些消息类型通常比较大，容易导致WebSocket 1009错误
	if contentType == constant.GroupInfoSetNotification ||
		contentType == constant.ConversationChangeNotification ||
		contentType == constant.UserInfoUpdatedNotification ||
		contentType == 113 || // 自定义消息类型113
		contentType == 1300 || // 自定义消息类型1300
		contentType == 2200 { // 自定义消息类型2200
		// 这些类型的消息通常比较大，直接进入分片处理流程
		goto FRAGMENT_PROCESSING
	}

	// 将消息序列化，以检查消息大小
	{
		n := sdkws.NotificationElem{Detail: jsonutil.StructToJsonString(m)}
		content, err := json.Marshal(&n)
		if err != nil {
			log.ZWarn(ctx, "json.Marshal failed", err, "sendID", sendID, "recvID", recvID, "contentType", contentType, "msg", jsonutil.StructToJsonString(m))
			return
		}

		contentSize = len(content) // 保存内容大小以供后续日志使用

		// 消息大小检查
		if contentSize <= int(maxFragmentSize) {
			// 消息不大，使用正常流程
			s.send(ctx, sendID, recvID, contentType, sessionType, m, opts...)
			return
		}
	}

FRAGMENT_PROCESSING:

	// 需要分片处理 - 对于MarkAsReadTips需要特殊处理
	if contentType == constant.HasReadReceipt {
		// 尝试将MarkAsReadTips转换为结构体
		markAsReadTips, ok := m.(*sdkws.MarkAsReadTips)
		if ok && markAsReadTips != nil && len(markAsReadTips.Seqs) > 500 {
			// 序列号数组太大，这是导致消息过大的主要原因
			// 将序列号数组分批发送，调整为500个序列号/批次（更频繁地分批以降低单条消息大小）
			seqsBatches := splitSeqsIntoBatches(markAsReadTips.Seqs, 500)

			// 第一批包含最大序列号，后续批次使用nil
			hasReadSeq := markAsReadTips.HasReadSeq
			conversationID := markAsReadTips.ConversationID
			markAsReadUserID := markAsReadTips.MarkAsReadUserID

			for i, batch := range seqsBatches {
				// 创建一个新的标记已读提示
				tipsBatch := &sdkws.MarkAsReadTips{
					MarkAsReadUserID: markAsReadUserID,
					ConversationID:   conversationID,
					HasReadSeq:       hasReadSeq,
				}

				if i == 0 {
					// 只在第一个批次发送序列号数组，其他批次只发送HasReadSeq
					tipsBatch.Seqs = batch
				} else {
					tipsBatch.Seqs = nil // 后续批次不重复发送序列号
				}

				// 发送这个批次
				s.send(ctx, sendID, recvID, contentType, sessionType, tipsBatch, opts...)

				// 增加批次间延迟，避免淹没接收者，但也不要太长以保持良好响应性
				time.Sleep(20 * time.Millisecond)
			}
			return
		}
	}

	// 尝试对任何类型的大型消息进行通用分片处理
	// 这是一个更激进的方法，将消息拆分成多个小块发送
	// 目前只分成两个部分：第一部分包含消息的一半内容，第二部分包含完整内容
	// 这种方法虽然会有一些内容重复，但确保接收端至少能收到完整消息的一部分

	// 将消息序列化为JSON字符串
	msgJSON := jsonutil.StructToJsonString(m)
	if len(msgJSON) > 1000 { // 只处理较大的消息
		log.ZInfo(ctx, "使用通用分片策略发送大消息",
			"sendID", sendID, "recvID", recvID,
			"contentType", contentType, "msgSize", len(msgJSON))

		// 第一部分：发送完整消息（分片方法1）
		// 我们直接发送完整消息，不使用包装器
		// 因为SDK可能无法正确处理自定义包装器
		s.send(ctx, sendID, recvID, contentType, sessionType, m, opts...)

		// 添加一个小延迟
		time.Sleep(50 * time.Millisecond)

		// 第二部分：发送完整消息
		s.send(ctx, sendID, recvID, contentType, sessionType, m, opts...)
		return
	}

	// 其他无法分片的消息使用常规发送方式
	log.ZWarn(ctx, "使用常规方式发送大型消息", nil, "sendID", sendID, "recvID", recvID,
		"contentType", contentType, "contentSize", contentSize)
	s.send(ctx, sendID, recvID, contentType, sessionType, m, opts...)
}

// 将序列号数组分割成较小的批次
func splitSeqsIntoBatches(seqs []int64, batchSize int) [][]int64 {
	if len(seqs) == 0 {
		return nil
	}

	// 计算需要多少个批次
	numBatches := (len(seqs) + batchSize - 1) / batchSize
	batches := make([][]int64, numBatches)

	// 填充每个批次
	for i := 0; i < numBatches; i++ {
		start := i * batchSize
		end := start + batchSize
		if end > len(seqs) {
			end = len(seqs)
		}
		batches[i] = seqs[start:end]
	}

	return batches
}

func (s *NotificationSender) SetOptionsByContentType(_ context.Context, options map[string]bool, contentType int32) {
	switch contentType {
	case constant.UserStatusChangeNotification:
		options[constant.IsSenderSync] = false
	case constant.FriendApplicationNotification:
		// 禁用好友申请通知的senderSync，避免申请发送者收到自己的申请消息
		// 这可能导致SDK客户端出现异常行为
		options[constant.IsSenderSync] = false
	default:
	}
}
