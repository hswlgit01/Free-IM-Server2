package mgo

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// 抽样统计 friend 集合里有多少「单向」好友关系。
// 单向 = A 的列表里有 B，但 B 的列表里没有 A。
//
// 为什么要查：DeleteFriend 只删发起方一侧的记录（FriendMgo.Delete 按
// owner_user_id 过滤），而发消息校验的是「发送方是否在接收方的好友列表里」。
// 两者叠加的后果是：谁删的好友，谁反而还能继续发消息。
// 如果库里确实存在大量单向记录，说明这条路径在生产上是活的。
func TestFriendDirectionality(t *testing.T) {
	uri := os.Getenv("FRIEND_IT_MONGO_URI")
	if uri == "" {
		t.Skip("未设置 FRIEND_IT_MONGO_URI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cli, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer cli.Disconnect(ctx)
	coll := cli.Database("openim_v3").Collection("friend")

	total, err := coll.CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("计数失败: %v", err)
	}
	t.Logf("friend 集合总记录数: %d", total)

	cur, err := coll.Find(ctx, bson.M{}, options.Find().SetLimit(500))
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	var docs []struct {
		Owner  string `bson:"owner_user_id"`
		Friend string `bson:"friend_user_id"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		t.Fatalf("解码失败: %v", err)
	}

	// 全量自连接：找出反向记录不存在的那些，避免只抽到最早的一批双向记录
	agg, err := coll.Aggregate(ctx, []bson.M{
		{"$lookup": bson.M{
			"from": "friend",
			"let":  bson.M{"o": "$owner_user_id", "f": "$friend_user_id"},
			"pipeline": []bson.M{{"$match": bson.M{"$expr": bson.M{"$and": []bson.M{
				{"$eq": []interface{}{"$owner_user_id", "$$f"}},
				{"$eq": []interface{}{"$friend_user_id", "$$o"}},
			}}}}, {"$limit": 1}},
			"as": "rev",
		}},
		{"$match": bson.M{"rev": bson.M{"$size": 0}}},
		{"$count": "n"},
	}, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		t.Fatalf("全量自连接失败: %v", err)
	}
	var cnt []struct {
		N int `bson:"n"`
	}
	if err := agg.All(ctx, &cnt); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	n := 0
	if len(cnt) > 0 {
		n = cnt[0].N
	}
	t.Logf("全量扫描：%d 条记录中有 %d 条是单向的（占比 %.2f%%）",
		total, n, float64(n)*100/float64(total))

	mutual, oneWay := 0, 0
	var samples []string
	for _, d := range docs {
		n, err := coll.CountDocuments(ctx,
			bson.M{"owner_user_id": d.Friend, "friend_user_id": d.Owner})
		if err != nil {
			t.Fatalf("反向查询失败: %v", err)
		}
		if n > 0 {
			mutual++
		} else {
			oneWay++
			if len(samples) < 5 {
				samples = append(samples, fmt.Sprintf("%s 的列表里有 %s，但反过来没有", d.Owner, d.Friend))
			}
		}
	}
	t.Logf("抽样 %d 条：双向 %d，单向 %d（占比 %.1f%%）",
		len(docs), mutual, oneWay, float64(oneWay)*100/float64(len(docs)))
	for _, s := range samples {
		t.Logf("  单向样本：%s", s)
	}
}
