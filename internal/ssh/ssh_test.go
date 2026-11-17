package ssh

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"remote-connect-manager/internal/testutil"
)

// tmux 等全屏程序会把多字节字符截断在 4KB 读边界上，
// 若用文本帧发送，浏览器会以 1007 关闭连接。
func TestHandleSSHSendsBinaryFrames(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		go func() {
			for req := range reqs {
				switch req.Type {
				case "pty-req", "shell":
					req.Reply(true, nil)
				default:
					req.Reply(false, nil)
				}
			}
		}()

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			// 把“你”的 UTF-8 字节拆成两段写，模拟读边界截断
			_, _ = ch.Write([]byte{0xe4, 0xbd})
			time.Sleep(30 * time.Millisecond)
			_, _ = ch.Write([]byte{0xa0})
			_, _ = ch.Write([]byte("out\n"))
		}()
		go func() {
			defer wg.Done()
			_, _ = ch.Stderr().Write([]byte("err\n"))
		}()
		wg.Wait()
		time.Sleep(100 * time.Millisecond)
		ch.Close()
	}

	srv := testutil.StartSSH(t, handler)
	id := testutil.RegisterConnection(t, srv)

	r := gin.New()
	r.GET("/ws/ssh/:id", HandleSSH)
	httpSrv := httptest.NewServer(r)
	defer httpSrv.Close()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/ws/ssh/" + id
	c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))

	var got []byte
	done := false
	for !done {
		mt, data, err := c.ReadMessage()
		if err != nil {
			break
		}
		if mt != websocket.BinaryMessage {
			t.Fatalf("期望二进制帧，收到消息类型 %d", mt)
		}
		got = append(got, data...)
		// stdout/stderr 到达顺序不固定，校验前先剔除 stderr 内容
		stdout := bytes.ReplaceAll(got, []byte("err\n"), nil)
		done = bytes.Contains(stdout, []byte("你out")) && bytes.Contains(got, []byte("err\n"))
	}
	stdout := bytes.ReplaceAll(got, []byte("err\n"), nil)
	if !bytes.Contains(stdout, []byte("你out")) || !bytes.Contains(got, []byte("err\n")) {
		t.Fatalf("输出不完整：%q", got)
	}
}
