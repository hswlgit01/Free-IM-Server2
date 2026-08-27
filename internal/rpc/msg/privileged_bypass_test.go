package msg

import (
	"os"
	"testing"
)

// 这条判断决定「谁能给谁发单聊」，改错的后果是要么用户被误拦、
// 要么解除好友后仍能骚扰。三种模式各自的语义在这里锁死。
func TestPrivilegedBypassAllows(t *testing.T) {
	const admin = "GroupManager" // 特权角色
	const lead = "TermManager"   // 特权角色
	const normal = "Normal"      // 非特权

	cases := []struct {
		mode       string
		senderRole string
		recvRole   string
		want       bool
		desc       string
	}{
		// none：默认。谁都不豁免，一律走好友校验。
		{"", admin, normal, false, "默认模式：管理员发给普通用户也要校验好友"},
		{"", normal, admin, false, "默认模式：普通用户发给管理员也要校验好友"},
		{"none", admin, admin, false, "none：双方都是管理员也要校验"},
		{"none", normal, normal, false, "none：普通用户之间照常校验"},

		// sender：仅发送方特权时放行。
		{"sender", admin, normal, true, "sender：管理员可主动私聊非好友"},
		{"sender", lead, normal, true, "sender：团队长可主动私聊非好友"},
		{"sender", normal, admin, false, "sender：普通用户不能主动私聊非好友管理员（也无法回复）"},
		{"sender", normal, normal, false, "sender：普通用户之间照常校验"},

		// both：原行为，任一方特权即双方豁免。
		{"both", admin, normal, true, "both：管理员可主动发"},
		{"both", normal, admin, true, "both：用户可回复管理员"},
		{"both", normal, normal, false, "both：普通用户之间仍需好友"},

		// 非法取值回落 none，避免拼错导致意外放开校验
		{"BOTH_TYPO", admin, normal, false, "非法取值回落 none"},
		{"Sender ", admin, normal, true, "大小写与空格应被容错"},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if c.mode == "" {
				os.Unsetenv("PRIVILEGED_ROLE_FRIEND_BYPASS")
			} else {
				os.Setenv("PRIVILEGED_ROLE_FRIEND_BYPASS", c.mode)
			}
			defer os.Unsetenv("PRIVILEGED_ROLE_FRIEND_BYPASS")

			if got := privilegedBypassAllows(c.senderRole, c.recvRole); got != c.want {
				t.Errorf("模式=%q 发送方=%s 接收方=%s：得到 %v，期望 %v",
					c.mode, c.senderRole, c.recvRole, got, c.want)
			}
		})
	}
}
