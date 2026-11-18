// 本文件记录每条 SSH 连接交互式 shell 的「当前目录」，供 SFTP 面板打开时直接对齐。
// 目录来自对终端输入的解析（cd 命令），起点用一次 exec 取到的 $HOME。
package ssh

import (
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	xssh "golang.org/x/crypto/ssh"
)

var (
	cwdMu   sync.RWMutex
	cwdByID = map[string]string{}
)

// SetCwd 记录某条连接当前目录
func SetCwd(id, dir string) {
	if id == "" || dir == "" {
		return
	}
	cwdMu.Lock()
	cwdByID[id] = dir
	cwdMu.Unlock()
}

// GetCwd 读取某条连接当前目录
func GetCwd(id string) (string, bool) {
	cwdMu.RLock()
	dir, ok := cwdByID[id]
	cwdMu.RUnlock()
	return dir, ok
}

func clearCwd(id string) {
	cwdMu.Lock()
	delete(cwdByID, id)
	cwdMu.Unlock()
}

// Cwd 返回该连接 shell 的当前目录（尽力而为：未建立过会话时 known=false）
func Cwd(c *gin.Context) {
	dir, known := GetCwd(c.Param("id"))
	c.JSON(http.StatusOK, gin.H{"cwd": dir, "known": known})
}

// homeDir 通过一次 exec 取远端 $HOME，作为目录追踪的起点
func homeDir(client *xssh.Client) string {
	s, err := client.NewSession()
	if err != nil {
		return ""
	}
	defer s.Close()
	out, err := s.Output(`printf %s "$HOME"`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// cwdTracker 从终端输入里解析 cd 命令，维护当前目录
type cwdTracker struct {
	mu   sync.Mutex
	buf  []byte
	cwd  string
	home string
}

func (t *cwdTracker) seed(home string) {
	t.mu.Lock()
	t.home = home
	t.cwd = home
	t.mu.Unlock()
}

func (t *cwdTracker) current() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cwd
}

// feed 处理一段终端输入，返回最新目录与「是否发生变化」
func (t *cwdTracker) feed(data []byte) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	changed := false
	for _, b := range data {
		switch b {
		case 0x03, 0x15: // Ctrl-C / Ctrl-U 丢弃当前行
			t.buf = t.buf[:0]
		case 0x7f, 0x08: // 退格
			if n := len(t.buf); n > 0 {
				t.buf = t.buf[:n-1]
			}
		case '\r', '\n':
			if d, ok := t.applyLine(string(t.buf)); ok {
				t.cwd = d
				changed = true
			}
			t.buf = t.buf[:0]
		default:
			if b >= 0x20 {
				t.buf = append(t.buf, b)
			}
		}
	}
	return t.cwd, changed
}

// applyLine 解析一行命令里出现的 cd（兼容 `cd a && ls`、`cd a; ls`）
func (t *cwdTracker) applyLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	for _, seg := range strings.Split(line, ";") {
		for _, part := range strings.Split(seg, "&&") {
			if d, ok := t.parseCd(strings.TrimSpace(part)); ok {
				return d, true
			}
		}
	}
	return "", false
}

func (t *cwdTracker) parseCd(cmd string) (string, bool) {
	if !strings.HasPrefix(cmd, "cd") {
		return "", false
	}
	rest := cmd[2:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return "", false // cdrom 之类不是 cd 命令
	}
	arg := strings.TrimSpace(rest)
	arg = strings.TrimSpace(strings.TrimPrefix(arg, "-P "))
	arg = strings.TrimSpace(strings.TrimPrefix(arg, "-L "))
	arg = strings.TrimSpace(arg)
	if len(arg) >= 2 && (arg[0] == '\'' || arg[0] == '"') && arg[len(arg)-1] == arg[0] {
		arg = arg[1 : len(arg)-1]
	}
	if arg == "" {
		if t.home == "" {
			return "", false
		}
		return t.home, true
	}
	if arg == "-" {
		return "", false // 依赖 OLDPWD，无法推断
	}
	if strings.HasPrefix(arg, "~") {
		if t.home == "" {
			return "", false
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(arg, "~"), "/")
		if rel == "" {
			return t.home, true
		}
		return path.Join(t.home, rel), true
	}
	if strings.HasPrefix(arg, "/") {
		return path.Clean(arg), true
	}
	if t.cwd == "" {
		return "", false // 相对路径需要已知基准目录
	}
	return path.Clean(path.Join(t.cwd, arg)), true
}
