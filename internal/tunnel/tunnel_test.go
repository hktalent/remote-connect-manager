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

// useFreePortRange 把转发端口区间收敛到一个空闲端口，避免测试互相抢占
func useFreePortRange(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	oldBase, oldMax := portBase, portMax
	portBase, portMax = port, port
	t.Cleanup(func() {
		closeAll()
		portBase, portMax = oldBase, oldMax
	})
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
	if tn.LocalPort != portBase {
		t.Fatalf("local_port = %d, want %d", tn.LocalPort, portBase)
	}
	if want := connID[:8] + "-" + strconv.Itoa(portBase); tn.ID != want {
		t.Fatalf("id = %q, want %q", tn.ID, want)
	}
	if tn.ConnName != "test-ssh" {
		t.Errorf("connection_name = %q", tn.ConnName)
	}

	// 通过本地端口访问，应被转发到 echo 服务
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tn.LocalPort), 3*time.Second)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want %q", string(buf), "ping")
	}

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
	if listResp.PortBase != portBase || listResp.PortMax != portMax {
		t.Errorf("port range = %d-%d, want %d-%d", listResp.PortBase, listResp.PortMax, portBase, portMax)
	}

	// 删除后本地端口应被释放
	if rec := do(t, r, http.MethodDelete, "/api/tunnels/"+tn.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tn.LocalPort), 200*time.Millisecond)
		if err != nil {
			break
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatalf("删除后端口 %d 仍可连接", tn.LocalPort)
		}
		time.Sleep(50 * time.Millisecond)
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
