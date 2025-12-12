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

package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/openimsdk/open-im-server/v3/pkg/common/config"
	"github.com/openimsdk/open-im-server/v3/protocol/third"
	"github.com/openimsdk/tools/a2r"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/log"
	"github.com/openimsdk/tools/mcontext"
	"google.golang.org/grpc"
)

type ThirdApi struct {
	GrafanaUrl  string
	Client      third.ThirdClient
	Config      *config.Third // 直接使用现有的Third配置
	MinioConfig *config.Minio // MinIO配置
	httpClient  *http.Client  // 复用HTTP客户端
	once        sync.Once     // 确保客户端只初始化一次
}

func NewThirdApi(client third.ThirdClient, grafanaUrl string, thirdConfig *config.Third, minioConfig *config.Minio) ThirdApi {
	return ThirdApi{
		Client:      client,
		GrafanaUrl:  grafanaUrl,
		Config:      thirdConfig,
		MinioConfig: minioConfig,
	}
}

// getHTTPClient 获取复用的HTTP客户端
func (o *ThirdApi) getHTTPClient() *http.Client {
	o.once.Do(func() {
		o.httpClient = &http.Client{
			Timeout: 60 * time.Second, // 增加超时时间以支持大文件
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	})
	return o.httpClient
}

func (o *ThirdApi) FcmUpdateToken(c *gin.Context) {
	a2r.Call(c, third.ThirdClient.FcmUpdateToken, o.Client)
}

func (o *ThirdApi) SetAppBadge(c *gin.Context) {
	a2r.Call(c, third.ThirdClient.SetAppBadge, o.Client)
}

// #################### s3 ####################

func setURLPrefixOption[A, B, C any](_ func(client C, ctx context.Context, req *A, options ...grpc.CallOption) (*B, error), fn func(*A) error) *a2r.Option[A, B] {
	return &a2r.Option[A, B]{
		BindAfter: fn,
	}
}

func setURLPrefix(c *gin.Context, urlPrefix *string) error {
	host := c.GetHeader("X-Request-Api")
	if host != "" {
		if strings.HasSuffix(host, "/") {
			*urlPrefix = host + "object/"
			return nil
		} else {
			*urlPrefix = host + "/object/"
			return nil
		}
	}
	u := url.URL{
		Scheme: "http",
		Host:   c.Request.Host,
		Path:   "/object/",
	}
	if c.Request.TLS != nil {
		u.Scheme = "https"
	}
	*urlPrefix = u.String()
	return nil
}

func (o *ThirdApi) PartLimit(c *gin.Context) {
	a2r.Call(c, third.ThirdClient.PartLimit, o.Client)
}

func (o *ThirdApi) PartSize(c *gin.Context) {
	a2r.Call(c, third.ThirdClient.PartSize, o.Client)
}

func (o *ThirdApi) InitiateMultipartUpload(c *gin.Context) {
	opt := setURLPrefixOption(third.ThirdClient.InitiateMultipartUpload, func(req *third.InitiateMultipartUploadReq) error {
		return setURLPrefix(c, &req.UrlPrefix)
	})
	a2r.Call(c, third.ThirdClient.InitiateMultipartUpload, o.Client, opt)
}

func (o *ThirdApi) AuthSign(c *gin.Context) {
	opt := setURLPrefixOption(third.ThirdClient.AuthSign, func(req *third.AuthSignReq) error {
		return setURLPrefix(c, &req.UrlPrefix)
	})
	a2r.Call(c, third.ThirdClient.AuthSign, o.Client, opt)
}

func (o *ThirdApi) CompleteMultipartUpload(c *gin.Context) {
	opt := setURLPrefixOption(third.ThirdClient.CompleteMultipartUpload, func(req *third.CompleteMultipartUploadReq) error {
		return setURLPrefix(c, &req.UrlPrefix)
	})
	a2r.Call(c, third.ThirdClient.CompleteMultipartUpload, o.Client, opt)
}

func (o *ThirdApi) AccessURL(c *gin.Context) {
	a2r.Call(c, third.ThirdClient.AccessURL, o.Client)
}

func (o *ThirdApi) InitiateFormData(c *gin.Context) {
	a2r.Call(c, third.ThirdClient.InitiateFormData, o.Client)
}

func (o *ThirdApi) CompleteFormData(c *gin.Context) {
	opt := setURLPrefixOption(third.ThirdClient.CompleteFormData, func(req *third.CompleteFormDataReq) error {
		return setURLPrefix(c, &req.UrlPrefix)
	})
	a2r.Call(c, third.ThirdClient.CompleteFormData, o.Client, opt)
}

func (o *ThirdApi) ObjectRedirect(c *gin.Context) {
	name := c.Param("name")
	if name == "" {
		c.String(http.StatusBadRequest, "name is empty")
		return
	}
	if name[0] == '/' {
		name = name[1:]
	}
	operationID := c.Query("operationID")
	if operationID == "" {
		operationID = strconv.Itoa(rand.Int())
	}
	ctx := mcontext.SetOperationID(c, operationID)
	query := make(map[string]string)
	for key, values := range c.Request.URL.Query() {
		if len(values) == 0 {
			continue
		}
		query[key] = values[0]
	}
	resp, err := o.Client.AccessURL(ctx, &third.AccessURLReq{Name: name, Query: query})
	if err != nil {
		if errs.ErrArgs.Is(err) {
			c.String(http.StatusBadRequest, err.Error())
			return
		}
		if errs.ErrRecordNotFound.Is(err) {
			c.String(http.StatusNotFound, err.Error())
			return
		}
		c.String(http.StatusInternalServerError, err.Error())
		return
	}

	// 使用复用的HTTP客户端
	client := o.getHTTPClient()

	req, err := http.NewRequestWithContext(c.Request.Context(), "GET", resp.Url, nil)
	if err != nil {
		log.ZError(ctx, "failed to create request", err, "url", resp.Url)
		c.String(http.StatusInternalServerError, "failed to create request: "+err.Error())
		return
	}

	// 改进的请求头处理
	for key, values := range c.Request.Header {
		switch strings.ToLower(key) {
		case "host", "content-length", "transfer-encoding", "connection", "te", "trailer", "upgrade", "proxy-authenticate", "proxy-authorization":
			// 跳过这些头，让Go的HTTP客户端自动处理
			continue
		case "range", "if-range", "if-match", "if-none-match", "if-modified-since", "if-unmodified-since":
			// 传递缓存和范围相关的头
			for _, value := range values {
				req.Header.Add(key, value)
			}
		case "user-agent", "accept", "accept-encoding", "accept-language":
			// 传递客户端相关的头
			for _, value := range values {
				req.Header.Add(key, value)
			}
		case "authorization":
			// 如果有认证头，也传递（某些存储服务可能需要）
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	}

	log.ZDebug(ctx, "proxying request to storage", "url", resp.Url)

	// 发送请求到存储服务
	proxyResp, err := client.Do(req)
	if err != nil {
		log.ZError(ctx, "failed to proxy request", err, "url", resp.Url)
		c.String(http.StatusBadGateway, "failed to proxy request: "+err.Error())
		return
	}
	defer proxyResp.Body.Close()

	log.ZDebug(ctx, "received response from storage", "status", proxyResp.StatusCode, "contentLength", proxyResp.ContentLength)

	// 安全的响应头复制（过滤掉某些不应该透传的头）
	for key, values := range proxyResp.Header {
		switch strings.ToLower(key) {
		case "server", "x-amz-request-id", "x-amz-id-2", "x-amz-version-id":
			// 跳过服务器特定的头
			continue
		case "set-cookie":
			// 跳过Cookie设置
			continue
		case "strict-transport-security", "x-frame-options", "x-content-type-options":
			// 跳过安全相关的头，让网关自己处理
			continue
		default:
			for _, value := range values {
				c.Header(key, value)
			}
		}
	}

	// 设置状态码
	c.Status(proxyResp.StatusCode)

	// 复制响应体并处理错误
	written, err := io.Copy(c.Writer, proxyResp.Body)
	if err != nil {
		log.ZError(ctx, "failed to copy response body", err, "bytesWritten", written)
		// 注意：此时已经开始写入响应，无法更改状态码
		return
	}

	log.ZDebug(ctx, "ObjectRedirect completed successfully", "bytesWritten", written, "status", proxyResp.StatusCode)
}

func (o *ThirdApi) S3Proxy(c *gin.Context) {
	filepath := c.Param("filepath")

	if filepath == "" {
		c.String(http.StatusBadRequest, "missing file path")
		return
	}

	// 从配置中构建真实的S3 URL
	targetURL, err := o.buildS3URL(filepath, c.Request.URL.RawQuery)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to build S3 URL: "+err.Error())
		return
	}

	// 创建到S3的请求
	client := &http.Client{
		Timeout: 30 * time.Minute, // 设置较长的超时时间以支持大文件上传
	}

	// 对于有请求体的方法，需要正确处理请求体
	var body io.Reader
	if c.Request.Method == "PUT" || c.Request.Method == "POST" || c.Request.Method == "PATCH" {
		// 读取请求体内容到字节数组，避免EOF错误
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.String(http.StatusBadRequest, "failed to read request body: "+err.Error())
			return
		}
		body = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, targetURL, body)
	if err != nil {
		c.String(http.StatusInternalServerError, "failed to create request: "+err.Error())
		return
	}

	// 复制请求头，但跳过一些不需要的头
	for key, values := range c.Request.Header {
		switch strings.ToLower(key) {
		case "host", "content-length", "transfer-encoding", "connection", "te", "trailer", "upgrade", "proxy-authenticate", "proxy-authorization":
			// 跳过这些头，让Go的HTTP客户端自动处理
			continue
		default:
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	}

	// 发送请求到S3
	resp, err := client.Do(req)
	if err != nil {
		c.String(http.StatusBadGateway, "failed to proxy request: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// 复制响应头
	for key, values := range resp.Header {
		for _, value := range values {
			c.Header(key, value)
		}
	}

	// 设置状态码并复制响应体
	c.Status(resp.StatusCode)
	io.Copy(c.Writer, resp.Body)
}

// buildS3URL 根据路径和查询参数构建真实的S3 URL
func (o *ThirdApi) buildS3URL(filePath, query string) (string, error) {
	if o.Config == nil {
		return "", fmt.Errorf("storage not configured")
	}

	// 检查配置的存储类型
	switch o.Config.Object.Enable {
	case "aws":
		// 从配置中获取AWS配置信息
		region := o.Config.Object.Aws.Region
		if region == "" {
			region = "ap-east-1" // 默认区域
		}

		// 从 /openim/ 开始提取路径
		openimIndex := strings.Index(filePath, "/openim/")
		if openimIndex != -1 {
			filePath = filePath[openimIndex:] // 从 /openim/ 开始截取
		} else {
			// 如果没找到 /openim/，确保filePath以/开头
			if !strings.HasPrefix(filePath, "/") {
				filePath = "/" + filePath
			}
		}

		// 获取桶名
		bucket := o.Config.Object.Aws.Bucket
		if bucket == "" {
			return "", fmt.Errorf("AWS S3 bucket not configured")
		}

		// 根据配置选择S3 URL格式
		var s3URL string
		urlStyle := o.Config.Object.Aws.URLStyle
		if urlStyle == "" {
			urlStyle = "virtual-hosted" // 默认使用 Virtual-hosted-style
		}

		switch urlStyle {
		case "path-style":
			// Path-style 格式: https://s3.region.amazonaws.com/bucket/path
			s3URL = fmt.Sprintf("https://s3.%s.amazonaws.com/%s%s", region, bucket, filePath)
		case "virtual-hosted":
			fallthrough
		default:
			// Virtual-hosted-style 格式: https://bucket.s3.region.amazonaws.com/path (AWS推荐)
			s3URL = fmt.Sprintf("https://%s.s3.%s.amazonaws.com%s", bucket, region, filePath)
		}
		if query != "" {
			s3URL += "?" + query
		}
		return s3URL, nil

	case "minio":
		// MinIO配置 - 直接使用MinIO的外部地址
		if o.MinioConfig == nil {
			return "", fmt.Errorf("MinIO not configured")
		}

		// 构建MinIO URL
		externalAddr := o.MinioConfig.ExternalAddress
		if externalAddr == "" {
			externalAddr = o.MinioConfig.InternalAddress
		}

		// 确保地址包含协议
		if !strings.HasPrefix(externalAddr, "http://") && !strings.HasPrefix(externalAddr, "https://") {
			externalAddr = "http://" + externalAddr
		}

		// filePath 格式为 /bucket/path 或 /openim/bucket/path
		// 需要保持原始路径,MinIO会自动处理
		// 移除前导斜杠
		filePath = strings.TrimPrefix(filePath, "/")

		minioURL := fmt.Sprintf("%s/%s", externalAddr, filePath)
		if query != "" {
			minioURL += "?" + query
		}
		return minioURL, nil

	default:
		return "", fmt.Errorf("unsupported storage type: %s", o.Config.Object.Enable)
	}
}

// #################### logs ####################.
func (o *ThirdApi) UploadLogs(c *gin.Context) {
	a2r.Call(c, third.ThirdClient.UploadLogs, o.Client)
}

func (o *ThirdApi) DeleteLogs(c *gin.Context) {
	a2r.Call(c, third.ThirdClient.DeleteLogs, o.Client)
}

func (o *ThirdApi) SearchLogs(c *gin.Context) {
	a2r.Call(c, third.ThirdClient.SearchLogs, o.Client)
}

func (o *ThirdApi) GetPrometheus(c *gin.Context) {
	c.Redirect(http.StatusFound, o.GrafanaUrl)
}
