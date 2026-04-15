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

package redisutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"

	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/mw/specialerror"
	"github.com/redis/go-redis/v9"
)

func init() {
	if err := specialerror.AddReplace(redis.Nil, errs.ErrRecordNotFound); err != nil {
		panic(err)
	}
}

// TLSConfig represents TLS configuration for Redis connection.
type TLSConfig struct {
	EnableTLS          bool   `yaml:"enableTLS"`
	CACrt              string `yaml:"caCrt"`
	ClientCrt          string `yaml:"clientCrt"`
	ClientKey          string `yaml:"clientKey"`
	ClientKeyPwd       string `yaml:"clientKeyPwd"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
}

// Config defines the configuration parameters for a Redis client, including
// options for both single-node and cluster mode connections.
type Config struct {
	ClusterMode bool      // Whether to use Redis in cluster mode.
	Address     []string  // List of Redis server addresses (host:port).
	Username    string    // Username for Redis authentication (Redis 6 ACL).
	Password    string    // Password for Redis authentication.
	MaxRetry    int       // Maximum number of retries for a command.
	DB          int       // Database number to connect to, for non-cluster mode.
	PoolSize    int       // Number of connections to pool.
	TLS         TLSConfig // TLS configuration.
}

func NewRedisClient(ctx context.Context, config *Config) (redis.UniversalClient, error) {
	if len(config.Address) == 0 {
		return nil, errs.New("redis address is empty").Wrap()
	}

	var tlsConfig *tls.Config
	var err error

	// 配置TLS
	if config.TLS.EnableTLS {
		tlsConfig, err = newTLSConfig(config.TLS.ClientCrt, config.TLS.ClientKey, config.TLS.CACrt, []byte(config.TLS.ClientKeyPwd), config.TLS.InsecureSkipVerify)
		if err != nil {
			return nil, errs.WrapMsg(err, "failed to build TLS config")
		}
	}

	var cli redis.UniversalClient
	if config.ClusterMode || len(config.Address) > 1 {
		opt := &redis.ClusterOptions{
			Addrs:      config.Address,
			Username:   config.Username,
			Password:   config.Password,
			PoolSize:   config.PoolSize,
			MaxRetries: config.MaxRetry,
			TLSConfig:  tlsConfig,
		}
		cli = redis.NewClusterClient(opt)
	} else {
		opt := &redis.Options{
			Addr:       config.Address[0],
			Username:   config.Username,
			Password:   config.Password,
			DB:         config.DB,
			PoolSize:   config.PoolSize,
			MaxRetries: config.MaxRetry,
			TLSConfig:  tlsConfig,
		}
		cli = redis.NewClient(opt)
	}
	if err := cli.Ping(ctx).Err(); err != nil {
		return nil, errs.WrapMsg(err, "Redis Ping failed", "Address", config.Address, "Username", config.Username, "ClusterMode", config.ClusterMode)
	}
	return cli, nil
}

// decryptPEM decrypts a PEM block using a password.
func decryptPEM(data []byte, passphrase []byte) ([]byte, error) {
	if len(passphrase) == 0 {
		return data, nil
	}
	b, _ := pem.Decode(data)
	if b == nil {
		return nil, errs.New("failed to decode PEM block")
	}
	d, err := x509.DecryptPEMBlock(b, passphrase)
	if err != nil {
		return nil, errs.WrapMsg(err, "DecryptPEMBlock failed")
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  b.Type,
		Bytes: d,
	}), nil
}

func readEncryptablePEMBlock(path string, pwd []byte) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.WrapMsg(err, "ReadFile failed", "path", path)
	}
	return decryptPEM(data, pwd)
}

// newTLSConfig setup the TLS config from general config file.
func newTLSConfig(clientCertFile, clientKeyFile, caCertFile string, keyPwd []byte, insecureSkipVerify bool) (*tls.Config, error) {
	var tlsConfig tls.Config
	if clientCertFile != "" && clientKeyFile != "" {
		certPEMBlock, err := os.ReadFile(clientCertFile)
		if err != nil {
			return nil, errs.WrapMsg(err, "ReadFile failed", "clientCertFile", clientCertFile)
		}
		keyPEMBlock, err := readEncryptablePEMBlock(clientKeyFile, keyPwd)
		if err != nil {
			return nil, err
		}

		cert, err := tls.X509KeyPair(certPEMBlock, keyPEMBlock)
		if err != nil {
			return nil, errs.WrapMsg(err, "X509KeyPair failed")
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if caCertFile != "" {
		caCert, err := os.ReadFile(caCertFile)
		if err != nil {
			return nil, errs.WrapMsg(err, "ReadFile failed", "caCertFile", caCertFile)
		}
		caCertPool := x509.NewCertPool()
		if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
			return nil, errs.New("AppendCertsFromPEM failed")
		}
		tlsConfig.RootCAs = caCertPool
	}
	tlsConfig.InsecureSkipVerify = insecureSkipVerify
	return &tlsConfig, nil
}
