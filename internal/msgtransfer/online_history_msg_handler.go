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

package msgtransfer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"

	"sync"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/rpcli"
	"github.com/openimsdk/tools/discovery"

	"github.com/go-redis/redis"
	"github.com/openimsdk/open-im-server/v3/pkg/common/prommetrics"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/controller"
	"github.com/openimsdk/open-im-server/v3/pkg/msgprocessor"
	"github.com/openimsdk/open-im-server/v3/pkg/tools/batcher"
	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	pbconv "github.com/openimsdk/open-im-server/v3/protocol/conversation"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/mcontext"
	"github.com/openimsdk/tools/utils/stringutil"
	"google.golang.org/protobuf/proto"
)

const (
	// 批处理大小，攒够这么多条就触发一次处理
	size = 1000
	// 子通道缓冲区大小
	subChanBuffer = 200
	// 处理间隔保持不变，更低的值会增加CPU使用率
	interval = 100 * time.Millisecond
	// 已读通道缓冲区大小
	hasReadChanBuffer = 10000
)

// worker / mainDataBuffer 决定了 msgtransfer 会用多大并发去打 Redis 和 MongoDB。
//
// 【重要】这两个值原来分别是 100 和 2000，注释写的是「提高并发处理能力」。
// 但 msgtransfer 的下游是数据库，把并发拉满等于把 MQ 的积压原样转嫁给数据库——
// MQ 堆积不致命，数据库被打爆才致命。压测中 MongoDB CPU 被打到 330%（共 4 核）
// 正是这个方向的结果。
//
// 现在默认降到 32，并且可以按机器规格用环境变量调：
//   MSGTRANSFER_WORKER、MSGTRANSFER_DATA_BUFFER
var (
	worker         = envInt("MSGTRANSFER_WORKER", 32)
	mainDataBuffer = envInt("MSGTRANSFER_DATA_BUFFER", 512)
)

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

type ContextMsg struct {
	message *sdkws.MsgData
	ctx     context.Context
}

// This structure is used for asynchronously writing the sender’s read sequence (seq) regarding a message into MongoDB.
// For example, if the sender sends a message with a seq of 10, then their own read seq for this conversation should be set to 10.
type userHasReadSeq struct {
	conversationID string
	userHasReadMap map[string]int64
}

type OnlineHistoryRedisConsumerHandler struct {
	redisMessageBatches *batcher.Batcher[ConsumerMessage]

	msgTransferDatabase         controller.MsgTransferDatabase
	conversationUserHasReadChan chan *userHasReadSeq
	wg                          sync.WaitGroup

	groupClient        *rpcli.GroupClient
	conversationClient *rpcli.ConversationClient
}

type ConsumerMessage struct {
	Ctx   context.Context
	Key   string
	Value []byte
}

func NewOnlineHistoryRedisConsumerHandler(ctx context.Context, client discovery.Conn, config *Config, database controller.MsgTransferDatabase) (*OnlineHistoryRedisConsumerHandler, error) {
	groupConn, err := client.GetConn(ctx, config.Discovery.RpcService.Group)
	if err != nil {
		return nil, err
	}
	conversationConn, err := client.GetConn(ctx, config.Discovery.RpcService.Conversation)
	if err != nil {
		return nil, err
	}
	var och OnlineHistoryRedisConsumerHandler
	och.msgTransferDatabase = database
	och.conversationUserHasReadChan = make(chan *userHasReadSeq, hasReadChanBuffer)
	och.groupClient = rpcli.NewGroupClient(groupConn)
	och.conversationClient = rpcli.NewConversationClient(conversationConn)
	och.wg.Add(1)

	b := batcher.New[ConsumerMessage](
		batcher.WithSize(size),
		batcher.WithWorker(worker),
		batcher.WithInterval(interval),
		batcher.WithDataBuffer(mainDataBuffer),
		batcher.WithSyncWait(true),
		batcher.WithBuffer(subChanBuffer),
	)
	b.Sharding = func(key string) int {
		hashCode := stringutil.GetHashCode(key)
		return int(hashCode) % och.redisMessageBatches.Worker()
	}
	b.Key = func(consumerMessage *ConsumerMessage) string {
		return consumerMessage.Key
	}
	b.Do = och.do
	och.redisMessageBatches = b

	return &och, nil
}
func (och *OnlineHistoryRedisConsumerHandler) do(ctx context.Context, channelID int, val *batcher.Msg[ConsumerMessage]) {
	ctx = mcontext.WithTriggerIDContext(ctx, val.TriggerID())
	ctxMessages := och.parseConsumerMessages(ctx, val.Val())
	ctx = withAggregationCtx(ctx, ctxMessages)
	log.ZInfo(ctx, "msg arrived channel", "channel id", channelID, "msgList length", len(ctxMessages), "key", val.Key())
	och.doSetReadSeq(ctx, ctxMessages)

	storageMsgList, notStorageMsgList, storageNotificationList, notStorageNotificationList :=
		och.categorizeMessageLists(ctxMessages)
	log.ZDebug(ctx, "number of categorized messages", "storageMsgList", len(storageMsgList), "notStorageMsgList",
		len(notStorageMsgList), "storageNotificationList", len(storageNotificationList), "notStorageNotificationList", len(notStorageNotificationList))

	conversationIDMsg := msgprocessor.GetChatConversationIDByMsg(ctxMessages[0].message)
	conversationIDNotification := msgprocessor.GetNotificationConversationIDByMsg(ctxMessages[0].message)
	och.handleMsg(ctx, val.Key(), conversationIDMsg, storageMsgList, notStorageMsgList)
	och.handleNotification(ctx, val.Key(), conversationIDNotification, storageNotificationList, notStorageNotificationList)
}

func (och *OnlineHistoryRedisConsumerHandler) doSetReadSeq(ctx context.Context, msgs []*ContextMsg) {

	var conversationID string
	var userSeqMap map[string]int64
	for _, msg := range msgs {
		if msg.message.ContentType != constant.HasReadReceipt {
			continue
		}
		var elem sdkws.NotificationElem
		if err := json.Unmarshal(msg.message.Content, &elem); err != nil {
			log.ZWarn(ctx, "handlerConversationRead Unmarshal NotificationElem msg err", err, "msg", msg)
			continue
		}
		var tips sdkws.MarkAsReadTips
		if err := json.Unmarshal([]byte(elem.Detail), &tips); err != nil {
			log.ZWarn(ctx, "handlerConversationRead Unmarshal MarkAsReadTips msg err", err, "msg", msg)
			continue
		}
		//The conversation ID for each batch of messages processed by the batcher is the same.
		conversationID = tips.ConversationID
		if len(tips.Seqs) > 0 {
			for _, seq := range tips.Seqs {
				if tips.HasReadSeq < seq {
					tips.HasReadSeq = seq
				}
			}
			clear(tips.Seqs)
			tips.Seqs = nil
		}
		if tips.HasReadSeq < 0 {
			continue
		}
		if userSeqMap == nil {
			userSeqMap = make(map[string]int64)
		}

		if userSeqMap[tips.MarkAsReadUserID] > tips.HasReadSeq {
			continue
		}
		userSeqMap[tips.MarkAsReadUserID] = tips.HasReadSeq
	}
	if userSeqMap == nil {
		return
	}
	if len(conversationID) == 0 {
		log.ZWarn(ctx, "conversation err", nil, "conversationID", conversationID)
	}
	if err := och.msgTransferDatabase.SetHasReadSeqToDB(ctx, conversationID, userSeqMap); err != nil {
		log.ZWarn(ctx, "set read seq to db error", err, "conversationID", conversationID, "userSeqMap", userSeqMap)
	}

}

func (och *OnlineHistoryRedisConsumerHandler) parseConsumerMessages(ctx context.Context, consumerMessages []*ConsumerMessage) []*ContextMsg {
	var ctxMessages []*ContextMsg
	for i := 0; i < len(consumerMessages); i++ {
		ctxMsg := &ContextMsg{}
		msgFromMQ := &sdkws.MsgData{}
		err := proto.Unmarshal(consumerMessages[i].Value, msgFromMQ)
		if err != nil {
			log.ZWarn(ctx, "msg_transfer Unmarshal msg err", err, string(consumerMessages[i].Value))
			continue
		}
		ctxMsg.ctx = consumerMessages[i].Ctx
		ctxMsg.message = msgFromMQ
		log.ZDebug(ctx, "message parse finish", "message", msgFromMQ, "key", consumerMessages[i].Key)
		ctxMessages = append(ctxMessages, ctxMsg)
	}
	return ctxMessages
}

// Get messages/notifications stored message list, not stored and pushed message list.
func (och *OnlineHistoryRedisConsumerHandler) categorizeMessageLists(totalMsgs []*ContextMsg) (storageMsgList,
	notStorageMsgList, storageNotificationList, notStorageNotificationList []*ContextMsg) {
	for _, v := range totalMsgs {
		options := msgprocessor.Options(v.message.Options)
		if !options.IsNotNotification() {
			// clone msg from notificationMsg；仅当需要下发给聊天会话时才写入并推送，避免大量系统通知刷屏
			if options.IsSendMsg() && msgprocessor.ShouldDeliverSystemMsgToChat(v.message) {
				msg := proto.Clone(v.message).(*sdkws.MsgData)
				// message
				if v.message.Options != nil {
					msg.Options = msgprocessor.NewMsgOptions()
				}
				msg.Options = msgprocessor.WithOptions(msg.Options,
					msgprocessor.WithOfflinePush(options.IsOfflinePush()),
					msgprocessor.WithUnreadCount(options.IsUnreadCount()),
				)
				v.message.Options = msgprocessor.WithOptions(
					v.message.Options,
					msgprocessor.WithOfflinePush(false),
					msgprocessor.WithUnreadCount(false),
				)
				ctxMsg := &ContextMsg{
					message: msg,
					ctx:     v.ctx,
				}
				storageMsgList = append(storageMsgList, ctxMsg)
			}
			if options.IsHistory() {
				storageNotificationList = append(storageNotificationList, v)
			} else {
				notStorageNotificationList = append(notStorageNotificationList, v)
			}
		} else {
			if options.IsHistory() {
				storageMsgList = append(storageMsgList, v)
			} else {
				notStorageMsgList = append(notStorageMsgList, v)
			}
		}
	}
	return
}

func (och *OnlineHistoryRedisConsumerHandler) handleMsg(ctx context.Context, key, conversationID string, storageList, notStorageList []*ContextMsg) {
	log.ZInfo(ctx, "handle storage msg")
	for _, storageMsg := range storageList {
		log.ZDebug(ctx, "handle storage msg", "msg", storageMsg.message.String())
	}

	och.toPushTopic(ctx, key, conversationID, notStorageList)
	var storageMessageList []*sdkws.MsgData
	for _, msg := range storageList {
		storageMessageList = append(storageMessageList, msg.message)
	}
	if len(storageMessageList) > 0 {
		msg := storageMessageList[0]
		lastSeq, isNewConversation, userSeqMap, err := och.msgTransferDatabase.BatchInsertChat2Cache(ctx, conversationID, storageMessageList)
		if err != nil && !errors.Is(errs.Unwrap(err), redis.Nil) {
			log.ZWarn(ctx, "batch data insert to redis err", err, "storageMsgList", storageMessageList)
			return
		}
		log.ZInfo(ctx, "BatchInsertChat2Cache end")
		err = och.msgTransferDatabase.SetHasReadSeqs(ctx, conversationID, userSeqMap)
		if err != nil {
			log.ZWarn(ctx, "SetHasReadSeqs error", err, "userSeqMap", userSeqMap, "conversationID", conversationID)
			prommetrics.SeqSetFailedCounter.Inc()
		}
		och.conversationUserHasReadChan <- &userHasReadSeq{
			conversationID: conversationID,
			userHasReadMap: userSeqMap,
		}

		if isNewConversation {
			switch msg.SessionType {
			case constant.ReadGroupChatType:
				log.ZDebug(ctx, "group chat first create conversation", "conversationID",
					conversationID)

				userIDs, err := och.groupClient.GetGroupMemberUserIDs(ctx, msg.GroupID)
				if err != nil {
					log.ZWarn(ctx, "get group member ids error", err, "conversationID",
						conversationID)
				} else {
					log.ZInfo(ctx, "GetGroupMemberIDs end")

					if err := och.conversationClient.CreateGroupChatConversations(ctx, msg.GroupID, userIDs); err != nil {
						log.ZWarn(ctx, "single chat first create conversation error", err,
							"conversationID", conversationID)
					}
				}
			case constant.SingleChatType, constant.NotificationChatType:
				req := &pbconv.CreateSingleChatConversationsReq{
					RecvID:           msg.RecvID,
					SendID:           msg.SendID,
					ConversationID:   conversationID,
					ConversationType: msg.SessionType,
				}
				if err := och.conversationClient.CreateSingleChatConversations(ctx, req); err != nil {
					log.ZWarn(ctx, "single chat or notification first create conversation error", err,
						"conversationID", conversationID, "sessionType", msg.SessionType)
				}
			default:
				log.ZWarn(ctx, "unknown session type", nil, "sessionType",
					msg.SessionType)
			}
		}

		log.ZInfo(ctx, "success incr to next topic")
		err = och.msgTransferDatabase.MsgToMongoMQ(ctx, key, conversationID, storageMessageList, lastSeq)
		if err != nil {
			log.ZError(ctx, "Msg To MongoDB MQ error", err, "conversationID",
				conversationID, "storageList", storageMessageList, "lastSeq", lastSeq)
		}
		log.ZInfo(ctx, "MsgToMongoMQ end")

		och.toPushTopic(ctx, key, conversationID, storageList)
		log.ZInfo(ctx, "toPushTopic end")
	}
}

func (och *OnlineHistoryRedisConsumerHandler) handleNotification(ctx context.Context, key, conversationID string,
	storageList, notStorageList []*ContextMsg) {
	och.toPushTopic(ctx, key, conversationID, notStorageList)
	var storageMessageList []*sdkws.MsgData
	for _, msg := range storageList {
		storageMessageList = append(storageMessageList, msg.message)
	}
	if len(storageMessageList) > 0 {
		lastSeq, _, _, err := och.msgTransferDatabase.BatchInsertChat2Cache(ctx, conversationID, storageMessageList)
		if err != nil {
			log.ZError(ctx, "notification batch insert to redis error", err, "conversationID", conversationID,
				"storageList", storageMessageList)
			return
		}
		log.ZDebug(ctx, "success to next topic", "conversationID", conversationID)
		err = och.msgTransferDatabase.MsgToMongoMQ(ctx, key, conversationID, storageMessageList, lastSeq)
		if err != nil {
			log.ZError(ctx, "Msg To MongoDB MQ error", err, "conversationID",
				conversationID, "storageList", storageMessageList, "lastSeq", lastSeq)
		}
		och.toPushTopic(ctx, key, conversationID, storageList)
	}
}
func (och *OnlineHistoryRedisConsumerHandler) HandleUserHasReadSeqMessages(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.ZPanic(ctx, "HandleUserHasReadSeqMessages Panic", errs.ErrPanic(r))
		}
	}()

	defer och.wg.Done()

	for msg := range och.conversationUserHasReadChan {
		if err := och.msgTransferDatabase.SetHasReadSeqToDB(ctx, msg.conversationID, msg.userHasReadMap); err != nil {
			log.ZWarn(ctx, "set read seq to db error", err, "conversationID", msg.conversationID, "userSeqMap", msg.userHasReadMap)
		}
	}

	log.ZInfo(ctx, "Channel closed, exiting handleUserHasReadSeqMessages")
}
func (och *OnlineHistoryRedisConsumerHandler) Close() {
	close(och.conversationUserHasReadChan)
	och.wg.Wait()
}

// toPushTopic 把一批消息投递到 toPush。
//
// 【性能】原来是对 batcher 攒出来的每一条消息各 produce 一次，
// batcher 好不容易把 1000 条聚成一批，到这里又拆成 1000 次 Kafka produce，
// 下游 push 消费端也就要逐条 poll、逐条解包、逐条取群成员、逐条查会话免打扰。
// 现在整批发一条：MQ 消息数从「每条消息一条」降到「每批一条」。
//
// 同一批消息的 conversationID 相同（batcher 按会话分片），所以可以安全合并。
// 通过 PUSH_BATCH_DISABLE=1 可以退回逐条模式做对照。
func (och *OnlineHistoryRedisConsumerHandler) toPushTopic(ctx context.Context, key, conversationID string, msgs []*ContextMsg) {
	if len(msgs) == 0 {
		return
	}
	if pushBatchDisabled() {
		for _, v := range msgs {
			if err := och.msgTransferDatabase.MsgToPushMQ(v.ctx, key, conversationID, v.message); err != nil {
				log.ZError(ctx, "MsgToPushMQ error", err, "msg", v.message.String())
			}
		}
		return
	}

	batch := make([]*sdkws.MsgData, 0, len(msgs))
	for _, v := range msgs {
		batch = append(batch, v.message)
	}
	// 用批内第一条消息的 ctx，保留 operationID 便于串起链路日志
	if err := och.msgTransferDatabase.MsgToPushMQBatch(msgs[0].ctx, key, conversationID, batch); err != nil {
		log.ZError(ctx, "MsgToPushMQBatch error", err, "conversationID", conversationID, "count", len(batch))
	}
}

func pushBatchDisabled() bool {
	return os.Getenv("PUSH_BATCH_DISABLE") == "1"
}

func withAggregationCtx(ctx context.Context, values []*ContextMsg) context.Context {
	var allMessageOperationID string
	for i, v := range values {
		if opid := mcontext.GetOperationID(v.ctx); opid != "" {
			if i == 0 {
				allMessageOperationID += opid
			} else {
				allMessageOperationID += "$" + opid
			}
		}
	}
	return mcontext.SetOperationID(ctx, allMessageOperationID)
}

func (och *OnlineHistoryRedisConsumerHandler) HandlerRedisMessage(ctx context.Context, key string, value []byte) error { // a instance in the consumer group
	err := och.redisMessageBatches.Put(ctx, &ConsumerMessage{Ctx: ctx, Key: key, Value: value})
	if err != nil {
		log.ZWarn(ctx, "put msg to  error", err, "key", key, "value", value)
	}
	return nil
}
