package msg

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/openimsdk/open-im-server/v3/protocol/constant"
	"github.com/openimsdk/open-im-server/v3/protocol/sdkws"
	"github.com/openimsdk/tools/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	sensitiveWordCollection = "sensitive_words"
	sensitiveWordStatusOn   = int32(1)
	sensitiveWordCacheTTL   = 10 * time.Second
)

type sensitiveWordCacheEntry struct {
	words    []string
	expireAt time.Time
}

var sensitiveWordCache sync.Map

// dawn 2026-05-14 新增敏感词过滤：消息服务直接读取后台维护的 Mongo 词表，并按组织短缓存。
func (m *msgServer) getSensitiveWords(ctx context.Context, orgID string) ([]string, error) {
	cacheKey := strings.TrimSpace(orgID)
	if cached, ok := sensitiveWordCache.Load(cacheKey); ok {
		entry := cached.(sensitiveWordCacheEntry)
		if time.Now().Before(entry.expireAt) {
			return entry.words, nil
		}
	}
	if m.mongoDatabase == nil || cacheKey == "" {
		return nil, nil
	}

	filter := bson.M{"status": sensitiveWordStatusOn}
	orgFilters := []bson.M{{"org_id_hex": cacheKey}, {"org_id": cacheKey}}
	if oid, err := primitive.ObjectIDFromHex(cacheKey); err == nil {
		orgFilters = append(orgFilters, bson.M{"org_id": oid})
	}
	filter["$or"] = orgFilters

	cursor, err := m.mongoDatabase.Collection(sensitiveWordCollection).Find(ctx, filter, options.Find().
		SetProjection(bson.M{"word": 1}).
		SetLimit(5000),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var rows []struct {
		Word string `bson:"word"`
	}
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}
	words := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		word := strings.TrimSpace(row.Word)
		if word == "" {
			continue
		}
		if _, ok := seen[word]; ok {
			continue
		}
		seen[word] = struct{}{}
		words = append(words, word)
	}
	sort.Slice(words, func(i, j int) bool {
		return utf8.RuneCountInString(words[i]) > utf8.RuneCountInString(words[j])
	})
	sensitiveWordCache.Store(cacheKey, sensitiveWordCacheEntry{words: words, expireAt: time.Now().Add(sensitiveWordCacheTTL)})
	return words, nil
}

func (m *msgServer) getSensitiveWordOrgID(ctx context.Context, msgData *sdkws.MsgData) string {
	if msgData == nil || msgData.SendID == "" || m.UserLocalCache == nil {
		return ""
	}
	user, err := m.UserLocalCache.GetUserInfo(ctx, msgData.SendID)
	if err != nil {
		log.ZWarn(ctx, "get sensitive word org id failed", err, "sendID", msgData.SendID)
		return ""
	}
	return strings.TrimSpace(user.GetOrgId())
}

func maskBySensitiveWords(text string, words []string) (string, bool) {
	if text == "" || len(words) == 0 {
		return text, false
	}
	masked := text
	for _, word := range words {
		if word == "" || !strings.Contains(masked, word) {
			continue
		}
		masked = strings.ReplaceAll(masked, word, strings.Repeat("*", utf8.RuneCountInString(word)))
	}
	return masked, masked != text
}

func maskJSONTextField(content []byte, field string, words []string) ([]byte, bool) {
	if len(content) == 0 {
		return content, false
	}
	var payload map[string]any
	if err := json.Unmarshal(content, &payload); err != nil {
		return content, false
	}
	raw, ok := payload[field].(string)
	if !ok {
		return content, false
	}
	masked, changed := maskBySensitiveWords(raw, words)
	if !changed {
		return content, false
	}
	payload[field] = masked
	next, err := json.Marshal(payload)
	if err != nil {
		return content, false
	}
	return next, true
}

func sensitiveWordJSONFields(contentType int32) []string {
	switch contentType {
	case constant.Text, constant.MarkdownText, constant.AdvancedText:
		return []string{"content"}
	case constant.AtText:
		return []string{"text"}
	default:
		return nil
	}
}

func (m *msgServer) maskSensitiveMessageContent(ctx context.Context, msgData *sdkws.MsgData) bool {
	fields := sensitiveWordJSONFields(msgData.GetContentType())
	if len(fields) == 0 {
		return false
	}
	words, err := m.getSensitiveWords(ctx, m.getSensitiveWordOrgID(ctx, msgData))
	if err != nil {
		log.ZWarn(ctx, "load sensitive words failed", err, "sendID", msgData.GetSendID())
		return false
	}
	changed := false
	for _, field := range fields {
		next, ok := maskJSONTextField(msgData.Content, field, words)
		if ok {
			msgData.Content = next
			changed = true
		}
	}
	return changed
}
