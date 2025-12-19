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

package third

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/openimsdk/open-im-server/v3/pkg/authverify"

	"github.com/google/uuid"
	"github.com/openimsdk/open-im-server/v3/pkg/common/servererrs"
	"github.com/openimsdk/open-im-server/v3/pkg/common/storage/model"
	"github.com/openimsdk/open-im-server/v3/protocol/third"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/mcontext"
	"github.com/openimsdk/tools/s3"
	"github.com/openimsdk/tools/s3/cont"
	"github.com/openimsdk/tools/utils/datautil"
)

func (t *thirdServer) PartLimit(ctx context.Context, req *third.PartLimitReq) (*third.PartLimitResp, error) {
	limit, err := t.s3dataBase.PartLimit()
	if err != nil {
		return nil, err
	}
	return &third.PartLimitResp{
		MinPartSize: limit.MinPartSize,
		MaxPartSize: limit.MaxPartSize,
		MaxNumSize:  int32(limit.MaxNumSize),
	}, nil
}

func (t *thirdServer) PartSize(ctx context.Context, req *third.PartSizeReq) (*third.PartSizeResp, error) {
	size, err := t.s3dataBase.PartSize(ctx, req.Size)
	if err != nil {
		return nil, err
	}
	return &third.PartSizeResp{Size: size}, nil
}

func (t *thirdServer) InitiateMultipartUpload(ctx context.Context, req *third.InitiateMultipartUploadReq) (*third.InitiateMultipartUploadResp, error) {
	// 从配置文件读取文件大小限制
	maxFileSizeMB := t.config.RpcConfig.MaxUploadSize
	if maxFileSizeMB <= 0 {
		maxFileSizeMB = 50 // 默认50MB
	}
	maxFileSize := maxFileSizeMB * 1024 * 1024 // 转换为字节
	if req.Size > maxFileSize {
		log.ZError(ctx, "InitiateMultipartUpload file size exceeds limit", nil, "requestedSize", req.Size, "maxSize", maxFileSize, "fileName", req.Name, "maxSizeMB", maxFileSizeMB)
		return nil, errs.ErrArgs.WrapMsg(fmt.Sprintf("file size exceeds limit, maximum allowed: %dMB, current file size: %.2fMB", maxFileSizeMB, float64(req.Size)/(1024*1024)))
	}

	if err := t.checkUploadName(ctx, req.Name); err != nil {
		return nil, err
	}
	expireTime := time.Now().Add(t.defaultExpire)
	result, err := t.s3dataBase.InitiateMultipartUpload(ctx, req.Hash, req.Size, t.defaultExpire, int(req.MaxParts))
	if err != nil {
		if haErr, ok := errs.Unwrap(err).(*cont.HashAlreadyExistsError); ok {
			obj := &model.Object{
				Name:        req.Name,
				UserID:      mcontext.GetOpUserID(ctx),
				Hash:        req.Hash,
				Key:         haErr.Object.Key,
				Size:        haErr.Object.Size,
				ContentType: req.ContentType,
				Group:       req.Cause,
				CreateTime:  time.Now(),
			}
			if err := t.s3dataBase.SetObject(ctx, obj); err != nil {
				return nil, err
			}
			return &third.InitiateMultipartUploadResp{
				Url: t.apiAddress(req.UrlPrefix, obj.Name),
			}, nil
		}
		return nil, err
	}
	var sign *third.AuthSignParts
	if result.Sign != nil && len(result.Sign.Parts) > 0 {
		// 直接使用请求中的UrlPrefix
		urlPrefix := req.UrlPrefix
		sign = &third.AuthSignParts{
			Url:    t.rewriteS3URLToProxy(result.Sign.URL, urlPrefix),
			Query:  toPbMapArray(result.Sign.Query),
			Header: toPbMapArray(result.Sign.Header),
			Parts:  make([]*third.SignPart, len(result.Sign.Parts)),
		}
		for i, part := range result.Sign.Parts {
			originalPartURL := part.URL
			// 如果URL为空但有查询参数，检查查询参数是否包含URL信息
			if originalPartURL == "" && len(part.Query) > 0 {
				for key, values := range part.Query {
					// 检查查询参数中是否有URL相关的信息
					if key == "url" || key == "URL" || strings.Contains(strings.ToLower(key), "url") {
						if len(values) > 0 {
							originalPartURL = values[0]
						}
					}
				}
			}

			newPartURL := t.rewriteS3URLToProxy(originalPartURL, urlPrefix)
			sign.Parts[i] = &third.SignPart{
				PartNumber: int32(part.PartNumber),
				Url:        newPartURL,
				Query:      toPbMapArray(part.Query),
				Header:     toPbMapArray(part.Header),
			}
		}
	}
	return &third.InitiateMultipartUploadResp{
		Upload: &third.UploadInfo{
			UploadID:   result.UploadID,
			PartSize:   result.PartSize,
			Sign:       sign,
			ExpireTime: expireTime.UnixMilli(),
		},
	}, nil
}

func (t *thirdServer) AuthSign(ctx context.Context, req *third.AuthSignReq) (*third.AuthSignResp, error) {

	partNumbers := datautil.Slice(req.PartNumbers, func(partNumber int32) int { return int(partNumber) })
	result, err := t.s3dataBase.AuthSign(ctx, req.UploadID, partNumbers)
	if err != nil {
		return nil, err
	}

	// 使用传入的UrlPrefix进行URL重写
	urlPrefix := req.UrlPrefix

	resp := &third.AuthSignResp{
		Url:    t.rewriteS3URLToProxy(result.URL, urlPrefix),
		Query:  toPbMapArray(result.Query),
		Header: toPbMapArray(result.Header),
		Parts:  make([]*third.SignPart, len(result.Parts)),
	}

	for i, part := range result.Parts {
		newPartURL := t.rewriteS3URLToProxy(part.URL, urlPrefix)
		resp.Parts[i] = &third.SignPart{
			PartNumber: int32(part.PartNumber),
			Url:        newPartURL,
			Query:      toPbMapArray(part.Query),
			Header:     toPbMapArray(part.Header),
		}
	}
	return resp, nil
}

func (t *thirdServer) CompleteMultipartUpload(ctx context.Context, req *third.CompleteMultipartUploadReq) (*third.CompleteMultipartUploadResp, error) {
	if err := t.checkUploadName(ctx, req.Name); err != nil {
		return nil, err
	}
	result, err := t.s3dataBase.CompleteMultipartUpload(ctx, req.UploadID, req.Parts)
	if err != nil {
		return nil, err
	}
	obj := &model.Object{
		Name:        req.Name,
		UserID:      mcontext.GetOpUserID(ctx),
		Hash:        result.Hash,
		Key:         result.Key,
		Size:        result.Size,
		ContentType: req.ContentType,
		Group:       req.Cause,
		CreateTime:  time.Now(),
	}
	if err := t.s3dataBase.SetObject(ctx, obj); err != nil {
		return nil, err
	}
	return &third.CompleteMultipartUploadResp{
		Url: t.apiAddress(req.UrlPrefix, obj.Name),
	}, nil
}

func (t *thirdServer) AccessURL(ctx context.Context, req *third.AccessURLReq) (*third.AccessURLResp, error) {
	opt := &s3.AccessURLOption{}
	if len(req.Query) > 0 {
		switch req.Query["type"] {
		case "":
		case "image":
			opt.Image = &s3.Image{}
			opt.Image.Format = req.Query["format"]
			opt.Image.Width, _ = strconv.Atoi(req.Query["width"])
			opt.Image.Height, _ = strconv.Atoi(req.Query["height"])
			log.ZDebug(ctx, "AccessURL image", "name", req.Name, "option", opt.Image)
		default:
			return nil, errs.ErrArgs.WrapMsg("invalid query type")
		}
	}
	expireTime, rawURL, err := t.s3dataBase.AccessURL(ctx, req.Name, t.defaultExpire, opt)
	if err != nil {
		return nil, err
	}
	return &third.AccessURLResp{
		Url:        rawURL,
		ExpireTime: expireTime.UnixMilli(),
	}, nil
}

func (t *thirdServer) InitiateFormData(ctx context.Context, req *third.InitiateFormDataReq) (*third.InitiateFormDataResp, error) {
	log.ZDebug(ctx, "InitiateFormData request", "name", req.Name, "size", req.Size, "contentType", req.ContentType)

	if req.Name == "" {
		return nil, errs.ErrArgs.WrapMsg("name is empty")
	}
	if req.Size <= 0 {
		return nil, errs.ErrArgs.WrapMsg("size must be greater than 0")
	}

	// 从配置文件读取文件大小限制
	maxFileSizeMB := t.config.RpcConfig.MaxUploadSize
	if maxFileSizeMB <= 0 {
		maxFileSizeMB = 50 // 默认50MB
	}
	maxFileSize := maxFileSizeMB * 1024 * 1024 // 转换为字节
	if req.Size > maxFileSize {
		log.ZError(ctx, "InitiateFormData file size exceeds limit", nil, "requestedSize", req.Size, "maxSize", maxFileSize, "fileName", req.Name, "maxSizeMB", maxFileSizeMB)
		return nil, errs.ErrArgs.WrapMsg(fmt.Sprintf("file size exceeds limit, maximum allowed: %dMB, current file size: %.2fMB", maxFileSizeMB, float64(req.Size)/(1024*1024)))
	}

	if err := t.checkUploadName(ctx, req.Name); err != nil {
		log.ZError(ctx, "InitiateFormData checkUploadName failed", err, "name", req.Name)
		return nil, err
	}
	var duration time.Duration
	opUserID := mcontext.GetOpUserID(ctx)
	var key string
	if t.IsManagerUserID(opUserID) {
		if req.Millisecond <= 0 {
			duration = time.Minute * 10
		} else {
			duration = time.Millisecond * time.Duration(req.Millisecond)
		}
		if req.Absolute {
			key = req.Name
		}
	} else {
		duration = time.Minute * 10
	}
	uid, err := uuid.NewRandom()
	if err != nil {
		return nil, errs.WrapMsg(err, "uuid NewRandom failed")
	}
	if key == "" {
		date := time.Now().Format("20060102")
		key = path.Join(cont.DirectPath, date, opUserID, hex.EncodeToString(uid[:])+path.Ext(req.Name))
	}
	mate := FormDataMate{
		Name:        req.Name,
		Size:        req.Size,
		ContentType: req.ContentType,
		Group:       req.Group,
		Key:         key,
	}
	mateData, err := json.Marshal(&mate)
	if err != nil {
		return nil, errs.WrapMsg(err, "marshal failed")
	}
	resp, err := t.s3dataBase.FormData(ctx, key, req.Size, req.ContentType, duration)
	if err != nil {
		log.ZError(ctx, "InitiateFormData FormData failed", err, "key", key)
		return nil, err
	}

	// InitiateFormData请求中没有UrlPrefix字段，暂时不进行URL重写
	urlPrefix := "" // 暂时不重写
	newURL := t.rewriteS3URLToProxy(resp.URL, urlPrefix)
	return &third.InitiateFormDataResp{
		Id:       base64.RawStdEncoding.EncodeToString(mateData),
		Url:      newURL,
		File:     resp.File,
		Header:   toPbMapArray(resp.Header),
		FormData: resp.FormData,
		Expires:  resp.Expires.UnixMilli(),
		SuccessCodes: datautil.Slice(resp.SuccessCodes, func(code int) int32 {
			return int32(code)
		}),
	}, nil
}

func (t *thirdServer) CompleteFormData(ctx context.Context, req *third.CompleteFormDataReq) (*third.CompleteFormDataResp, error) {
	if req.Id == "" {
		return nil, errs.ErrArgs.WrapMsg("id is empty")
	}
	data, err := base64.RawStdEncoding.DecodeString(req.Id)
	if err != nil {
		return nil, errs.ErrArgs.WrapMsg("invalid id " + err.Error())
	}
	var mate FormDataMate
	if err := json.Unmarshal(data, &mate); err != nil {
		return nil, errs.ErrArgs.WrapMsg("invalid id " + err.Error())
	}
	if err := t.checkUploadName(ctx, mate.Name); err != nil {
		return nil, err
	}
	info, err := t.s3dataBase.StatObject(ctx, mate.Key)
	if err != nil {
		return nil, err
	}
	if info.Size > 0 && info.Size != mate.Size {
		return nil, servererrs.ErrData.WrapMsg("file size mismatch")
	}
	obj := &model.Object{
		Name:        mate.Name,
		UserID:      mcontext.GetOpUserID(ctx),
		Hash:        "etag_" + info.ETag,
		Key:         info.Key,
		Size:        info.Size,
		ContentType: mate.ContentType,
		Group:       mate.Group,
		CreateTime:  time.Now(),
	}
	if err := t.s3dataBase.SetObject(ctx, obj); err != nil {
		return nil, err
	}
	return &third.CompleteFormDataResp{Url: t.apiAddress(req.UrlPrefix, mate.Name)}, nil
}

func (t *thirdServer) apiAddress(prefix, name string) string {
	return prefix + name
}

func (t *thirdServer) DeleteOutdatedData(ctx context.Context, req *third.DeleteOutdatedDataReq) (*third.DeleteOutdatedDataResp, error) {
	if err := authverify.CheckAdmin(ctx, t.config.Share.IMAdminUserID); err != nil {
		return nil, err
	}
	engine := t.config.RpcConfig.Object.Enable
	expireTime := time.UnixMilli(req.ExpireTime)
	// Find all expired data in S3 database
	models, err := t.s3dataBase.FindExpirationObject(ctx, engine, expireTime, req.ObjectGroup, int64(req.Limit))
	if err != nil {
		return nil, err
	}
	for i, obj := range models {
		if err := t.s3dataBase.DeleteSpecifiedData(ctx, engine, []string{obj.Name}); err != nil {
			return nil, errs.Wrap(err)
		}
		if err := t.s3dataBase.DelS3Key(ctx, engine, obj.Name); err != nil {
			return nil, err
		}
		count, err := t.s3dataBase.GetKeyCount(ctx, engine, obj.Key)
		if err != nil {
			return nil, err
		}
		log.ZDebug(ctx, "delete s3 object record", "index", i, "s3", obj, "count", count)
		if count == 0 {
			if err := t.s3.DeleteObject(ctx, obj.Key); err != nil {
				return nil, err
			}
		}
	}
	return &third.DeleteOutdatedDataResp{Count: int32(len(models))}, nil
}

type FormDataMate struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	Group       string `json:"group"`
	Key         string `json:"key"`
}

// rewriteS3URLToProxy 将S3的URL重写为代理服务的URL
// 对于 MinIO，直接返回原始 URL（MinIO 已通过 externalAddress 配置了公网访问）
// 对于 AWS S3 等云存储，重写为代理 URL
func (t *thirdServer) rewriteS3URLToProxy(s3URL, urlPrefix string) string {
	log.ZDebug(context.Background(), "URL rewrite start", "originalURL", s3URL, "urlPrefix", urlPrefix, "storageType", t.config.RpcConfig.Object.Enable)

	if s3URL == "" {
		log.ZWarn(context.Background(), "URL rewrite skipped - empty s3URL", nil)
		return s3URL
	}

	// MinIO 直接返回原始 URL，不需要代理
	// MinIO 已通过 externalAddress (如 https://files.vitechstore.xyz) 配置了公网 HTTPS 访问
	if t.config.RpcConfig.Object.Enable == "minio" {
		log.ZDebug(context.Background(), "MinIO storage - using original presigned URL", "url", s3URL)
		return s3URL
	}

	// 其他云存储（AWS S3、OSS、COS 等）需要通过代理访问
	if urlPrefix == "" {
		log.ZWarn(context.Background(), "URL rewrite skipped - empty urlPrefix for non-MinIO storage", nil, "s3URL", s3URL)
		return s3URL
	}

	// 解析S3 URL
	u, err := url.Parse(s3URL)
	if err != nil {
		log.ZError(context.Background(), "URL parse failed", err, "s3URL", s3URL)
		return s3URL
	}

	// 构建代理URL - 直接替换域名部分
	// 将原来的 s3.ap-southeast-1.amazonaws.com 替换为我们的域名 + /object/s3_proxy
	proxyURL := urlPrefix + "s3_proxy" + u.Path
	if !strings.HasSuffix(urlPrefix, "/") {
		proxyURL = urlPrefix + "/s3_proxy" + u.Path
	}

	// 保留原来的查询参数
	if u.RawQuery != "" {
		proxyURL += "?" + u.RawQuery
	}

	log.ZDebug(context.Background(), "URL rewritten for cloud storage", "originalURL", s3URL, "proxyURL", proxyURL)
	return proxyURL
}
