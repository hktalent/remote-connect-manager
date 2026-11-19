package ssh

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
	"remote-connect-manager/internal/config"
	"remote-connect-manager/internal/keys"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Dial 建立到目标主机的 SSH 连接，供 SFTP 与端口转发复用
func Dial(conn *config.Connection) (*ssh.Client, error) {
	authMethods := []ssh.AuthMethod{}
	if conn.AuthType == "key" || conn.AuthType == "" {
		var signer ssh.Signer
		var err error
		if conn.KeyName != "" {
			signer, err = keys.LoadSigner(conn.KeyName, conn.Password)
		} else if conn.PrivateKey != "" {
			signer, err = ssh.ParsePrivateKey([]byte(conn.PrivateKey))
		}
		if signer != nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		} else if err != nil {
			return nil, err
		}
	}
	if conn.Password != "" {
		authMethods = append(authMethods, ssh.Password(conn.Password))
	}
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("没有可用的认证方式：请选择密钥或填写密码")
	}

	cfg := &ssh.ClientConfig{
		User:            conn.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	return ssh.Dial("tcp", fmt.Sprintf("%s:%d", conn.Host, conn.Port), cfg)
}

type wsClientMsg struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Rows int    `json:"rows"`
	Cols int    `json:"cols"`
}

func HandleSSH(c *gin.Context) {
	id := c.Param("id")
	conn, err := config.GetConnectionByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "connection not found"})
		return
	}

	ws, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	authMethods := []ssh.AuthMethod{}
	if conn.AuthType == "key" || conn.AuthType == "" {
		var signer ssh.Signer
		var signerErr error
		if conn.KeyName != "" {
			// 加密密钥用密码栏作为口令
			signer, signerErr = keys.LoadSigner(conn.KeyName, conn.Password)
		} else if conn.PrivateKey != "" {
			signer, signerErr = ssh.ParsePrivateKey([]byte(conn.PrivateKey))
		}
		if signer != nil {
			authMethods = append(authMethods, ssh.PublicKeys(signer))
		} else if signerErr != nil {
			ws.WriteJSON(gin.H{"type": "error", "data": signerErr.Error()})
		}
	}
	// 密码认证：既支持密码登录，也作为密钥认证的兜底
	if conn.Password != "" {
		authMethods = append(authMethods, ssh.Password(conn.Password))
	}
	if len(authMethods) == 0 {
		ws.WriteJSON(gin.H{"type": "error", "data": "没有可用的认证方式：请选择密钥或填写密码"})
		return
	}

	sshConfig := &ssh.ClientConfig{
		User:            conn.Username,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", conn.Host, conn.Port)
	sshClient, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		ws.WriteJSON(gin.H{"type": "error", "data": err.Error()})
		return
	}
	defer sshClient.Close()
	// 供 /api/ssh/:id/cwd 在远端校验 pwd 用
	registerClient(id, sshClient)
	defer unregisterClient(id)

	// 链路保活：长时间无输入时，NAT/防火墙/sshd 的 ClientAlive 常把空闲连接掐掉，
	// 表现为「连着好好的，过一会自己断开」。每 30s 发一次 keepalive 请求，
	// 失败就主动关掉 WebSocket，让前端立刻显示离线并可一键重连。
	keepStop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-keepStop:
				return
			case <-ticker.C:
				if _, _, err := sshClient.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					ws.Close()
					return
				}
			}
		}
	}()
	defer close(keepStop)

	// 追踪交互式 shell 的当前目录（起点为远端的 $HOME），SFTP 面板打开时据此定位
	tr := &cwdTracker{}
	if home := homeDir(sshClient); home != "" {
		tr.seed(home)
		SetCwd(id, home)
	}
	defer clearCwd(id)
	defer clearPromptCwd(id)

	session, err := sshClient.NewSession()
	if err != nil {
		ws.WriteJSON(gin.H{"type": "error", "data": err.Error()})
		return
	}
	defer session.Close()

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	cols, rows := 120, 40
	if conn.Width > 0 {
		cols = conn.Width / 8
	}
	if conn.Height > 0 {
		rows = conn.Height / 16
	}
	if cols < 1 {
		cols = 120
	}
	if rows < 1 {
		rows = 40
	}

	if err := session.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		ws.WriteJSON(gin.H{"type": "error", "data": err.Error()})
		return
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		return
	}

	// 重连时带上 ?cwd=，直接在目标目录里拉起 shell，避免先 cd 一次把屏幕刷脏
	shellStarted := false
	if dir := safeRestoreCwd(c.Query("cwd")); dir != "" {
		// 交互式 shell 从指定目录启动；$SHELL 取登录 shell，没有则退回 /bin/sh
		cmd := fmt.Sprintf("cd -- %s && exec ${SHELL:-/bin/sh}", shellQuote(dir))
		if err := session.Start(cmd); err == nil {
			shellStarted = true
		}
	}
	if !shellStarted {
		if err := session.Shell(); err != nil {
			ws.WriteJSON(gin.H{"type": "error", "data": err.Error()})
			return
		}
		// 远端不允许 exec 时退化为：先 cd 再 clear，保证目录仍被还原
		if dir := safeRestoreCwd(c.Query("cwd")); dir != "" {
			_, _ = stdin.Write([]byte(fmt.Sprintf("cd -- %s && clear\r", shellQuote(dir))))
		}
	}

	// PTY 输出必须用二进制帧：文本帧要求整帧是合法 UTF-8，而 4KB 的读取
	// 边界可能把多字节字符截断，浏览器会以 1007 关闭连接（tmux 等全屏程序
	// 输出量大、多字节字符多，最容易触发）。同时只保留一个写协程，避免
	// stdout/stderr 并发写同一条 WebSocket。
	done := make(chan struct{})
	out := make(chan []byte, 64)
	var readers sync.WaitGroup

	pumpFrom := func(r io.Reader) {
		defer readers.Done()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case out <- chunk:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}

	readers.Add(2)
	go pumpFrom(stdout)
	go pumpFrom(stderr)
	go func() {
		readers.Wait()
		close(out)
	}()

	// WebSocket 层心跳：代理/反代对空闲连接也有超时，定期 Ping 保活
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-keepStop:
				return
			case <-ticker.C:
				_ = ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			}
		}
	}()

	go func() {
		for chunk := range out {
			// 顺带从终端输出里识别提示符中的目录（zsh 的 [~] 样式），供 SFTP 对齐
			scanPromptCwd(id, chunk)
			if err := ws.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
				ws.Close()
				return
			}
		}
		ws.Close()
	}()

	go func() {
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				break
			}
			var msg wsClientMsg
			if err := json.Unmarshal(data, &msg); err == nil && msg.Type == "resize" {
				session.WindowChange(msg.Rows, msg.Cols)
				continue
			}
			// 从输入里解析 cd，同步给 SFTP 面板
			if dir, changed := tr.feed(data); changed {
				SetCwd(id, dir)
			}
			stdin.Write(data)
		}
		close(done)
	}()

	<-done
}
