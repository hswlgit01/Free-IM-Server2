// Copyright © 2024 OpenIM open source community. All rights reserved.
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

package mongoutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"time"

	"github.com/openimsdk/open-im-server/v3/tools/db/tx"
	"github.com/openimsdk/tools/errs"
	"github.com/openimsdk/tools/mw/specialerror"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func init() {
	if err := specialerror.AddReplace(mongo.ErrNoDocuments, errs.ErrRecordNotFound); err != nil {
		panic(err)
	}
}

// TLSConfig represents TLS configuration for MongoDB connection.
type TLSConfig struct {
	EnableTLS          bool   `yaml:"enableTLS"`
	CACrt              string `yaml:"caCrt"`
	ClientCrt          string `yaml:"clientCrt"`
	ClientKey          string `yaml:"clientKey"`
	ClientKeyPwd       string `yaml:"clientKeyPwd"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
}

// Config represents the MongoDB configuration.
type Config struct {
	Uri         string
	Address     []string
	Database    string
	Username    string
	Password    string
	AuthSource  string
	MaxPoolSize int
	MaxRetry    int
	TLS         TLSConfig
}

type Client struct {
	tx tx.Tx
	db *mongo.Database
}

func (c *Client) GetDB() *mongo.Database {
	return c.db
}

func (c *Client) GetTx() tx.Tx {
	return c.tx
}

// NewMongoDB initializes a new MongoDB connection.
func NewMongoDB(ctx context.Context, config *Config) (*Client, error) {
	if err := config.ValidateAndSetDefaults(); err != nil {
		return nil, err
	}
	opts := options.Client().ApplyURI(config.Uri).SetMaxPoolSize(uint64(config.MaxPoolSize))

	// 配置TLS
	if config.TLS.EnableTLS {
		tlsConfig, err := newTLSConfig(config.TLS.ClientCrt, config.TLS.ClientKey, config.TLS.CACrt, []byte(config.TLS.ClientKeyPwd), config.TLS.InsecureSkipVerify)
		if err != nil {
			return nil, errs.WrapMsg(err, "failed to build TLS config")
		}
		opts = opts.SetTLSConfig(tlsConfig)
	}

	var (
		cli *mongo.Client
		err error
	)
	for i := 0; i < config.MaxRetry; i++ {
		cli, err = connectMongo(ctx, opts)
		if err != nil && shouldRetry(ctx, err) {
			time.Sleep(time.Second / 2)
			continue
		}
		break
	}
	if err != nil {
		return nil, errs.WrapMsg(err, "failed to connect to MongoDB", "URI", config.Uri)
	}
	mtx, err := NewMongoTx(ctx, cli)
	if err != nil {
		return nil, err
	}
	return &Client{
		tx: mtx,
		db: cli.Database(config.Database),
	}, nil
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

func connectMongo(ctx context.Context, opts *options.ClientOptions) (*mongo.Client, error) {
	cli, err := mongo.Connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := cli.Ping(ctx, nil); err != nil {
		return nil, err
	}
	return cli, nil
}
