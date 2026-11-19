// 本文件记录每条 SSH 连接交互式 shell 的「当前目录」，供 SFTP 面板打开时直接对齐。
// 目录来自对终端输入的解析（cd 命令），起点用一次 exec 取到的 $HOME。
package ssh

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"remote-connect-manager/internal/config"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	xssh "golang.org/x/crypto/ssh"
)

var (
	cwdMu   sync.RWMutex
	cwdByID = map[string]string{}

	// 最后一次确认过的目录（断开后仍保留，重连时用它还原）
	lastMu      sync.RWMutex
	lastCwdByID = map[string]string{}

	// 从提示符里读到的目录（如 zsh 的 ┌──(root)-[/home/kali]），比解析 cd 更可靠
	promptMu   sync.RWMutex
	promptByID = map[string]string{}
	promptTail = map[string][]byte{}

	clientMu sync.RWMutex
	clients  = map[string]*xssh.Client{}
)

// registerClient 记录连接对应的 SSH 客户端，供 /cwd 接口在远端执行 pwd 校验
func registerClient(id string, client *xssh.Client) {
	if id == "" || client == nil {
		return
	}
	clientMu.Lock()
	clients[id] = client
	clientMu.Unlock()
}

func unregisterClient(id string) {
	clientMu.Lock()
	delete(clients, id)
	clientMu.Unlock()
}

func liveClient(id string) *xssh.Client {
	clientMu.RLock()
	c := clients[id]
	clientMu.RUnlock()
	return c
}

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
// cwdCachePath 最后目录的落盘位置（与连接配置同目录），重启后仍能还原
func cwdCachePath() string {
	p := config.DataPath()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "cwd.json")
}

var lastLoadOnce sync.Once

func loadLastFromDisk() {
	path := cwdCachePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(data, &m) != nil {
		return
	}
	lastMu.Lock()
	for k, v := range m {
		// 历史上被错误回退成根目录的记录直接丢弃，避免重连后跳到 /
		if v != "" && v != "/" {
			lastCwdByID[k] = v
		}
	}
	lastMu.Unlock()
}

func saveLastToDisk() {
	path := cwdCachePath()
	if path == "" {
		return
	}
	lastMu.RLock()
	data, err := json.Marshal(lastCwdByID)
	lastMu.RUnlock()
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

// rememberLast 记录该连接最后一次确认的目录（跨会话保留）
func rememberLast(id, dir string) {
	if id == "" || dir == "" || dir == "/" {
		return
	}
	lastLoadOnce.Do(loadLastFromDisk)
	lastMu.Lock()
	changed := lastCwdByID[id] != dir
	lastCwdByID[id] = dir
	lastMu.Unlock()
	if changed {
		saveLastToDisk()
	}
}

// GetLastCwd 返回该连接最后确认的目录（SSH 断开后仍可用，供重连还原）
func GetLastCwd(id string) (string, bool) {
	lastLoadOnce.Do(loadLastFromDisk)
	lastMu.RLock()
	dir, ok := lastCwdByID[id]
	lastMu.RUnlock()
	return dir, ok
}

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
	id := c.Param("id")
	dir, known := GetCwd(id)
	if pdir, ok := GetPromptCwd(id); ok && pdir != "" {
		// 提示符里直接写着目录，最接近用户看到的 pwd，优先使用
		dir, known = pdir, true
	}
	client := liveClient(id)
	if client == nil {
		// 会话已断开：用断开前记住的目录，保证重连能回到原处
		if dir == "" {
			if last, ok := GetLastCwd(id); ok {
				dir, known = last, true
			}
		}
		c.JSON(http.StatusOK, gin.H{"cwd": dir, "known": known})
		return
	}
	if client != nil {
		if dir == "" {
			if home := homeDir(client); home != "" {
				dir, known = home, true
			}
		}
		// 交给远端执行 `cd <dir> && pwd -P`：拿到真实路径（解析 ~ / .. / 软链接）
		if strings.HasPrefix(dir, "~") {
			if home := homeDir(client); home != "" {
				dir = path.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/"))
			}
		}
		if dir != "" {
			if real := resolveExisting(client, dir); real != "" {
				if real != dir {
					SetCwd(id, real)
				}
				dir = real
				rememberLast(id, real)
			} else if last, ok := GetLastCwd(id); ok && last != "" {
				// 跟踪到的路径不存在（Tab 补全只记了半截）：退回最后一次确认过的目录
				SetCwd(id, last)
				dir = last
			} else {
				// 既不存在又没有历史：不要把无效路径当成当前目录
				dir, known = "", false
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"cwd": dir, "known": known})
}

var (
	pendingPwdMu sync.RWMutex
	pendingPwd   = map[string]bool{}
	inputBufByID = map[string]string{}
)

// notePwdInput 记录按键流：回车那一刻如果整行就是 pwd，就等它的输出校准目录
func notePwdInput(id string, data []byte) {
	pendingPwdMu.Lock()
	defer pendingPwdMu.Unlock()
	buf := inputBufByID[id]
	for _, r := range string(data) {
		switch {
		case r == '\r' || r == '\n':
			if strings.TrimSpace(buf) == "pwd" {
				pendingPwd[id] = true
			}
			buf = ""
		case r == 0x7f || r == '\b':
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case r < 0x20:
			// Ctrl-C/Ctrl-U 之类：丢弃当前行
			buf = ""
		default:
			buf += string(r)
		}
	}
	if len(buf) > 512 {
		buf = buf[len(buf)-512:]
	}
	inputBufByID[id] = buf
}

func takePendingPwd(id string) bool {
	pendingPwdMu.Lock()
	on := pendingPwd[id]
	delete(pendingPwd, id)
	pendingPwdMu.Unlock()
	return on
}

func clearPendingPwd(id string) {
	pendingPwdMu.Lock()
	delete(pendingPwd, id)
	delete(inputBufByID, id)
	pendingPwdMu.Unlock()
}

// ansiRe 去掉 ANSI 转义序列
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)")

// promptPathRe 提示符里的目录，如 [/home/kali]、[~/work]
var promptPathRe = regexp.MustCompile(`\[((?:/|~)[^\[\]\r\n]{0,200})\][^\r\n]{0,80}[#$%>]`)

// promptPathRe2 bash 风格 user@host:/home/kali$ 、user@host:~/work#
var promptPathRe2 = regexp.MustCompile(`@[^:\r\n]{0,60}:((?:/|~)[^\r\n#$%\s]{0,200})[#$%]`)

// scanPromptCwd 从一段终端输出里提取提示符中的目录
func scanPromptCwd(id string, chunk []byte) {
	if id == "" || len(chunk) == 0 {
		return
	}
	clean := ansiRe.ReplaceAllString(string(chunk), "")

	promptMu.Lock()
	tail := append(promptTail[id], clean...)
	if len(tail) > 2048 {
		tail = tail[len(tail)-2048:]
	}
	promptTail[id] = tail
	matches := promptPathRe.FindAllStringSubmatch(string(tail), -1)
	var dir string
	if len(matches) > 0 {
		dir = matches[len(matches)-1][1] // 取最后一个提示符
	} else if m2 := promptPathRe2.FindAllStringSubmatch(string(tail), -1); len(m2) > 0 {
		dir = m2[len(m2)-1][1]
	}
	promptMu.Unlock()

	// pwd 的输出就是绝对路径，直接采信（比解析 cd 更准）
	if takePendingPwd(id) {
		for _, ln := range strings.Split(clean, "\n") {
			ln = strings.TrimRight(ln, "\r ")
			if strings.HasPrefix(ln, "/") && !strings.ContainsAny(ln, " \t") && ln != "/" {
				SetCwd(id, ln)
				rememberLast(id, ln)
				break
			}
		}
	}

	if dir == "" {
		return
	}
	promptMu.Lock()
	promptByID[id] = dir
	promptMu.Unlock()
	if !strings.HasPrefix(dir, "~") {
		rememberLast(id, dir)
	}
}

// GetPromptCwd 返回提示符里解析出的目录
func GetPromptCwd(id string) (string, bool) {
	promptMu.RLock()
	dir, ok := promptByID[id]
	promptMu.RUnlock()
	return dir, ok
}

func clearPromptCwd(id string) {
	promptMu.Lock()
	delete(promptByID, id)
	delete(promptTail, id)
	promptMu.Unlock()
}

// resolveExisting 逐级向上找一个真实存在的目录，全都不存在返回空串
// 只认「这个目录本身存在」，绝不再向上回退到父目录/根目录：
// 回退会把半截路径（Tab 补全只记录到一半）解析成 /，进而把 / 当成当前目录记下来
func resolveExisting(client *xssh.Client, dir string) string {
	if dir == "" || dir == "." {
		return ""
	}
	return resolveDir(client, dir)
}

// resolveDir 在远端把目录转成绝对真实路径，失败返回空串
func resolveDir(client *xssh.Client, dir string) string {
	s, err := client.NewSession()
	if err != nil {
		return ""
	}
	defer s.Close()
	out, err := s.Output(fmt.Sprintf("cd -- %s 2>/dev/null && pwd", shellQuote(dir)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// safeRestoreCwd 校验重连时要还原的目录：必须是绝对路径、无控制字符
func safeRestoreCwd(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" || !strings.HasPrefix(dir, "/") || len(dir) > 1024 {
		return ""
	}
	if strings.ContainsAny(dir, "\r\n\x00") {
		return ""
	}
	return dir
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
	prev string
	home string
	esc  bool // 正在吞掉 ESC/CSI 转义序列
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
		// 方向键 / 历史回溯等转义序列里的字符不是真实输入，不能计入命令行
		if t.esc {
			if b >= 0x40 && b <= 0x7e && b != '[' && b != 'O' {
				t.esc = false
			}
			continue
		}
		if b == 0x1b {
			t.esc = true
			continue
		}
		switch b {
		case 0x03, 0x15: // Ctrl-C / Ctrl-U 丢弃当前行
			t.buf = t.buf[:0]
		case 0x7f, 0x08: // 退格
			if n := len(t.buf); n > 0 {
				t.buf = t.buf[:n-1]
			}
		case '\r', '\n':
			if d, ok := t.applyLine(string(t.buf)); ok {
				t.prev = t.cwd
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
		if t.prev == "" {
			return "", false
		}
		return t.prev, true
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
