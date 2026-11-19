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
	"os/exec"
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
	go handleGlobalRequests(sconn, reqs)

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
				go serveExec(ch, chReqs)
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

// handleGlobalRequests 处理全局请求：支持 tcpip-forward（反向/远端转发）测试。
func handleGlobalRequests(sconn *ssh.ServerConn, reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			var p struct {
				Addr string
				Port uint32
			}
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			ln, err := net.Listen("tcp", net.JoinHostPort(p.Addr, strconv.Itoa(int(p.Port))))
			if err != nil {
				req.Reply(false, nil)
				continue
			}
			actual := uint32(ln.Addr().(*net.TCPAddr).Port)
			if err := req.Reply(true, ssh.Marshal(struct{ Port uint32 }{actual})); err != nil {
				ln.Close()
				continue
			}
			go acceptForwarded(sconn, ln, p.Addr, actual)
		case "cancel-tcpip-forward":
			req.Reply(true, nil)
		default:
			req.Reply(false, nil)
		}
	}
}

// acceptForwarded 把远端监听上的连接通过 forwarded-tcpip 通道交回客户端。
func acceptForwarded(sconn *ssh.ServerConn, ln net.Listener, addr string, port uint32) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		origin := c.RemoteAddr().(*net.TCPAddr)
		payload := ssh.Marshal(struct {
			Addr       string
			Port       uint32
			OriginAddr string
			OriginPort uint32
		}{addr, port, origin.IP.String(), uint32(origin.Port)})
		ch, chReqs, err := sconn.OpenChannel("forwarded-tcpip", payload)
		if err != nil {
			c.Close()
			continue
		}
		go ssh.DiscardRequests(chReqs)
		go pipeBoth(ch, c)
	}
}

func pipeBoth(a ssh.Channel, b net.Conn) {
	defer a.Close()
	defer b.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
}

// serveExec 在测试服务端执行 exec 请求（本地 sh），用于验证远端 pwd 之类的命令
func serveExec(ch ssh.Channel, reqs <-chan *ssh.Request) {
	deferredClose := true
	defer func() {
		if deferredClose {
			ch.Close()
		}
	}()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var p struct{ Cmd string }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			cmd := exec.Command("/bin/sh", "-c", p.Cmd)
			cmd.Stdout = ch
			cmd.Stderr = ch.Stderr()
			_ = cmd.Run()
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			ch.CloseWrite()
			return
		case "shell":
			req.Reply(true, nil)
		case "pty-req", "env", "window-change", "signal":
			req.Reply(true, nil)
		default:
			req.Reply(false, nil)
		}
	}
	deferredClose = false
}
