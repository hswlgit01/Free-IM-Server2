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
	"fmt"

	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/cache"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/cache/mcache"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/cache/redis"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/database/mgo"
	"github.com/openimsdk/open-im-server/v3/pkg/dbbuild"
	"github.com/openimsdk/open-im-server/v3/pkg/mqbuild"
	"github.com/openimsdk/open-im-server/v3/pkg/util/crypto"
	"github.com/openimsdk/tools/discovery"
	"github.com/openimsdk/tools/mq"
	"github.com/openimsdk/tools/utils/runtimeenv"

	conf "github.com/openimsdk/open-im-server/v3/pkg/common/config"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/controller"
	"github.com/openimsdk/tools/log"
	"google.golang.org/grpc"
)

type MsgTransfer struct {
	historyConsumer      mq.Consumer
	historyMongoConsumer mq.Consumer
	// historyHandler 负责消费「写入 Redis + 推送」的消息：
	//   1）订阅 toRedis topic，从 MQ 拉取在线/离线消息；
	//   2）写入 Redis、更新 seq、组装推送任务；
	//   3）将消息继续投递到 toPush / toMongo 等 topic。
	historyHandler *OnlineHistoryRedisConsumerHandler
	// historyMongoHandler 负责消费「写入 MongoDB」的消息，实现历史消息持久化
	historyMongoHandler *OnlineHistoryMongoConsumerHandler
	ctx                 context.Context
	//cancel              context.CancelFunc
}

type Config struct {
	MsgTransfer    conf.MsgTransfer
	RedisConfig    conf.Redis
	MongodbConfig  conf.Mongo
	KafkaConfig    conf.Kafka
	Share          conf.Share
	WebhooksConfig conf.Webhooks
	Discovery      conf.Discovery
	Index          conf.Index
}

func Start(ctx context.Context, config *Config, client discovery.Conn, server grpc.ServiceRegistrar) error {
	// 1. 初始化加解密密钥（用于消息落库前后的加密解密等场景）
	if err := crypto.InitServiceKey(crypto.TransferKey); err != nil {
		log.ZError(ctx, "初始化密钥系统失败: %v\n", err)
		return err
	}

	// 2. 构建 MQ 生产者/消费者，用于连接 Kafka
	builder := mqbuild.NewBuilder(&config.KafkaConfig)

	// 3. 打印启动信息：运行环境、Prometheus 端口、索引配置
	log.CInfo(ctx, "MSG-TRANSFER server is initializing", "runTimeEnv", runtimeenv.RuntimeEnvironment(), "prometheusPorts",
		config.MsgTransfer.Prometheus.Ports, "index", config.Index)

	// 4. 初始化 MongoDB + Redis 客户端
	dbb := dbbuild.NewBuilder(&config.MongodbConfig, &config.RedisConfig)
	mgocli, err := dbb.Mongo(ctx)
	if err != nil {
		return err
	}
	rdb, err := dbb.Redis(ctx)
	if err != nil {
		return err
	}

	//if config.Discovery.Enable == conf.ETCD {
	//	cm := disetcd.NewConfigManager(client.(*etcd.SvcDiscoveryRegistryImpl).GetClient(), []string{
	//		config.MsgTransfer.GetConfigFileName(),
	//		config.RedisConfig.GetConfigFileName(),
	//		config.MongodbConfig.GetConfigFileName(),
	//		config.KafkaConfig.GetConfigFileName(),
	//		config.Share.GetConfigFileName(),
	//		config.WebhooksConfig.GetConfigFileName(),
	//		config.Discovery.GetConfigFileName(),
	//		conf.LogConfigFileName,
	//	})
	//	cm.Watch(ctx)
	//}
	// 5. 初始化 Mongo 专用 Producer（用于向 toMongo topic 投递消息，做历史持久化）
	mongoProducer, err := builder.GetTopicProducer(ctx, config.KafkaConfig.ToMongoTopic)
	if err != nil {
		return err
	}
	// 6. 初始化 Push 专用 Producer（用于向 toPush topic 投递消息，做在线/离线推送）
	pushProducer, err := builder.GetTopicProducer(ctx, config.KafkaConfig.ToPushTopic)
	if err != nil {
		return err
	}
	// 7. 创建消息文档模型（Mongo 消息集合的封装）
	msgDocModel, err := mgo.NewMsgMongo(mgocli.GetDB())
	if err != nil {
		return err
	}
	// 8. 构建消息缓存模型：
	//    - 如果未启用 Redis，则使用 Mongo 作为缓存后端（mcache）
	//    - 如果启用 Redis，则使用 Redis + Mongo 的组合缓存
	var msgModel cache.MsgCache
	if rdb == nil {
		cm, err := mgo.NewCacheMgo(mgocli.GetDB())
		if err != nil {
			return err
		}
		msgModel = mcache.NewMsgCache(cm, msgDocModel)
	} else {
		msgModel = redis.NewMsgCache(rdb, msgDocModel)
	}
	// 9. 初始化会话 seq 相关存储（每个会话的最小/最大 seq）
	seqConversation, err := mgo.NewSeqConversationMongo(mgocli.GetDB())
	if err != nil {
		return err
	}
	seqConversationCache := redis.NewSeqConversationCacheRedis(rdb, seqConversation)
	// 10. 初始化用户 seq 相关存储（用户在每个会话中的最小/最大可见 seq）
	seqUser, err := mgo.NewSeqUserMongo(mgocli.GetDB())
	if err != nil {
		return err
	}
	seqUserCache := redis.NewSeqUserCacheRedis(rdb, seqUser)
	// 11. 组装 MsgTransferDatabase：
	//     - 负责对消息文档、缓存、seq、Kafka Producer 进行统一封装，
	//       提供给历史消息处理链路（写 Redis / Mongo / 更新 seq / 推送）。
	msgTransferDatabase, err := controller.NewMsgTransferDatabase(msgDocModel, msgModel, seqUserCache, seqConversationCache, mongoProducer, pushProducer)
	if err != nil {
		return err
	}
	// 12. 创建两个 MQ Consumer：
	//     - historyConsumer：订阅 toRedis topic，用于在线/离线消息 + Redis/推送处理
	//     - historyMongoConsumer：订阅 toMongo topic，用于历史消息持久化到 Mongo
	historyConsumer, err := builder.GetTopicConsumer(ctx, config.KafkaConfig.ToRedisTopic)
	if err != nil {
		return err
	}
	historyMongoConsumer, err := builder.GetTopicConsumer(ctx, config.KafkaConfig.ToMongoTopic)
	if err != nil {
		return err
	}
	// 13. 初始化 Redis 路径的消息处理器（含批处理逻辑）
	//     - 负责：解析 MQ 消息 → 分类存储/推送 → 更新已读 seq 等。
	historyHandler, err := NewOnlineHistoryRedisConsumerHandler(ctx, client, config, msgTransferDatabase)
	if err != nil {
		return err
	}
	// 14. 初始化 Mongo 路径的消息处理器
	//     - 负责：将 MQ 中的消息写入 Mongo，形成可查询的历史消息。
	historyMongoHandler := NewOnlineHistoryMongoConsumerHandler(msgTransferDatabase)

	msgTransfer := &MsgTransfer{
		historyConsumer:      historyConsumer,
		historyMongoConsumer: historyMongoConsumer,
		historyHandler:       historyHandler,
		historyMongoHandler:  historyMongoHandler,
	}

	return msgTransfer.Start(ctx)
}

func (m *MsgTransfer) Start(ctx context.Context) error {
	var cancel context.CancelCauseFunc
	m.ctx, cancel = context.WithCancelCause(ctx)

	// 1. 启动协程订阅 toRedis topic：
	//    - 收到消息后交给 historyHandler.HandlerRedisMessage 做批处理（写 Redis / 推送 / seq 等）
	go func() {
		for {
			if err := m.historyConsumer.Subscribe(m.ctx, m.historyHandler.HandlerRedisMessage); err != nil {
				cancel(fmt.Errorf("history consumer %w", err))
				log.ZError(m.ctx, "historyConsumer err", err)
				return
			}
		}
	}()

	// 2. 启动协程订阅 toMongo topic：
	//    - 收到消息后交给 historyMongoHandler.HandleChatWs2Mongo 落库到 Mongo
	go func() {
		fn := func(ctx context.Context, key string, value []byte) error {
			m.historyMongoHandler.HandleChatWs2Mongo(ctx, key, value)
			return nil
		}
		for {
			if err := m.historyMongoConsumer.Subscribe(m.ctx, fn); err != nil {
				cancel(fmt.Errorf("history mongo consumer %w", err))
				log.ZError(m.ctx, "historyMongoConsumer err", err)
				return
			}
		}
	}()

	// 3. 启动处理「用户已读 seq 更新」的后台协程：
	//    - 将批量的已读回执异步写入 Mongo/Redis，避免阻塞主流程。
	go m.historyHandler.HandleUserHasReadSeqMessages(m.ctx)

	// 4. 启动批处理器（Batcher）：
	//    - 负责对来自 MQ 的消息做按 key 分片 + 批量回调（提高写入/推送吞吐量）。
	err := m.historyHandler.redisMessageBatches.Start()
	if err != nil {
		return err
	}
	// 5. 阻塞等待 ctx 被取消（服务关闭）并返回关闭原因
	<-m.ctx.Done()
	return context.Cause(m.ctx)
}
