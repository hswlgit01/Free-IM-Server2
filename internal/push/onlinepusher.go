package push

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msggateway"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/discovery"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/mcontext"
	"github.com/openimsdk/tools/utils/datautil"
	"github.com/openimsdk/tools/utils/runtimeenv"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	conf "github.com/openimsdk/open-im-server/v3/pkg/common/config"
)

type OnlinePusher interface {
	GetConnsAndOnlinePush(ctx context.Context, msg *sdkws.MsgData,
		pushToUserIDs []string) (wsResults []*msggateway.SingleMsgToUserResults, err error)
	GetOnlinePushFailedUserIDs(ctx context.Context, msg *sdkws.MsgData, wsResults []*msggateway.SingleMsgToUserResults,
		pushToUserIDs *[]string) []string
}

type emptyOnlinePusher struct{}

func newEmptyOnlinePusher() *emptyOnlinePusher {
	return &emptyOnlinePusher{}
}

func (emptyOnlinePusher) GetConnsAndOnlinePush(ctx context.Context, msg *sdkws.MsgData, pushToUserIDs []string) (wsResults []*msggateway.SingleMsgToUserResults, err error) {
	log.ZInfo(ctx, "emptyOnlinePusher GetConnsAndOnlinePush", nil)
	return nil, nil
}
func (u emptyOnlinePusher) GetOnlinePushFailedUserIDs(ctx context.Context, msg *sdkws.MsgData, wsResults []*msggateway.SingleMsgToUserResults, pushToUserIDs *[]string) []string {
	log.ZInfo(ctx, "emptyOnlinePusher GetOnlinePushFailedUserIDs", nil)
	return nil
}

func NewOnlinePusher(disCov discovery.Conn, config *Config) (OnlinePusher, error) {
	if conf.Standalone() {
		return NewDefaultAllNode(disCov, config), nil
	}
	if runtimeenv.RuntimeEnvironment() == conf.KUBERNETES {
		return NewDefaultAllNode(disCov, config), nil
	}
	switch config.Discovery.Enable {
	case conf.ETCD:
		return NewDefaultAllNode(disCov, config), nil
	default:
		return nil, errs.New(fmt.Sprintf("unsupported discovery type %s", config.Discovery.Enable))
	}
}

type DefaultAllNode struct {
	disCov discovery.Conn
	config *Config
}

func NewDefaultAllNode(disCov discovery.Conn, config *Config) *DefaultAllNode {
	return &DefaultAllNode{disCov: disCov, config: config}
}

func (d *DefaultAllNode) GetConnsAndOnlinePush(ctx context.Context, msg *sdkws.MsgData,
	pushToUserIDs []string) (wsResults []*msggateway.SingleMsgToUserResults, err error) {
	// 对空用户列表进行快速处理
	if len(pushToUserIDs) == 0 {
		return nil, nil
	}

	// 对大型群组的读回执消息进行特殊优化处理
	if msg.ContentType == constant.HasReadReceipt && msg.SessionType == constant.ReadGroupChatType && len(pushToUserIDs) > 100 {
		return d.batchPushReadReceipt(ctx, msg, pushToUserIDs)
	}

	// 对一般消息的优化：大型群组使用批处理
	if msg.SessionType == constant.ReadGroupChatType && len(pushToUserIDs) > 200 {
		return d.batchPushMessage(ctx, msg, pushToUserIDs)
	}

	// 获取网关连接
	conns, err := d.disCov.GetConns(ctx, d.config.Discovery.RpcService.MessageGateway)
	if len(conns) == 0 {
		// 只在非读回执消息时记录警告，避免大量警告日志
		if msg.ContentType != constant.HasReadReceipt {
			log.ZWarn(ctx, "get gateway conn 0 ", nil)
		}
		return nil, err
	}

	if err != nil {
		return nil, err
	}

	// 快速路径：使用第一个连接发送消息
	// 这避免了同步等待多个连接的响应，减少延迟
	if len(conns) > 0 && len(pushToUserIDs) < 100 {
		conn := conns[0]
		msgClient := msggateway.NewMsgGatewayClient(conn)

		// 设置较短的超时时间，避免阻塞太长
		ctxWithTimeout, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()

		input := &msggateway.OnlineBatchPushOneMsgReq{MsgData: msg, PushToUserIDs: pushToUserIDs}
		reply, err := msgClient.SuperGroupOnlineBatchPushOneMsg(ctxWithTimeout, input)
		if err != nil {
			log.ZWarn(ctx, "Fast path push failed, falling back to parallel push", err)
		} else if reply != nil && reply.SinglePushResult != nil {
			return reply.SinglePushResult, nil
		}
	}

	// 慢路径：并行使用所有连接
	var (
		mu         sync.Mutex
		wg         = errgroup.Group{}
		input      = &msggateway.OnlineBatchPushOneMsgReq{MsgData: msg, PushToUserIDs: pushToUserIDs}
		maxWorkers = d.config.RpcConfig.MaxConcurrentWorkers
	)

	if maxWorkers < 3 {
		maxWorkers = 3
	}

	wg.SetLimit(maxWorkers)

	for _, conn := range conns {
		conn := conn // loop var safe
		wg.Go(func() error {
			// 使用独立上下文避免取消传播
			pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// 复制操作ID
			if opID := mcontext.GetOperationID(ctx); opID != "" {
				pushCtx = mcontext.SetOperationID(pushCtx, opID)
			}

			msgClient := msggateway.NewMsgGatewayClient(conn)
			reply, err := msgClient.SuperGroupOnlineBatchPushOneMsg(pushCtx, input)
			if err != nil {
				log.ZWarn(pushCtx, "SuperGroupOnlineBatchPushOneMsg ", err)
				return nil
			}

			if reply != nil && reply.SinglePushResult != nil {
				mu.Lock()
				wsResults = append(wsResults, reply.SinglePushResult...)
				mu.Unlock()
			}

			return nil
		})
	}

	_ = wg.Wait()

	return wsResults, nil
}

// 批量处理一般消息，针对大型群组
func (d *DefaultAllNode) batchPushMessage(ctx context.Context, msg *sdkws.MsgData, pushToUserIDs []string) ([]*msggateway.SingleMsgToUserResults, error) {
	// 获取网关连接
	conns, err := d.disCov.GetConns(ctx, d.config.Discovery.RpcService.MessageGateway)
	if err != nil {
		return nil, err
	}

	if len(conns) == 0 {
		log.ZWarn(ctx, "batchPushMessage: 获取网关连接失败，连接数为0", nil)
		return nil, nil
	}

	// 消息批处理策略
	batchSize := 100 // 默认批次大小
	if len(pushToUserIDs) > 3000 {
		batchSize = 200 // 大型群组使用更大的批次
	} else if len(pushToUserIDs) > 1000 {
		batchSize = 150 // 中型群组
	}

	batches := make([][]string, 0, (len(pushToUserIDs)+batchSize-1)/batchSize)
	for i := 0; i < len(pushToUserIDs); i += batchSize {
		end := i + batchSize
		if end > len(pushToUserIDs) {
			end = len(pushToUserIDs)
		}
		batches = append(batches, pushToUserIDs[i:end])
	}

	var (
		mu         sync.Mutex
		wg         = errgroup.Group{}
		allResults []*msggateway.SingleMsgToUserResults
		maxWorkers = d.config.RpcConfig.MaxConcurrentWorkers
	)

	if maxWorkers < 5 {
		maxWorkers = 5
	}

	wg.SetLimit(maxWorkers)

	// 处理每个批次
	log.ZInfo(ctx, "batchPushMessage: 开始批量推送消息", "总用户数", len(pushToUserIDs), "批次数", len(batches))

	// 选择第一个可用连接
	conn := conns[0]

	for batchIndex, batch := range batches {
		bIndex := batchIndex
		userBatch := batch

		wg.Go(func() error {
			// 使用独立上下文
			batchCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			// 复制操作ID
			if opID := mcontext.GetOperationID(ctx); opID != "" {
				batchCtx = mcontext.SetOperationID(batchCtx, opID)
			}

			input := &msggateway.OnlineBatchPushOneMsgReq{
				MsgData:       msg,
				PushToUserIDs: userBatch,
			}

			// 使用选定的连接
			msgClient := msggateway.NewMsgGatewayClient(conn)
			reply, err := msgClient.SuperGroupOnlineBatchPushOneMsg(batchCtx, input)

			if err != nil {
				log.ZWarn(batchCtx, "batchPushMessage: 批次推送失败", err,
					"批次", bIndex)
				return nil
			}

			if reply != nil && reply.SinglePushResult != nil {
				mu.Lock()
				allResults = append(allResults, reply.SinglePushResult...)
				mu.Unlock()
				log.ZDebug(batchCtx, "batchPushMessage: 批次推送成功",
					"批次", bIndex,
					"用户数", len(userBatch),
					"成功数", len(reply.SinglePushResult))
			}

			return nil
		})
	}

	_ = wg.Wait()

	log.ZInfo(ctx, "batchPushMessage: 批量推送消息完成",
		"总用户数", len(pushToUserIDs),
		"结果数", len(allResults))

	return allResults, nil
}

// 批量处理大型群组的读回执消息
func (d *DefaultAllNode) batchPushReadReceipt(ctx context.Context, msg *sdkws.MsgData, pushToUserIDs []string) ([]*msggateway.SingleMsgToUserResults, error) {
	// 获取网关连接
	conns, err := d.disCov.GetConns(ctx, d.config.Discovery.RpcService.MessageGateway)
	if err != nil {
		return nil, err
	}

	if len(conns) == 0 {
		log.ZWarn(ctx, "batchPushReadReceipt: 获取网关连接失败，连接数为0", nil)
		return nil, nil
	}

	// 更激进的批处理策略：大型群组使用更大的批次
	// 对于1000人以上的群组，使用更大的批次以减少RPC调用次数
	batchSize := 200 // 增加默认批次大小为200
	if len(pushToUserIDs) > 3000 {
		batchSize = 300 // 3000人以上的群组使用300的批次大小
	} else if len(pushToUserIDs) > 1000 {
		batchSize = 250 // 1000-3000人的群组使用250的批次大小
	}

	batches := make([][]string, 0, (len(pushToUserIDs)+batchSize-1)/batchSize)
	for i := 0; i < len(pushToUserIDs); i += batchSize {
		end := i + batchSize
		if end > len(pushToUserIDs) {
			end = len(pushToUserIDs)
		}
		batches = append(batches, pushToUserIDs[i:end])
	}

	var (
		mu           sync.Mutex
		wg           = errgroup.Group{}
		allResults   []*msggateway.SingleMsgToUserResults
		maxWorkers   = d.config.RpcConfig.MaxConcurrentWorkers
		batchTimeout = 5 * time.Second // 增加超时时间，避免因网络波动而失败
	)

	// 增加工作线程数，提高并行处理能力
	if maxWorkers < 5 {
		maxWorkers = 5
	}

	wg.SetLimit(maxWorkers)

	// 处理每个批次
	log.ZInfo(ctx, "batchPushReadReceipt: 开始批量推送读回执", "总用户数", len(pushToUserIDs), "批次数", len(batches), "批次大小", batchSize)

	// 为每个批次分配一个优先级
	// 优先处理在线用户和小批次
	for batchIndex, batch := range batches {
		bIndex := batchIndex
		userBatch := batch

		// 添加小延迟避免所有请求同时发出
		time.Sleep(time.Millisecond * 10)

		wg.Go(func() error {
			// 使用独立上下文
			batchCtx, cancel := context.WithTimeout(context.Background(), batchTimeout)
			defer cancel()

			// 从原始上下文复制操作ID
			if opID := mcontext.GetOperationID(ctx); opID != "" {
				batchCtx = mcontext.SetOperationID(batchCtx, opID)
			}

			input := &msggateway.OnlineBatchPushOneMsgReq{
				MsgData:       msg,
				PushToUserIDs: userBatch,
			}

			// 简化连接逻辑：使用第一个可用连接
			// 这避免了多次尝试连接的延迟
			var batchResults []*msggateway.SingleMsgToUserResults
			if len(conns) > 0 {
				conn := conns[0]
				msgClient := msggateway.NewMsgGatewayClient(conn)

				// 使用SuperGroupOnlineBatchPushOneMsg替代OnlineBatchPushOneMsg
				// 这个API对大型群组有更好的处理能力
				reply, err := msgClient.SuperGroupOnlineBatchPushOneMsg(batchCtx, input)

				if err == nil && reply != nil && reply.SinglePushResult != nil {
					batchResults = reply.SinglePushResult
					log.ZDebug(ctx, "batchPushReadReceipt: 批次推送成功",
						"批次", bIndex,
						"用户数", len(userBatch),
						"成功数", len(batchResults))
				} else {
					log.ZDebug(ctx, "batchPushReadReceipt: 网关推送失败", err,
						"批次", bIndex)
				}
			}

			// 记录此批次的结果
			if len(batchResults) > 0 {
				mu.Lock()
				allResults = append(allResults, batchResults...)
				mu.Unlock()
			}

			return nil
		})
	}

	// 等待所有批次处理完成，但不要因为部分失败而中断整个过程
	_ = wg.Wait()

	log.ZInfo(ctx, "batchPushReadReceipt: 批量推送读回执完成",
		"总用户数", len(pushToUserIDs),
		"结果数", len(allResults))

	return allResults, nil
}

func (d *DefaultAllNode) GetOnlinePushFailedUserIDs(_ context.Context, msg *sdkws.MsgData,
	wsResults []*msggateway.SingleMsgToUserResults, pushToUserIDs *[]string) []string {

	onlineSuccessUserIDs := []string{msg.SendID}
	for _, v := range wsResults {
		//message sender do not need offline push
		if msg.SendID == v.UserID {
			continue
		}
		// mobile online push success
		if v.OnlinePush {
			onlineSuccessUserIDs = append(onlineSuccessUserIDs, v.UserID)
		}

	}

	return datautil.SliceSub(*pushToUserIDs, onlineSuccessUserIDs)
}

type K8sStaticConsistentHash struct {
	disCov discovery.SvcDiscoveryRegistry
	config *Config
}

func NewK8sStaticConsistentHash(disCov discovery.SvcDiscoveryRegistry, config *Config) *K8sStaticConsistentHash {
	return &K8sStaticConsistentHash{disCov: disCov, config: config}
}

func (k *K8sStaticConsistentHash) GetConnsAndOnlinePush(ctx context.Context, msg *sdkws.MsgData,
	pushToUserIDs []string) (wsResults []*msggateway.SingleMsgToUserResults, err error) {

	var usersHost = make(map[string][]string)
	for _, v := range pushToUserIDs {
		tHost, err := k.disCov.GetUserIdHashGatewayHost(ctx, v)
		if err != nil {
			log.ZError(ctx, "get msg gateway hash error", err)
			return nil, err
		}
		tUsers, tbl := usersHost[tHost]
		if tbl {
			tUsers = append(tUsers, v)
			usersHost[tHost] = tUsers
		} else {
			usersHost[tHost] = []string{v}
		}
	}
	log.ZDebug(ctx, "genUsers send hosts struct:", "usersHost", usersHost)
	var usersConns = make(map[grpc.ClientConnInterface][]string)
	for host, userIds := range usersHost {
		tconn, _ := k.disCov.GetConn(ctx, host)
		usersConns[tconn] = userIds
	}
	var (
		mu         sync.Mutex
		wg         = errgroup.Group{}
		maxWorkers = k.config.RpcConfig.MaxConcurrentWorkers
	)
	if maxWorkers < 3 {
		maxWorkers = 3
	}
	wg.SetLimit(maxWorkers)
	for conn, userIds := range usersConns {
		tcon := conn
		tuserIds := userIds
		wg.Go(func() error {
			input := &msggateway.OnlineBatchPushOneMsgReq{MsgData: msg, PushToUserIDs: tuserIds}
			msgClient := msggateway.NewMsgGatewayClient(tcon)
			reply, err := msgClient.SuperGroupOnlineBatchPushOneMsg(ctx, input)
			if err != nil {
				return nil
			}
			log.ZDebug(ctx, "push result", "reply", reply)
			if reply != nil && reply.SinglePushResult != nil {
				mu.Lock()
				wsResults = append(wsResults, reply.SinglePushResult...)
				mu.Unlock()
			}
			return nil
		})
	}
	_ = wg.Wait()
	return wsResults, nil
}
func (k *K8sStaticConsistentHash) GetOnlinePushFailedUserIDs(_ context.Context, _ *sdkws.MsgData,
	wsResults []*msggateway.SingleMsgToUserResults, _ *[]string) []string {
	var needOfflinePushUserIDs []string
	for _, v := range wsResults {
		if !v.OnlinePush {
			needOfflinePushUserIDs = append(needOfflinePushUserIDs, v.UserID)
		}
	}
	return needOfflinePushUserIDs
}
