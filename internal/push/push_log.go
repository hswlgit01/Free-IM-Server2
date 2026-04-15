package push

import (
	"github.com/openimsdk/open-im-server/v3/protocol/msggateway"
)

// mergeOnlinePushOutcomeByUser 合并多网关返回的多条 SingleMsgToUserResults（同一 userID 可能重复），
// 只要任一网关 OnlinePush 成功则视为该用户在线推送成功。
func mergeOnlinePushOutcomeByUser(results []*msggateway.SingleMsgToUserResults) map[string]bool {
	out := make(map[string]bool)
	for _, r := range results {
		if r == nil {
			continue
		}
		if r.OnlinePush {
			out[r.UserID] = true
		} else if _, ok := out[r.UserID]; !ok {
			out[r.UserID] = false
		}
	}
	return out
}
