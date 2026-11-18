package tunnel

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"remote-connect-manager/internal/testutil"
)

func newRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/tunnels", List)
	r.POST("/api/tunnels", Create)
	r.DELETE("/api/tunnels/:id", Delete)
	return r
}

// useFreePortRange 把正向转发的本地端口区间收敛到一个空闲端口，避免测试互相抢占
func useFreePortRange(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	t.Setenv("TUNNEL_PORT_BASE", strconv.Itoa(port))
	t.Setenv("TUNNEL_PORT_MAX", strconv.Itoa(port))
	t.Cleanup(func() { closeAll() })
}

// closeAll 关闭并清空所有隧道（避免全局状态串测）
func closeAll() {
	mu.Lock()
	list := make([]*Tunnel, 0, len(tunnels))
	for id, tn := range tunnels {
		list = append(list, tn)
		delete(tunnels, id)
	}
	mu.Unlock()
	for _, tn := range list {
		tn.close()
	}
}

// startEcho 启动一个原样回显的 TCP 服务，作为转发目标
func startEcho(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func do(t *testing.T, r *gin.Engine, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// roundTrip 连接 addr，发送 msg，期望原样回显
func roundTrip(t *testing.T, addr, msg string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != msg {
		t.Fatalf("echo = %q, want %q", buf, msg)
	}
}

func TestCreateTunnelForwardsTraffic(t *testing.T) {
	useFreePortRange(t)

	srv := testutil.StartSSH(t, nil)
	connID := testutil.RegisterConnection(t, srv)
	echoPort := startEcho(t)
	r := newRouter()

	body := fmt.Sprintf(`{"connection_id":%q,"remote_host":"127.0.0.1","remote_port":%d}`, connID, echoPort)
	rec := do(t, r, http.MethodPost, "/api/tunnels", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var tn Tunnel
	if err := json.Unmarshal(rec.Body.Bytes(), &tn); err != nil {
		t.Fatal(err)
	}
	if tn.Mode != "local" {
		t.Errorf("mode = %q, want local", tn.Mode)
	}
	base, _ := portRange()
	if tn.ListenPort != base {
		t.Fatalf("listen_port = %d, want %d", tn.ListenPort, base)
	}
	if want := connID[:8] + "-" + strconv.Itoa(base); tn.ID != want {
		t.Fatalf("id = %q, want %q", tn.ID, want)
	}
	if tn.ConnName != "test-ssh" {
		t.Errorf("connection_name = %q", tn.ConnName)
	}
	if tn.TargetPort != echoPort || tn.TargetHost != "127.0.0.1" {
		t.Errorf("target = %s:%d, want 127.0.0.1:%d", tn.TargetHost, tn.TargetPort, echoPort)
	}

	// 通过本地端口访问，应被转发到 echo 服务
	roundTrip(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(tn.ListenPort)), "ping")

	// 列表接口应返回该隧道与端口区间
	rec = do(t, r, http.MethodGet, "/api/tunnels", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var listResp struct {
		Tunnels  []*Tunnel `json:"tunnels"`
		PortBase int       `json:"port_base"`
		PortMax  int       `json:"port_max"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Tunnels) != 1 || listResp.Tunnels[0].ID != tn.ID {
		t.Fatalf("tunnels = %+v", listResp.Tunnels)
	}
	if listResp.PortBase != base || listResp.PortMax != base {
		t.Errorf("port range = %d-%d, want %d-%d", listResp.PortBase, listResp.PortMax, base, base)
	}

	// 删除后本地端口应被释放
	if rec := do(t, r, http.MethodDelete, "/api/tunnels/"+tn.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(tn.ListenPort)), 200*time.Millisecond)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatalf("删除后端口 %d 仍可连接", tn.ListenPort)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestCreateRemoteTunnel 反向转发：跳板机端口 → 本机服务
func TestCreateRemoteTunnel(t *testing.T) {
	useFreePortRange(t)

	srv := testutil.StartSSH(t, nil) // 测试服务端实现了 tcpip-forward
	connID := testutil.RegisterConnection(t, srv)
	echoPort := startEcho(t)
	r := newRouter()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenPort := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	body := fmt.Sprintf(`{"connection_id":%q,"mode":"remote","listen_host":"127.0.0.1","listen_port":%d,"local_host":"127.0.0.1","local_port":%d}`,
		connID, listenPort, echoPort)
	rec := do(t, r, http.MethodPost, "/api/tunnels", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var tn Tunnel
	if err := json.Unmarshal(rec.Body.Bytes(), &tn); err != nil {
		t.Fatal(err)
	}
	if tn.Mode != "remote" {
		t.Errorf("mode = %q, want remote", tn.Mode)
	}
	if tn.ListenPort != listenPort || tn.TargetPort != echoPort {
		t.Errorf("listen=%d target=%d, want %d / %d", tn.ListenPort, tn.TargetPort, listenPort, echoPort)
	}
	if tn.ID != connID[:8]+"-r"+strconv.Itoa(listenPort) {
		t.Errorf("id = %q", tn.ID)
	}

	// 连接“跳板机上的监听端口”，应被转发回本机 echo 服务
	listenAddr := net.JoinHostPort(tn.ListenHost, strconv.Itoa(tn.ListenPort))
	roundTrip(t, listenAddr, "hello-remote")

	if rec := do(t, r, http.MethodDelete, "/api/tunnels/"+tn.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCreateTunnelValidation(t *testing.T) {
	useFreePortRange(t)
	r := newRouter()

	cases := []struct {
		name string
		body string
		want int
	}{
		{"缺少 connection_id", `{"remote_port":22}`, http.StatusBadRequest},
		{"缺少 remote_port", `{"connection_id":"11111111-2222-3333-4444-555555555555"}`, http.StatusBadRequest},
		{"连接不存在", `{"connection_id":"deadbeef-dead-beef-dead-beefdeadbeef","remote_port":22}`, http.StatusNotFound},
		{"非法 mode", `{"connection_id":"11111111-2222-3333-4444-555555555555","mode":"sideways","remote_port":22}`, http.StatusBadRequest},
		{"反向缺端口", `{"connection_id":"11111111-2222-3333-4444-555555555555","mode":"remote"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rec := do(t, r, http.MethodPost, "/api/tunnels", tc.body); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestDeleteUnknownTunnel(t *testing.T) {
	useFreePortRange(t)
	r := newRouter()
	if rec := do(t, r, http.MethodDelete, "/api/tunnels/not-exist", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("RCM_TEST_ENV_INT", "abc")
	if got := envInt("RCM_TEST_ENV_INT", 7); got != 7 {
		t.Errorf("非法值 = %d, want 7", got)
	}
	t.Setenv("RCM_TEST_ENV_INT", "42")
	if got := envInt("RCM_TEST_ENV_INT", 7); got != 42 {
		t.Errorf("合法值 = %d, want 42", got)
	}
	t.Setenv("RCM_TEST_ENV_INT", "0")
	if got := envInt("RCM_TEST_ENV_INT", 7); got != 7 {
		t.Errorf("非正值 = %d, want 7", got)
	}
}
