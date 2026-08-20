package database

import "context"

type SeqUser interface {
	GetUserMaxSeq(ctx context.Context, conversationID string, userID string) (int64, error)
	SetUserMaxSeq(ctx context.Context, conversationID string, userID string, seq int64) error
	GetUserMinSeq(ctx context.Context, conversationID string, userID string) (int64, error)
	SetUserMinSeq(ctx context.Context, conversationID string, userID string, seq int64) error
	GetUserReadSeq(ctx context.Context, conversationID string, userID string) (int64, error)
	SetUserReadSeq(ctx context.Context, conversationID string, userID string, seq int64) error
	// SetUserReadSeqBatch 一次写入同一会话下多个用户的已读 seq。
	// 逐用户版本是「1 次读 + 1 次 upsert」，一批 1000 条消息就是 2000 次串行往返。
	SetUserReadSeqBatch(ctx context.Context, conversationID string, userSeqMap map[string]int64) error
	GetUserReadSeqs(ctx context.Context, userID string, conversationID []string) (map[string]int64, error)
}
