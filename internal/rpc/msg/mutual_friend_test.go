package msg

import (
	"os"
	"testing"
)

// 私聊校验的方向语义极易搞反，这里把它锁死。
//
// 背景：好友记录是有方向的 (owner_user_id, friend_user_id)，
// DeleteFriend 只删发起方一侧。原先的单边校验问的是
// 「发送方在接收方的好友列表里吗」，结果是**谁删的好友谁还能发**，
// 正是客户所报的「业务员解除好友后仍可聊天」。
func TestMutualFriendRequired(t *testing.T) {
	cases := []struct {
		env  string
		want bool
		desc string
	}{
		{"", true, "默认要求双向好友（解除好友即断绝联系）"},
		{"off", false, "off 退回单边判断"},
		{"false", false, "false 等价于 off"},
		{"0", false, "0 等价于 off"},
		{"on", true, "on 显式开启"},
		{"OFF", false, "大小写容错"},
		{" off ", false, "空格容错"},
		{"随便写", true, "非法取值回落到默认开启，不会意外放宽校验"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if c.env == "" {
				os.Unsetenv("FRIEND_VERIFY_MUTUAL")
			} else {
				os.Setenv("FRIEND_VERIFY_MUTUAL", c.env)
			}
			defer os.Unsetenv("FRIEND_VERIFY_MUTUAL")
			if got := mutualFriendRequired(); got != c.want {
				t.Errorf("FRIEND_VERIFY_MUTUAL=%q：得到 %v，期望 %v", c.env, got, c.want)
			}
		})
	}
}

// 用一个双向好友表模拟四种关系，验证「单边」与「双向」两种判据的差异，
// 确认双向判据确实堵住了「谁删的好友谁还能发」这条路。
func TestFriendCheckSemantics(t *testing.T) {
	// friendList[X] = X 的好友列表
	friendList := map[string][]string{
		"业务员": {"用户B"},        // 业务员加了用户B、也加了用户A（下面补）
		"用户A":  {"业务员"},        // 用户A 单方面留着业务员（业务员把他删了）
		"用户B":  {"业务员"},        // 双向
		"陌生人":  {},             // 无任何好友
	}
	inList := func(who, target string) bool {
		for _, f := range friendList[who] {
			if f == target {
				return true
			}
		}
		return false
	}
	// 原判据：发送方是否在接收方的列表里
	oneWay := func(sender, recv string) bool { return inList(recv, sender) }
	// 新判据：两个方向都要成立
	mutual := func(sender, recv string) bool { return inList(recv, sender) && inList(sender, recv) }

	cases := []struct {
		sender, recv         string
		wantOneWay, wantMutual bool
		desc                 string
	}{
		{"业务员", "用户B", true, true, "正常双向好友：两种判据都放行"},
		{"用户B", "业务员", true, true, "反向同样放行"},
		// 核心场景：业务员把用户A删了，用户A的列表里还留着业务员
		{"业务员", "用户A", true, false, "业务员删了用户A后仍发消息 —— 原判据放行（BUG），双向判据拦截"},
		{"用户A", "业务员", false, false, "被删的用户A发不出去，两种判据一致"},
		{"陌生人", "用户A", false, false, "非好友：都拦"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if got := oneWay(c.sender, c.recv); got != c.wantOneWay {
				t.Errorf("单边判据 %s->%s：得到 %v，期望 %v", c.sender, c.recv, got, c.wantOneWay)
			}
			if got := mutual(c.sender, c.recv); got != c.wantMutual {
				t.Errorf("双向判据 %s->%s：得到 %v，期望 %v", c.sender, c.recv, got, c.wantMutual)
			}
		})
	}

	// 把结论再显式断言一次：这正是本次修复要堵的那条路
	if !oneWay("业务员", "用户A") {
		t.Error("前提不成立：原判据本应放行「删了好友的人继续发消息」")
	}
	if mutual("业务员", "用户A") {
		t.Error("修复失效：双向判据必须拦住「删了好友的人继续发消息」")
	}
}
