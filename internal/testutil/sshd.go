// Package testutil 提供测试用的最小 SSH 服务端与连接登记辅助。
// 仅被 _test.go 引用，不会进入生产二进制。
package testutil

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"remote-connect-manager/internal/config"
)

// SessionHandler 处理 SSH session 通道，例如挂载 SFTP 子系统
type SessionHandler func(ch ssh.Channel, reqs <-chan *ssh.Request)

// Server 测试用 SSH 服务端
type Server struct {
	Host          string
	Port          int
	User          string
	PrivateKeyPEM string // 客户端私钥（PKCS#8 PEM），供 config.Connection.PrivateKey 使用
}

// Addr 返回 host:port
func (s *Server) Addr() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

type directTCPIP struct {
	DestAddr   string
	DestPort   uint32
	OriginAddr string
	OriginPort uint32
}

// StartSSH 启动一个仅监听回环地址的 SSH 服务端。
// 支持 publickey 认证、session 通道（交给 onSession）与 direct-tcpip 端口转发。
func StartSSH(t *testing.T, onSession SessionHandler) *Server {
	t.Helper()

	// 服务端主机密钥
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	// 客户端密钥（测试里直接以 PrivateKey 字段提供，避免依赖 keys 目录）
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	authorized, err := ssh.NewPublicKey(clientPub)
	if err != nil {
		t.Fatalf("client public key: %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(clientPriv)
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	srvCfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if bytes.Equal(key.Marshal(), authorized.Marshal()) {
				return nil, nil
			}
			return nil, fmt.Errorf("unknown public key")
		},
	}
	srvCfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serveConn(nc, srvCfg, onSession)
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return &Server{
		Host:          "127.0.0.1",
		Port:          addr.Port,
		User:          "tester",
		PrivateKeyPEM: string(keyPEM),
	}
}

func serveConn(nc net.Conn, cfg *ssh.ServerConfig, onSession SessionHandler) {
	defer nc.Close()
	sconn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		switch newCh.ChannelType() {
		case "session":
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				continue
			}
			if onSession != nil {
				go onSession(ch, chReqs)
			} else {
				go ssh.DiscardRequests(chReqs)
				ch.Close()
			}
		case "direct-tcpip":
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				continue
			}
			go ssh.DiscardRequests(chReqs)
			go forward(ch, newCh.ExtraData())
		default:
			_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel")
		}
	}
}

// forward 把 direct-tcpip 通道连到目标地址并双向复制
func forward(ch ssh.Channel, extra []byte) {
	defer ch.Close()
	var payload directTCPIP
	if err := ssh.Unmarshal(extra, &payload); err != nil {
		return
	}
	target, err := net.DialTimeout("tcp",
		net.JoinHostPort(payload.DestAddr, strconv.Itoa(int(payload.DestPort))), 5*time.Second)
	if err != nil {
		return
	}
	defer target.Close()

	done := make(chan struct{}, 2)
	go func() { io.Copy(target, ch); done <- struct{}{} }()
	go func() { io.Copy(ch, target); done <- struct{}{} }()
	<-done
}

// ServeSFTP 返回一个把 session 通道当作 SFTP 子系统服务的处理器
func ServeSFTP(t *testing.T) SessionHandler {
	t.Helper()
	return func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		go func() {
			for req := range reqs {
				// sftp.NewClient 会先发 subsystem 请求
				if req.Type == "subsystem" {
					req.Reply(true, nil)
					continue
				}
				req.Reply(false, nil)
			}
		}()
		server, err := sftp.NewServer(ch)
		if err != nil {
			ch.Close()
			return
		}
		_ = server.Serve()
		ch.Close()
	}
}

// RegisterConnection 写入一个指向测试 SSH 服务端的连接，并初始化 config 存储
func RegisterConnection(t *testing.T, srv *Server) string {
	t.Helper()

	id := "11111111-2222-3333-4444-555555555555"
	conns := []config.Connection{{
		ID:         id,
		Name:       "test-ssh",
		Type:       "ssh",
		Host:       srv.Host,
		Port:       srv.Port,
		Username:   srv.User,
		AuthType:   "key",
		PrivateKey: srv.PrivateKeyPEM,
		Width:      1280,
		Height:     720,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}}
	data, err := json.Marshal(conns)
	if err != nil {
		t.Fatalf("marshal connections: %v", err)
	}
	path := filepath.Join(t.TempDir(), "connections.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write connections: %v", err)
	}
	if err := config.InitDB(path); err != nil {
		t.Fatalf("init config: %v", err)
	}
	return id
}
