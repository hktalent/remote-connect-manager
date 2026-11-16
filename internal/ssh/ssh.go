package ssh

import (
	"encoding/json"
	"fmt"
	"net/http"
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

	if err := session.Shell(); err != nil {
		ws.WriteJSON(gin.H{"type": "error", "data": err.Error()})
		return
	}

	go pumpToWS(ws, stdout)
	go pumpToWS(ws, stderr)

	done := make(chan struct{})
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
			stdin.Write(data)
		}
		close(done)
	}()

	<-done
}

func pumpToWS(ws *websocket.Conn, r interface{ Read([]byte) (int, error) }) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			ws.WriteMessage(websocket.TextMessage, buf[:n])
		}
		if err != nil {
			break
		}
	}
}
