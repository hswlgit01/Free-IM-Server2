package msg_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/msg"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// 端到端验证客户反馈的 #4：
// 「业务员添加了好友，解除好友以后，还是可以继续聊天」。
//
// 完整复现客户描述的顺序：
//
//	建两个号 -> 互加好友 -> 双向都能发 -> 业务员单方面解除 -> 再发
//
// 最后一步在修复前会成功（这就是 BUG），修复后应被 ErrNotPeersFriend 拦下。
//
// 【为什么发消息要直连 gRPC 而不走 HTTP】
// /msg/send_msg 是管理员专用接口，而管理员消息会被 CheckSystemAccount
// 提前放行、根本进不了好友校验；普通用户的真实发送走 WebSocket。
// 两条路都验不到这段逻辑，所以直接压 msg RPC —— messageVerification
// 就在这一层，正是本次改动所在。
//
// 需要的环境变量（缺任一则跳过，CI 上不会失败）：
//
//	MSG_RPC_ADDR   msg RPC 地址，如 127.0.0.1:44425（可用 SSH 隧道转发）
//	CHAT_BASE      chat-api 地址，默认 http://127.0.0.1:10008
//	IM_BASE        openim-api 地址，默认 http://127.0.0.1:10002
//	ORG_ENV_FILE   含 ORG_ID / ORG_AES_KEY 两行的文件（密钥不走命令行，避免留在 shell 历史里）
func TestFriendVerifyAfterUnfriendLive(t *testing.T) {
	rpcAddr := os.Getenv("MSG_RPC_ADDR")
	orgID, aesKey := loadOrgEnv(os.Getenv("ORG_ENV_FILE"))
	if rpcAddr == "" || orgID == "" || aesKey == "" {
		t.Skip("未设置 MSG_RPC_ADDR / ORG_ENV_FILE，跳过实环境验证")
	}
	chatBase := envOr("CHAT_BASE", "http://127.0.0.1:10008")
	imBase := envOr("IM_BASE", "http://127.0.0.1:10002")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	conn, err := grpc.NewClient(rpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("连接 msg RPC 失败: %v", err)
	}
	defer conn.Close()
	msgCli := msg.NewMsgClient(conn)

	stamp := time.Now().Format("150405")
	env := &liveEnv{t: t, chatBase: chatBase, imBase: imBase, orgID: orgID, aesKey: aesKey}

	sales := env.login("fv-sales-"+stamp, "fvS"+stamp)
	// 两次登录之间必须拉开间隔：测试环境的组织缓存里 aesKeyBase64 是空的，
	// 短时间内的第二次登录会命中它并报 "invalid key size 0"，
	// 等缓存过期重新读库才正常。这是环境的毛病，与被验证的功能无关。
	time.Sleep(15 * time.Second)
	cust := env.login("fv-cust-"+stamp, "fvC"+stamp)
	t.Logf("业务员 %s，用户 %s", sales.imUserID, cust.imUserID)

	// ---------- 1. 互加好友 ----------
	env.befriend(sales, cust)
	t.Logf("互加后的好友关系: %s", env.friendState(sales, cust))

	// ---------- 2. 互为好友时双向都能发 ----------
	assertSend(t, ctx, msgCli, sales, cust, false, "互为好友：业务员 -> 用户")
	assertSend(t, ctx, msgCli, cust, sales, false, "互为好友：用户 -> 业务员")

	// ---------- 3. 业务员单方面解除好友 ----------
	// DeleteFriend 只删发起方一侧的记录，用户那边还留着业务员 —— BUG 的温床
	env.post(env.imBase+"/friend/delete_friend", sales.imToken, map[string]any{
		"ownerUserID": sales.imUserID, "friendUserID": cust.imUserID,
	})
	state := env.friendState(sales, cust)
	t.Logf("业务员单方面解除后的好友关系: %s", state)
	if !strings.Contains(state, `"inUser2Friends":true`) {
		t.Fatalf("前提不成立：解除后用户那一侧的记录应当还在，实际 %s", state)
	}
	if strings.Contains(state, `"inUser1Friends":true`) {
		t.Fatalf("前提不成立：业务员那一侧的记录应已删除，实际 %s", state)
	}

	// ---------- 4. 核心：解除之后谁都不该再发得出去 ----------
	// 修复前，业务员（删好友的那个人）这一条会成功。
	assertSend(t, ctx, msgCli, sales, cust, true, "解除后：业务员 -> 用户（删好友的人继续发）")
	assertSend(t, ctx, msgCli, cust, sales, true, "解除后：用户 -> 业务员（被删的人发）")
}

type liveUser struct {
	imUserID string
	imToken  string
}

type liveEnv struct {
	t                          *testing.T
	chatBase, imBase           string
	orgID, aesKey              string
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// loadOrgEnv 从文件读组织 id 与密钥。走文件而不是命令行参数，
// 避免密钥留在 shell 历史或进程列表里。
func loadOrgEnv(path string) (orgID, aesKey string) {
	if path == "" {
		return "", ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "ORG_ID":
			orgID = v
		case "ORG_AES_KEY":
			aesKey = v
		}
	}
	return orgID, aesKey
}

var liveHTTP = &http.Client{Timeout: 30 * time.Second}

func (e *liveEnv) post(url, token string, body any) json.RawMessage {
	e.t.Helper()
	data, err := e.tryPost(url, token, body)
	if err != nil {
		e.t.Fatalf("%v", err)
	}
	return data
}

func (e *liveEnv) tryPost(url, token string, body any) (json.RawMessage, error) {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("operationID", fmt.Sprintf("fv-%d", time.Now().UnixNano()))
	if token != "" {
		req.Header.Set("token", token)
	}
	resp, err := liveHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s 失败: %w", url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		ErrCode int             `json:"errCode"`
		ErrMsg  string          `json:"errMsg"`
		ErrDlt  string          `json:"errDlt"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("解析 %s 响应失败: %s", url, string(raw))
	}
	if env.ErrCode != 0 {
		return nil, fmt.Errorf("%s 返回错误: errCode=%d %s %s", url, env.ErrCode, env.ErrMsg, env.ErrDlt)
	}
	return env.Data, nil
}

func aesGCMEncrypt(plain []byte, keyB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func aesGCMDecrypt(b64, keyB64 string) ([]byte, error) {
	key, _ := base64.StdEncoding.DecodeString(keyB64)
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(data) < gcm.NonceSize() {
		return nil, fmt.Errorf("密文过短")
	}
	return gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
}

// login 走嵌入式登录建号并换 im_token。
func (e *liveEnv) login(thirdID, nickname string) *liveUser {
	e.t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"deviceID": "fv-" + thirdID, "platform": 5,
		"third_user_id": thirdID,
		"user":          map[string]any{"nickname": nickname},
	})
	sec, err := aesGCMEncrypt(payload, e.aesKey)
	if err != nil {
		e.t.Fatalf("加密登录请求失败: %v", err)
	}
	// 测试环境的组织缓存里 aesKeyBase64 有时是空的，服务端会返回
	// "crypto/aes: invalid key size 0"，等缓存过期重新读库才正常。
	// 这是环境的毛病，与被验证的功能无关，在这里重试吸收掉。
	var data json.RawMessage
	var lastErr error
	for i := 0; i < 6; i++ {
		data, lastErr = e.tryPost(e.chatBase+"/third/account/embed/login", "",
			map[string]any{"app_id": e.orgID, "secret": sec})
		if lastErr == nil {
			break
		}
		if !strings.Contains(lastErr.Error(), "invalid key size") {
			e.t.Fatalf("嵌入式登录失败: %v", lastErr)
		}
		e.t.Logf("组织缓存丢了密钥，%d 秒后重试（环境问题，非功能问题）", (i+1)*5)
		time.Sleep(time.Duration(i+1) * 5 * time.Second)
	}
	if lastErr != nil {
		e.t.Fatalf("嵌入式登录重试后仍失败: %v", lastErr)
	}
	var wrapped struct {
		Secret string `json:"secret"`
	}
	json.Unmarshal(data, &wrapped)
	dec, err := aesGCMDecrypt(wrapped.Secret, e.aesKey)
	if err != nil {
		e.t.Fatalf("解密登录响应失败: %v", err)
	}
	var out struct {
		UserID  string `json:"user_id"`
		IMToken string `json:"im_token"`
	}
	json.Unmarshal(dec, &out)
	if out.IMToken == "" {
		e.t.Fatalf("没拿到 im_token")
	}
	return &liveUser{imUserID: out.UserID, imToken: out.IMToken}
}

// befriend 建立双向好友：a 发申请，b 同意。
// 响应方是 toUserID，且必须用自己的 token 调（服务端按 ToUserID 校验权限）。
func (e *liveEnv) befriend(a, b *liveUser) {
	e.t.Helper()
	e.post(e.imBase+"/friend/add_friend", a.imToken, map[string]any{
		"fromUserID": a.imUserID, "toUserID": b.imUserID, "reqMsg": "验证", "ex": "",
	})
	e.post(e.imBase+"/friend/add_friend_response", b.imToken, map[string]any{
		"fromUserID": a.imUserID, "toUserID": b.imUserID, "handleResult": 1, "handleMsg": "同意",
	})
}

func (e *liveEnv) friendState(a, b *liveUser) string {
	e.t.Helper()
	return string(e.post(e.imBase+"/friend/is_friend", a.imToken,
		map[string]any{"userID1": a.imUserID, "userID2": b.imUserID}))
}

// assertSend 直连 msg RPC 发一条单聊，断言是否被好友校验拦下。
func assertSend(t *testing.T, ctx context.Context, cli msg.MsgClient,
	from, to *liveUser, wantBlocked bool, desc string) {
	t.Helper()

	// RPC 从 metadata 取 operationID，缺了会直接报错
	sendCtx := metadata.AppendToOutgoingContext(ctx, constant.OperationID,
		fmt.Sprintf("fv-%d", time.Now().UnixNano()))

	_, err := cli.SendMsg(sendCtx, &msg.SendMsgReq{
		MsgData: &sdkws.MsgData{
			SendID:           from.imUserID,
			RecvID:           to.imUserID,
			SenderPlatformID: constant.WebPlatformID,
			SessionType:      constant.SingleChatType,
			ContentType:      constant.Text,
			Content:          []byte(`{"content":"friend verify"}`),
			ClientMsgID:      fmt.Sprintf("fv-%d", time.Now().UnixNano()),
			CreateTime:       time.Now().UnixMilli(),
			SenderNickname:   "fv",
		},
	})
	notFriend := err != nil && strings.Contains(err.Error(), "NotPeersFriend")

	switch {
	case wantBlocked && notFriend:
		t.Logf("✓ %s：已被好友校验拦下", desc)
	case wantBlocked && err == nil:
		t.Errorf("✗ %s：期望被拦下，实际发送成功了 —— BUG 未修复", desc)
	case wantBlocked:
		t.Errorf("✗ %s：期望 NotPeersFriend，实际是另一个错误: %v", desc, err)
	case err == nil:
		t.Logf("✓ %s：发送成功", desc)
	case notFriend:
		t.Errorf("✗ %s：期望发送成功，却被好友校验拦下 —— 校验过严: %v", desc, err)
	default:
		t.Fatalf("%s：发送失败（非好友校验原因）: %v", desc, err)
	}
}

// TestSendOnceLive 给定两个已存在的用户，只发一条消息看是否被好友校验拦下。
//
// 用途：与上面的完整流程配合做判别。完整流程里「互为好友时先发一轮」
// 会把好友关系写进 msg RPC 的进程内缓存，之后解除好友若缓存没失效，
// 就分不清「修复没生效」还是「缓存是旧的」。重启 msg RPC 后单独跑这个，
// 拿到的一定是新数据。
//
//	MSG_RPC_ADDR / SENDER_ID / RECV_ID / EXPECT=blocked|ok
func TestSendOnceLive(t *testing.T) {
	addr, sender, recv := os.Getenv("MSG_RPC_ADDR"), os.Getenv("SENDER_ID"), os.Getenv("RECV_ID")
	if addr == "" || sender == "" || recv == "" {
		t.Skip("未设置 MSG_RPC_ADDR / SENDER_ID / RECV_ID，跳过")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("连接 msg RPC 失败: %v", err)
	}
	defer conn.Close()
	assertSend(t, ctx, msg.NewMsgClient(conn),
		&liveUser{imUserID: sender}, &liveUser{imUserID: recv},
		os.Getenv("EXPECT") == "blocked",
		fmt.Sprintf("%s -> %s", sender, recv))
}
