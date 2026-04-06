package push

import (
	"context"
	"encoding/json"
	"time"

	"github.com/openimsdk/open-im-server/v3/internal/push/offlinepush"
	"github.com/openimsdk/open-im-server/v3/internal/push/offlinepush/options"
	"github.com/openimsdk/open-im-server/v3/pkg/common/prommetrics"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/controller"
	"github.com/openimsdk/open-im-server/v3/pkg/common/webhook"
	"github.com/openimsdk/open-im-server/v3/pkg/msgprocessor"
	"github.com/openimsdk/open-im-server/v3/pkg/rpccache"
	"github.com/openimsdk/open-im-server/v3/pkg/rpcli"
	"github.com/openimsdk/open-im-server/v3/pkg/util/conversationutil"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msggateway"
	pbpush "github.com/openimsdk/open-im-server/v3/protocol/push"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/discovery"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/mcontext"
	"github.com/openimsdk/tools/utils/datautil"
	"github.com/openimsdk/tools/utils/jsonutil"
	"github.com/openimsdk/tools/utils/timeutil"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

type ConsumerHandler struct {
	// offlinePusher 负责「离线推送」能力（厂商通道：个推/FCM/极光等）
	offlinePusher offlinepush.OfflinePusher
	// onlinePusher 负责「在线推送」，即通过 msggateway 的长连接实时下发
	onlinePusher OnlinePusher
	// pushDatabase 持久化推送记录/状态的存储抽象
	pushDatabase controller.PushDatabase
	// onlineCache 缓存在线状态和设备信息，用于决定哪些用户可以在线推送
	onlineCache *rpccache.OnlineCache
	// groupLocalCache 本地群缓存，加速获取群成员列表等
	groupLocalCache *rpccache.GroupLocalCache
	// conversationLocalCache 会话本地缓存，用于快速获取会话设置（免打扰等）
	conversationLocalCache *rpccache.ConversationLocalCache
	// webhookClient 支持在推送前/后触发自定义 Webhook（如审计/埋点）
	webhookClient *webhook.Client
	// config 推送模块完整配置
	config *Config
	// 以下为各 RPC 客户端，用于查询用户/群/会话/消息相关数据
	userClient         *rpcli.UserClient
	groupClient        *rpcli.GroupClient
	msgClient          *rpcli.MsgClient
	conversationClient *rpcli.ConversationClient
}

func NewConsumerHandler(ctx context.Context, config *Config, database controller.PushDatabase, offlinePusher offlinepush.OfflinePusher, rdb redis.UniversalClient, client discovery.Conn) (*ConsumerHandler, error) {
	userConn, err := client.GetConn(ctx, config.Discovery.RpcService.User)
	if err != nil {
		return nil, err
	}
	groupConn, err := client.GetConn(ctx, config.Discovery.RpcService.Group)
	if err != nil {
		return nil, err
	}
	msgConn, err := client.GetConn(ctx, config.Discovery.RpcService.Msg)
	if err != nil {
		return nil, err
	}
	conversationConn, err := client.GetConn(ctx, config.Discovery.RpcService.Conversation)
	if err != nil {
		return nil, err
	}
	onlinePusher, err := NewOnlinePusher(client, config)
	if err != nil {
		return nil, err
	}
	var consumerHandler ConsumerHandler
	consumerHandler.userClient = rpcli.NewUserClient(userConn)
	consumerHandler.groupClient = rpcli.NewGroupClient(groupConn)
	consumerHandler.msgClient = rpcli.NewMsgClient(msgConn)
	consumerHandler.conversationClient = rpcli.NewConversationClient(conversationConn)

	consumerHandler.offlinePusher = offlinePusher
	consumerHandler.onlinePusher = onlinePusher
	consumerHandler.groupLocalCache = rpccache.NewGroupLocalCache(consumerHandler.groupClient, &config.LocalCacheConfig, rdb)
	consumerHandler.conversationLocalCache = rpccache.NewConversationLocalCache(consumerHandler.conversationClient, &config.LocalCacheConfig, rdb)
	consumerHandler.webhookClient = webhook.NewWebhookClient(config.WebhooksConfig.URL)
	consumerHandler.config = config
	consumerHandler.pushDatabase = database
	consumerHandler.onlineCache, err = rpccache.NewOnlineCache(consumerHandler.userClient, consumerHandler.groupLocalCache, rdb, config.RpcConfig.FullUserCache, nil)
	if err != nil {
		return nil, err
	}
	return &consumerHandler, nil
}

func (c *ConsumerHandler) HandleMs2PsChat(ctx context.Context, msg []byte) {
	msgFromMQ := pbpush.PushMsgReq{}
	if err := proto.Unmarshal(msg, &msgFromMQ); err != nil {
		log.ZError(ctx, "push Unmarshal msg err", err, "msg", string(msg))
		return
	}

	sec := msgFromMQ.MsgData.SendTime / 1000
	nowSec := timeutil.GetCurrentTimestampBySecond()

	// 检查消息是否已经很旧，超过30秒的消息可能已经过时
	if nowSec-sec > 30 {
		log.ZWarn(ctx, "消息延迟过高，可能已过时", nil, "msg_type", msgFromMQ.MsgData.ContentType,
			"session_type", msgFromMQ.MsgData.SessionType, "delay_seconds", nowSec-sec)

		// 但仍然处理系统消息和非聊天消息
		if msgFromMQ.MsgData.ContentType <= constant.Text ||
			msgFromMQ.MsgData.ContentType == constant.SignalingNotification {
			prommetrics.MsgLoneTimePushCounter.Inc()
		} else {
			// 非重要消息，直接丢弃，避免大量过期消息拥塞系统
			log.ZInfo(ctx, "丢弃延迟过高的非重要消息", "content_type", msgFromMQ.MsgData.ContentType,
				"session_type", msgFromMQ.MsgData.SessionType, "delay_seconds", nowSec-sec)
			return
		}
	}

	var err error

	// 根据消息类型决定处理优先级
	// 1. 单聊文本/图片/语音/视频等重要消息使用高优先级通道
	isHighPriority := false

	// 判断是否为用户直接聊天消息（非通知类消息）
	if msgFromMQ.MsgData.SessionType == constant.SingleChatType &&
		(msgFromMQ.MsgData.ContentType <= constant.File ||
			msgFromMQ.MsgData.ContentType == constant.SignalingNotification) {
		isHighPriority = true
	}

	// 推送到对应通道
	switch msgFromMQ.MsgData.SessionType {
	case constant.ReadGroupChatType:
		if isHighPriority {
			// 较小的群聊也走高优先级
			if len(msgFromMQ.MsgData.GroupID) > 0 {
				memberIDs, err := c.groupLocalCache.GetGroupMemberIDs(ctx, msgFromMQ.MsgData.GroupID)
				if err == nil && len(memberIDs) < 200 {
					err = c.PushHighPriority2Group(ctx, msgFromMQ.MsgData.GroupID, msgFromMQ.MsgData)
					break
				}
			}
		}
		err = c.Push2Group(ctx, msgFromMQ.MsgData.GroupID, msgFromMQ.MsgData)
	default:
		var pushUserIDList []string
		isSenderSync := datautil.GetSwitchFromOptions(msgFromMQ.MsgData.Options, constant.IsSenderSync)
		if !isSenderSync || msgFromMQ.MsgData.SendID == msgFromMQ.MsgData.RecvID {
			pushUserIDList = append(pushUserIDList, msgFromMQ.MsgData.RecvID)
		} else {
			pushUserIDList = append(pushUserIDList, msgFromMQ.MsgData.RecvID, msgFromMQ.MsgData.SendID)
		}

		if isHighPriority {
			err = c.PushHighPriority2User(ctx, pushUserIDList, msgFromMQ.MsgData)
		} else {
			err = c.Push2User(ctx, pushUserIDList, msgFromMQ.MsgData)
		}
	}

	if err != nil {
		log.ZError(ctx, "Push failed", err, "msg", msgFromMQ.String())
	}
}

// 高优先级消息推送到用户
func (c *ConsumerHandler) PushHighPriority2User(ctx context.Context, userIDs []string, msg *sdkws.MsgData) error {
	log.ZInfo(ctx, "开始高优先级消息推送", "userIDs", userIDs, "msgType", msg.ContentType)

	if err := c.webhookBeforeOnlinePush(ctx, &c.config.WebhooksConfig.BeforeOnlinePush, userIDs, msg); err != nil {
		return err
	}

	// 执行快速路径推送
	wsResults, err := c.GetConnsAndOnlinePush(ctx, msg, userIDs)
	if err != nil {
		return err
	}

	// 如果不需要离线推送，直接返回
	if !c.shouldPushOffline(ctx, msg) {
		return nil
	}

	// 对于高优先级消息，尝试立即进行离线推送
	for _, v := range wsResults {
		//message sender do not need offline push
		if msg.SendID == v.UserID {
			continue
		}
		//receiver online push success
		if v.OnlinePush {
			return nil
		}
	}

	// 在线推送失败，立即执行离线推送
	needOfflinePushUserID := []string{msg.RecvID}
	err = c.offlinePushMsg(ctx, msg, needOfflinePushUserID)
	if err != nil {
		log.ZWarn(ctx, "高优先级消息离线推送失败", err)
	}

	return nil
}

// 高优先级消息推送到群组
func (c *ConsumerHandler) PushHighPriority2Group(ctx context.Context, groupID string, msg *sdkws.MsgData) error {
	log.ZInfo(ctx, "开始高优先级群组消息推送", "groupID", groupID, "msgType", msg.ContentType)

	var pushToUserIDs []string
	if err := c.webhookBeforeGroupOnlinePush(ctx, &c.config.WebhooksConfig.BeforeGroupOnlinePush, groupID, msg,
		&pushToUserIDs); err != nil {
		return err
	}

	err := c.groupMessagesHandler(ctx, groupID, &pushToUserIDs, msg)
	if err != nil {
		return err
	}

	// 使用优化的小群组推送策略
	wsResults, err := c.onlinePusher.GetConnsAndOnlinePush(ctx, msg, pushToUserIDs)
	if err != nil {
		return err
	}

	// 如果不需要离线推送，直接返回
	if !c.shouldPushOffline(ctx, msg) {
		return nil
	}

	// 快速获取需要离线推送的用户
	needOfflinePushUserIDs := c.onlinePusher.GetOnlinePushFailedUserIDs(ctx, msg, wsResults, &pushToUserIDs)
	if len(needOfflinePushUserIDs) == 0 {
		return nil
	}

	// 快速过滤并推送离线消息
	filteredUserIDs, err := c.conversationClient.GetConversationOfflinePushUserIDs(ctx,
		conversationutil.GenGroupConversationID(groupID), needOfflinePushUserIDs)
	if err != nil {
		return err
	}

	// 直接执行离线推送
	if len(filteredUserIDs) > 0 {
		err = c.offlinePushMsg(ctx, msg, filteredUserIDs)
		if err != nil {
			log.ZWarn(ctx, "高优先级群组消息离线推送失败", err)
		}
	}

	return nil
}

func (c *ConsumerHandler) WaitCache() {
	c.onlineCache.Lock.Lock()
	for c.onlineCache.CurrentPhase.Load() < rpccache.DoSubscribeOver {
		c.onlineCache.Cond.Wait()
	}
	c.onlineCache.Lock.Unlock()
}

// Push2User Suitable for two types of conversations, one is SingleChatType and the other is NotificationChatType.
func (c *ConsumerHandler) Push2User(ctx context.Context, userIDs []string, msg *sdkws.MsgData) (err error) {
	// 减少日志输出，只记录非读回执消息的详细日志
	isVerboseLogging := msg.ContentType != constant.HasReadReceipt

	if isVerboseLogging {
		log.ZInfo(ctx, "Get msg from msg_transfer And push msg", "userIDs", userIDs, "msgType", msg.ContentType)
	}

	defer func(duration time.Time) {
		// 只对超过100ms的操作或非读回执消息记录时间消耗
		t := time.Since(duration)
		if isVerboseLogging || t > 100*time.Millisecond {
			log.ZInfo(ctx, "Push msg completed", "msgType", msg.ContentType, "time cost", t)
		}
	}(time.Now())

	// 对空用户列表进行快速处理
	if len(userIDs) == 0 {
		return nil
	}

	if err := c.webhookBeforeOnlinePush(ctx, &c.config.WebhooksConfig.BeforeOnlinePush, userIDs, msg); err != nil {
		return err
	}

	wsResults, err := c.GetConnsAndOnlinePush(ctx, msg, userIDs)
	if err != nil {
		return err
	}

	// 只对非读回执消息或有明确结果的情况记录调试日志
	if isVerboseLogging && wsResults != nil && len(wsResults) > 0 {
		log.ZDebug(ctx, "push result", "msgType", msg.ContentType, "resultCount", len(wsResults))
	}

	if !c.shouldPushOffline(ctx, msg) {
		return nil
	}
	log.ZInfo(ctx, "pushOffline start")

	for _, v := range wsResults {
		//message sender do not need offline push
		if msg.SendID == v.UserID {
			continue
		}
		//receiver online push success
		if v.OnlinePush {
			return nil
		}
	}
	needOfflinePushUserID := []string{msg.RecvID}
	var offlinePushUserID []string

	//receiver offline push
	if err = c.webhookBeforeOfflinePush(ctx, &c.config.WebhooksConfig.BeforeOfflinePush, needOfflinePushUserID, msg, &offlinePushUserID); err != nil {
		return err
	}

	if len(offlinePushUserID) > 0 {
		needOfflinePushUserID = offlinePushUserID
	}
	err = c.offlinePushMsg(ctx, msg, needOfflinePushUserID)
	if err != nil {
		log.ZDebug(ctx, "offlinePushMsg failed", err, "needOfflinePushUserID", needOfflinePushUserID, "msg", msg)
		log.ZWarn(ctx, "offlinePushMsg failed", err, "needOfflinePushUserID length", len(needOfflinePushUserID), "msg", msg)
		return nil
	}

	return nil
}

func (c *ConsumerHandler) shouldPushOffline(_ context.Context, msg *sdkws.MsgData) bool {
	isOfflinePush := datautil.GetSwitchFromOptions(msg.Options, constant.IsOfflinePush)
	if !isOfflinePush {
		return false
	}
	switch msg.ContentType {
	case constant.RoomParticipantsConnectedNotification:
		return false
	case constant.RoomParticipantsDisconnectedNotification:
		return false
	}
	return true
}

func (c *ConsumerHandler) GetConnsAndOnlinePush(ctx context.Context, msg *sdkws.MsgData, pushToUserIDs []string) ([]*msggateway.SingleMsgToUserResults, error) {
	if msg != nil && msg.Status == constant.MsgStatusSending {
		msg.Status = constant.MsgStatusSendSuccess
	}
	onlineUserIDs, offlineUserIDs, err := c.onlineCache.GetUsersOnline(ctx, pushToUserIDs)
	if err != nil {
		return nil, err
	}

	log.ZDebug(ctx, "GetConnsAndOnlinePush online cache", "sendID", msg.SendID, "recvID", msg.RecvID, "groupID", msg.GroupID, "sessionType", msg.SessionType, "clientMsgID", msg.ClientMsgID, "serverMsgID", msg.ServerMsgID, "offlineUserIDs", offlineUserIDs, "onlineUserIDs", onlineUserIDs)
	var result []*msggateway.SingleMsgToUserResults
	if len(onlineUserIDs) > 0 {
		var err error
		result, err = c.onlinePusher.GetConnsAndOnlinePush(ctx, msg, onlineUserIDs)
		if err != nil {
			return nil, err
		}
	}
	for _, userID := range offlineUserIDs {
		result = append(result, &msggateway.SingleMsgToUserResults{
			UserID: userID,
		})
	}
	return result, nil
}

func (c *ConsumerHandler) Push2Group(ctx context.Context, groupID string, msg *sdkws.MsgData) (err error) {
	// 减少日志输出，只记录非读回执消息的详细日志
	isVerboseLogging := msg.ContentType != constant.HasReadReceipt

	if isVerboseLogging {
		log.ZInfo(ctx, "Get group msg from msg_transfer", "groupID", groupID, "msgType", msg.ContentType)
	}

	defer func(duration time.Time) {
		// 只对超过100ms的操作或非读回执消息记录时间消耗
		t := time.Since(duration)
		if isVerboseLogging || t > 100*time.Millisecond {
			log.ZInfo(ctx, "Group push completed", "groupID", groupID, "msgType", msg.ContentType, "time cost", t)
		}
	}(time.Now())

	var pushToUserIDs []string
	if err = c.webhookBeforeGroupOnlinePush(ctx, &c.config.WebhooksConfig.BeforeGroupOnlinePush, groupID, msg,
		&pushToUserIDs); err != nil {
		return err
	}

	// 如果群组为空，快速返回
	if len(pushToUserIDs) == 0 {
		err = c.groupMessagesHandler(ctx, groupID, &pushToUserIDs, msg)
		if err != nil {
			return err
		}

		// 检查处理后是否还是空群组
		if len(pushToUserIDs) == 0 {
			if isVerboseLogging {
				log.ZInfo(ctx, "Group has no members to push", "groupID", groupID)
			}
			return nil
		}
	} else {
		// 处理群组消息逻辑
		err = c.groupMessagesHandler(ctx, groupID, &pushToUserIDs, msg)
		if err != nil {
			return err
		}
	}

	wsResults, err := c.GetConnsAndOnlinePush(ctx, msg, pushToUserIDs)
	if err != nil {
		return err
	}

	// 只对非读回执消息或成功的结果记录调试日志
	if isVerboseLogging && wsResults != nil && len(wsResults) > 0 {
		log.ZDebug(ctx, "group push result", "msgType", msg.ContentType, "resultCount", len(wsResults))
	}

	if !c.shouldPushOffline(ctx, msg) {
		return nil
	}
	needOfflinePushUserIDs := c.onlinePusher.GetOnlinePushFailedUserIDs(ctx, msg, wsResults, &pushToUserIDs)
	//filter some user, like don not disturb or don't need offline push etc.
	needOfflinePushUserIDs, err = c.filterGroupMessageOfflinePush(ctx, groupID, msg, needOfflinePushUserIDs)
	if err != nil {
		return err
	}
	log.ZInfo(ctx, "filterGroupMessageOfflinePush end")

	// Use offline push messaging
	if len(needOfflinePushUserIDs) > 0 {
		c.asyncOfflinePush(ctx, needOfflinePushUserIDs, msg)
	}

	return nil
}

func (c *ConsumerHandler) asyncOfflinePush(ctx context.Context, needOfflinePushUserIDs []string, msg *sdkws.MsgData) {
	var offlinePushUserIDs []string
	err := c.webhookBeforeOfflinePush(ctx, &c.config.WebhooksConfig.BeforeOfflinePush, needOfflinePushUserIDs, msg, &offlinePushUserIDs)
	if err != nil {
		log.ZWarn(ctx, "webhookBeforeOfflinePush failed", err, "msg", msg)
		return
	}

	if len(offlinePushUserIDs) > 0 {
		needOfflinePushUserIDs = offlinePushUserIDs
	}
	if err := c.pushDatabase.MsgToOfflinePushMQ(ctx, conversationutil.GenConversationUniqueKeyForSingle(msg.SendID, msg.RecvID), needOfflinePushUserIDs, msg); err != nil {
		log.ZDebug(ctx, "Msg To OfflinePush MQ error", err, "needOfflinePushUserIDs",
			needOfflinePushUserIDs, "msg", msg)
		log.ZWarn(ctx, "Msg To OfflinePush MQ error", err, "needOfflinePushUserIDs length",
			len(needOfflinePushUserIDs), "msg", msg)
		prommetrics.GroupChatMsgProcessFailedCounter.Inc()
		return
	}
}

func (c *ConsumerHandler) groupMessagesHandler(ctx context.Context, groupID string, pushToUserIDs *[]string, msg *sdkws.MsgData) (err error) {
	if len(*pushToUserIDs) == 0 {
		*pushToUserIDs, err = c.groupLocalCache.GetGroupMemberIDs(ctx, groupID)
		if err != nil {
			return err
		}
		switch msg.ContentType {
		case constant.MemberQuitNotification:
			var tips sdkws.MemberQuitTips
			if unmarshalNotificationElem(msg.Content, &tips) != nil {
				return err
			}
			if err = c.DeleteMemberAndSetConversationSeq(ctx, groupID, []string{tips.QuitUser.UserID}); err != nil {
				log.ZError(ctx, "MemberQuitNotification DeleteMemberAndSetConversationSeq", err, "groupID", groupID, "userID", tips.QuitUser.UserID)
			}
			*pushToUserIDs = append(*pushToUserIDs, tips.QuitUser.UserID)
		case constant.MemberKickedNotification:
			var tips sdkws.MemberKickedTips
			if unmarshalNotificationElem(msg.Content, &tips) != nil {
				return err
			}
			kickedUsers := datautil.Slice(tips.KickedUserList, func(e *sdkws.GroupMemberFullInfo) string { return e.UserID })
			if err = c.DeleteMemberAndSetConversationSeq(ctx, groupID, kickedUsers); err != nil {
				log.ZError(ctx, "MemberKickedNotification DeleteMemberAndSetConversationSeq", err, "groupID", groupID, "userIDs", kickedUsers)
			}

			*pushToUserIDs = append(*pushToUserIDs, kickedUsers...)
		case constant.GroupDismissedNotification:
			if msgprocessor.IsNotification(msgprocessor.GetConversationIDByMsg(msg)) {
				var tips sdkws.GroupDismissedTips
				if unmarshalNotificationElem(msg.Content, &tips) != nil {
					return err
				}
				log.ZDebug(ctx, "GroupDismissedNotificationInfo****", "groupID", groupID, "num", len(*pushToUserIDs), "list", pushToUserIDs)
				if len(c.config.Share.IMAdminUserID) > 0 {
					ctx = mcontext.WithOpUserIDContext(ctx, c.config.Share.IMAdminUserID[0])
				}
				defer func(groupID string) {
					if err := c.groupClient.DismissGroup(ctx, groupID, true); err != nil {
						log.ZError(ctx, "DismissGroup Notification clear members", err, "groupID", groupID)
					}
				}(groupID)
			}
		}
	}
	return err
}

func (c *ConsumerHandler) offlinePushMsg(ctx context.Context, msg *sdkws.MsgData, offlinePushUserIDs []string) error {
	title, content, opts, err := c.getOfflinePushInfos(msg)
	if err != nil {
		log.ZError(ctx, "getOfflinePushInfos failed", err, "msg", msg)
		return err
	}
	err = c.offlinePusher.Push(ctx, offlinePushUserIDs, title, content, opts)
	if err != nil {
		prommetrics.MsgOfflinePushFailedCounter.Inc()
		return err
	}
	return nil
}

func (c *ConsumerHandler) filterGroupMessageOfflinePush(ctx context.Context, groupID string, msg *sdkws.MsgData,
	offlinePushUserIDs []string) (userIDs []string, err error) {
	needOfflinePushUserIDs, err := c.conversationClient.GetConversationOfflinePushUserIDs(ctx, conversationutil.GenGroupConversationID(groupID), offlinePushUserIDs)
	if err != nil {
		return nil, err
	}
	return needOfflinePushUserIDs, nil
}

func (c *ConsumerHandler) getOfflinePushInfos(msg *sdkws.MsgData) (title, content string, opts *options.Opts, err error) {
	type AtTextElem struct {
		Text       string   `json:"text,omitempty"`
		AtUserList []string `json:"atUserList,omitempty"`
		IsAtSelf   bool     `json:"isAtSelf"`
	}

	opts = &options.Opts{Signal: &options.Signal{ClientMsgID: msg.ClientMsgID}}
	if msg.OfflinePushInfo != nil {
		opts.IOSBadgeCount = msg.OfflinePushInfo.IOSBadgeCount
		opts.IOSPushSound = msg.OfflinePushInfo.IOSPushSound
		opts.Ex = msg.OfflinePushInfo.Ex
	}

	if msg.OfflinePushInfo != nil {
		title = msg.OfflinePushInfo.Title
		content = msg.OfflinePushInfo.Desc
	}
	if title == "" {
		switch msg.ContentType {
		case constant.Text:
			fallthrough
		case constant.Picture:
			fallthrough
		case constant.Voice:
			fallthrough
		case constant.Video:
			fallthrough
		case constant.File:
			title = constant.ContentType2PushContent[int64(msg.ContentType)]
		case constant.AtText:
			ac := AtTextElem{}
			_ = jsonutil.JsonStringToStruct(string(msg.Content), &ac)
		case constant.SignalingNotification:
			title = constant.ContentType2PushContent[constant.SignalMsg]
		default:
			title = constant.ContentType2PushContent[constant.Common]
		}
	}
	if content == "" {
		content = title
	}
	return
}

func (c *ConsumerHandler) DeleteMemberAndSetConversationSeq(ctx context.Context, groupID string, userIDs []string) error {
	conversationID := msgprocessor.GetConversationIDBySessionType(constant.ReadGroupChatType, groupID)
	maxSeq, err := c.msgClient.GetConversationMaxSeq(ctx, conversationID)
	if err != nil {
		return err
	}
	return c.conversationClient.SetConversationMaxSeq(ctx, conversationID, userIDs, maxSeq)
}

func unmarshalNotificationElem(bytes []byte, t any) error {
	var notification sdkws.NotificationElem
	if err := json.Unmarshal(bytes, &notification); err != nil {
		return err
	}
	return json.Unmarshal([]byte(notification.Detail), t)
}
