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
	// 供 /api/ssh/:id/cwd、SFTP 判断会话是否仍然存活
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

	// 追踪交互式 shell 的当前目录（起点用登录用户名本地推断的家目录），SFTP 面板打开时据此定位。
	// 注意：不要在这里到远端 exec 探测 $HOME——登录过程只允许一次标准 shell 请求。
	registerUser(id, conn.Username)
	defer unregisterUser(id)
	tr := &cwdTracker{}
	if home := homeGuess(conn.Username); home != "" {
		tr.seed(home)
		SetCwd(id, home)
	}
	defer clearCwd(id)
	defer clearPromptCwd(id)
	defer clearPendingPwd(id)

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

	_ = session.Setenv("TERM", "xterm-256color")
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

	// 一律走标准 shell 请求（不用 exec 定制启动）；目录还原改成"拿到提示符后再敲一条干净的 cd"
	if err := session.Shell(); err != nil {
		ws.WriteJSON(gin.H{"type": "error", "data": err.Error()})
		return
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

	// 自动还原目录：目录来自最后一次权威输出（提示符里的路径 / 用户敲 pwd 的输出），
	// 登录或重连时在 shell 就绪后以普通输入的方式执行 `cd '<目录>'`。
	// 只发这一条、且只发一次：不做存在性探测、不清屏、不拼接其他命令。
	restoreDir := safeRestoreCwd(c.Query("cwd"))
	if restoreDir == "" {
		if last, ok := GetLastCwd(id); ok {
			restoreDir = safeRestoreCwd(last)
		}
	}
	var stdinMu sync.Mutex
	writeStdin := func(data []byte) {
		stdinMu.Lock()
		_, _ = stdin.Write(data)
		stdinMu.Unlock()
	}
	readers.Add(2)
	go pumpFrom(stdout)
	go pumpFrom(stderr)
	go func() {
		readers.Wait()
		close(out)
	}()
	// 等到"提示符就绪"再敲 cd：横幅/motd 之类先到，若在横幅阶段写入，
	// 命令回显会插在提示符前面（cd 会显示两遍、顺序错乱）。
	promptReady := make(chan struct{})
	var promptOnce sync.Once
	markPromptReady := func() { promptOnce.Do(func() { close(promptReady) }) }
	if restoreDir != "" {
		go func() {
			select {
			case <-promptReady:
			case <-time.After(6 * time.Second): // 提示符不可识别时兜底
			case <-done:
				return
			}
			writeStdin([]byte("cd " + shellQuote(restoreDir) + "\r"))
		}()
	} else {
		markPromptReady() // 无需还原时不要浪费一个挂起 goroutine
	}

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
			if restoreDir != "" && looksLikePrompt(chunk) {
				markPromptReady()
			}
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
			// 用户敲了 pwd：标记一下，用它的输出精确校准当前目录
			notePwdInput(id, data)
			// 从输入里解析 cd，仅同步给 SFTP 面板用。
			// 不在这里做远端 `cd && pwd` 校验（那是多一次 session 执行命令，
			// 且会把半截路径回退成父目录/根目录），也不记入「最后目录」——
			// 只有提示符 / pwd 输出这类权威来源才写「最后目录」。
			if dir, changed := tr.feed(data); changed {
				SetCwd(id, dir)
			}
			writeStdin(data)
		}
		close(done)
	}()

	<-done
}
