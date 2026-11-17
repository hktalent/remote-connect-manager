// Package tunnel 提供基于 SSH 的本地端口转发（local forwarding）
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

// Tunnel 一条本地端口转发规则
type Tunnel struct {
	ID         string `json:"id"`
	ConnID     string `json:"connection_id"`
	ConnName   string `json:"connection_name"`
	LocalPort  int    `json:"local_port"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	CreatedAt  string `json:"created_at"`

	client *ssh.Client
	ln     net.Listener
	closed bool
}

var (
	mu      sync.Mutex
	tunnels = map[string]*Tunnel{}

	portBase = envInt("TUNNEL_PORT_BASE", 20000)
	portMax  = envInt("TUNNEL_PORT_MAX", 20010)
)

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
	RemoteHost   string `json:"remote_host"`
	RemotePort   int    `json:"remote_port"`
}

// List 返回当前所有隧道
func List(c *gin.Context) {
	mu.Lock()
	defer mu.Unlock()
	out := make([]*Tunnel, 0, len(tunnels))
	for _, t := range tunnels {
		out = append(out, t)
	}
	c.JSON(http.StatusOK, gin.H{"tunnels": out, "port_base": portBase, "port_max": portMax})
}

// Create 新建一条本地端口转发
func Create(c *gin.Context) {
	var req createReq
	if err := c.ShouldBindJSON(&req); err != nil || req.ConnectionID == "" || req.RemotePort <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "connection_id 与 remote_port 必填"})
		return
	}
	conn, err := config.GetConnectionByID(req.ConnectionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "connection not found"})
		return
	}
	if req.RemoteHost == "" {
		req.RemoteHost = "127.0.0.1"
	}

	client, err := sshx.Dial(conn)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	mu.Lock()
	defer mu.Unlock()
	for port := portBase; port <= portMax; port++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err != nil {
			continue
		}
		t := &Tunnel{
			ID:         fmt.Sprintf("%s-%d", conn.ID[:8], port),
			ConnID:     conn.ID,
			ConnName:   conn.Name,
			LocalPort:  port,
			RemoteHost: req.RemoteHost,
			RemotePort: req.RemotePort,
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
	c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("端口段 %d-%d 已用尽", portBase, portMax)})
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

func (t *Tunnel) pipe(local net.Conn) {
	defer local.Close()
	remote, err := t.client.Dial("tcp", net.JoinHostPort(t.RemoteHost, strconv.Itoa(t.RemotePort)))
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); done <- struct{}{} }()
	go func() { io.Copy(local, remote); done <- struct{}{} }()
	<-done
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
