// Package tunnel 提供基于 SSH 的端口转发，支持两个方向：
//   - local（正向 / 本地转发）：在本机监听端口，连接转发到远端主机上的目标端口，
//     等价于 ssh -L <本地端口>:<目标主机>:<目标端口> user@跳板机；
//   - remote（反向 / 远端转发）：在远端（跳板机）上监听端口，连接转发回本机的目标端口，
//     等价于 ssh -R <远端端口>:<本机目标主机>:<本机目标端口> user@跳板机。
package tunnel

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/ssh"
	"remote-connect-manager/internal/config"
	sshx "remote-connect-manager/internal/ssh"
)

// Tunnel 一条端口转发规则
type Tunnel struct {
	ID         string `json:"id"`
	ConnID     string `json:"connection_id"`
	ConnName   string `json:"connection_name"`
	Mode       string `json:"mode"`        // local / remote
	ListenHost string `json:"listen_host"` // 监听地址：local 在本机，remote 在跳板机
	ListenPort int    `json:"listen_port"`
	TargetHost string `json:"target_host"` // 目标地址：local 在跳板机侧，remote 在本机侧
	TargetPort int    `json:"target_port"`
	CreatedAt  string `json:"created_at"`

	client *ssh.Client
	ln     net.Listener
	closed bool
}

var (
	mu      sync.Mutex
	tunnels = map[string]*Tunnel{}
)

// portRange 每次读取环境变量，便于测试里换一段空闲端口
func portRange() (int, int) {
	return envInt("TUNNEL_PORT_BASE", 20000), envInt("TUNNEL_PORT_MAX", 20010)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

type createReq struct {
	ConnectionID string `json:"connection_id"`
	Mode         string `json:"mode"` // 空 = local
	// 正向：目标在远端
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	// 反向：目标在本机，监听在远端
	LocalHost  string `json:"local_host"`
	LocalPort  int    `json:"local_port"`
	ListenHost string `json:"listen_host"`
	ListenPort int    `json:"listen_port"`
}

// List 返回当前所有隧道
func List(c *gin.Context) {
	mu.Lock()
	defer mu.Unlock()
	out := make([]*Tunnel, 0, len(tunnels))
	for _, t := range tunnels {
		out = append(out, t)
	}
	base, max := portRange()
	c.JSON(http.StatusOK, gin.H{"tunnels": out, "port_base": base, "port_max": max})
}

// Create 新建一条端口转发（默认正向）
func Create(c *gin.Context) {
	var req createReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ConnectionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "connection_id 必填"})
		return
	}
	if req.Mode == "" {
		req.Mode = "local"
	}
	if req.Mode != "local" && req.Mode != "remote" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode 只能是 local 或 remote"})
		return
	}
	// 先校验端口参数，再查连接，避免参数错误被 404 掩盖
	if req.Mode == "remote" {
		if req.ListenPort <= 0 || req.ListenPort > 65535 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "远端监听端口必填（1-65535）"})
			return
		}
		if req.LocalPort <= 0 || req.LocalPort > 65535 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "本机目标端口必填（1-65535）"})
			return
		}
	} else if req.RemotePort <= 0 || req.RemotePort > 65535 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "目标端口必填（1-65535）"})
		return
	}

	conn, err := config.GetConnectionByID(req.ConnectionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "connection not found"})
		return
	}

	client, err := sshx.Dial(conn)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	if req.Mode == "remote" {
		createRemote(c, conn, client, req)
		return
	}
	createLocal(c, conn, client, req)
}

// createLocal 正向（本地转发）：本机监听 → 远端目标
func createLocal(c *gin.Context, conn *config.Connection, client *ssh.Client, req createReq) {
	if req.RemotePort <= 0 || req.RemotePort > 65535 {
		client.Close()
		c.JSON(http.StatusBadRequest, gin.H{"error": "目标端口必填（1-65535）"})
		return
	}
	if req.RemoteHost == "" {
		req.RemoteHost = "127.0.0.1"
	}

	base, max := portRange()
	mu.Lock()
	defer mu.Unlock()
	for port := base; port <= max; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err != nil {
			continue
		}
		t := &Tunnel{
			ID:         fmt.Sprintf("%s-%d", conn.ID[:8], port),
			ConnID:     conn.ID,
			ConnName:   conn.Name,
			Mode:       "local",
			ListenHost: "127.0.0.1",
			ListenPort: port,
			TargetHost: req.RemoteHost,
			TargetPort: req.RemotePort,
			CreatedAt:  time.Now().Format(time.RFC3339),
			client:     client,
			ln:         ln,
		}
		tunnels[t.ID] = t
		go t.serve()
		c.JSON(http.StatusOK, t)
		return
	}
	client.Close()
	c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("端口段 %d-%d 已用尽", base, max)})
}

// createRemote 反向（远端转发）：跳板机监听 → 本机目标
func createRemote(c *gin.Context, conn *config.Connection, client *ssh.Client, req createReq) {
	if req.ListenPort <= 0 || req.ListenPort > 65535 {
		client.Close()
		c.JSON(http.StatusBadRequest, gin.H{"error": "远端监听端口必填（1-65535）"})
		return
	}
	if req.LocalPort <= 0 || req.LocalPort > 65535 {
		client.Close()
		c.JSON(http.StatusBadRequest, gin.H{"error": "本机目标端口必填（1-65535）"})
		return
	}
	if req.LocalHost == "" {
		req.LocalHost = "127.0.0.1"
	}
	if req.ListenHost == "" {
		req.ListenHost = "127.0.0.1"
	}

	ln, err := client.Listen("tcp", net.JoinHostPort(req.ListenHost, strconv.Itoa(req.ListenPort)))
	if err != nil {
		client.Close()
		msg := fmt.Sprintf("远端拒绝监听 %s:%d：%v", req.ListenHost, req.ListenPort, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": msg})
		return
	}

	t := &Tunnel{
		ID:         fmt.Sprintf("%s-r%d", conn.ID[:8], req.ListenPort),
		ConnID:     conn.ID,
		ConnName:   conn.Name,
		Mode:       "remote",
		ListenHost: req.ListenHost,
		ListenPort: req.ListenPort,
		TargetHost: req.LocalHost,
		TargetPort: req.LocalPort,
		CreatedAt:  time.Now().Format(time.RFC3339),
		client:     client,
		ln:         ln,
	}

	mu.Lock()
	if old, ok := tunnels[t.ID]; ok { // 同端口重建：先关旧的，避免监听泄漏
		delete(tunnels, t.ID)
		mu.Unlock()
		old.close()
		mu.Lock()
	}
	tunnels[t.ID] = t
	mu.Unlock()

	go t.serve()
	c.JSON(http.StatusOK, t)
}

// Delete 关闭并移除隧道
func Delete(c *gin.Context) {
	mu.Lock()
	t, ok := tunnels[c.Param("id")]
	if ok {
		delete(tunnels, c.Param("id"))
	}
	mu.Unlock()
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "tunnel not found"})
		return
	}
	t.close()
	c.JSON(http.StatusOK, gin.H{"deleted": t.ID})
}

func (t *Tunnel) serve() {
	for {
		local, err := t.ln.Accept()
		if err != nil {
			return
		}
		go t.pipe(local)
	}
}

func (t *Tunnel) pipe(in net.Conn) {
	defer in.Close()
	out, err := t.dialTarget()
	if err != nil {
		return
	}
	defer out.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(out, in); done <- struct{}{} }()
	go func() { io.Copy(in, out); done <- struct{}{} }()
	<-done
}

// dialTarget 按方向连接目标：正向经 SSH 通道连远端，反向直接连本机
func (t *Tunnel) dialTarget() (net.Conn, error) {
	addr := net.JoinHostPort(t.TargetHost, strconv.Itoa(t.TargetPort))
	if t.Mode == "remote" {
		return net.DialTimeout("tcp", addr, 5*time.Second)
	}
	return t.client.Dial("tcp", addr)
}

func (t *Tunnel) close() {
	mu.Lock()
	t.closed = true
	mu.Unlock()
	if t.ln != nil {
		t.ln.Close()
	}
	if t.client != nil {
		t.client.Close()
	}
}
