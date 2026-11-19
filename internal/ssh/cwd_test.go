package ssh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"remote-connect-manager/internal/config"
	"remote-connect-manager/internal/testutil"
)

func newTracker() *cwdTracker {
	tr := &cwdTracker{}
	tr.seed("/home/u")
	return tr
}

func feed(t *testing.T, tr *cwdTracker, s string) string {
	t.Helper()
	dir, _ := tr.feed([]byte(s))
	return dir
}

func TestCwdTrackerAbsolute(t *testing.T) {
	tr := newTracker()
	if got := feed(t, tr, "cd /etc\r"); got != "/etc" {
		t.Fatalf("cd /etc -> %q", got)
	}
}

func TestCwdTrackerRelative(t *testing.T) {
	tr := newTracker()
	feed(t, tr, "cd /var\r")
	if got := feed(t, tr, "cd log\r"); got != "/var/log" {
		t.Fatalf("relative cd -> %q, want /var/log", got)
	}
	if got := feed(t, tr, "cd ..\r"); got != "/var" {
		t.Fatalf("cd .. -> %q, want /var", got)
	}
}

func TestCwdTrackerHome(t *testing.T) {
	tr := newTracker()
	feed(t, tr, "cd /etc\r")
	if got := feed(t, tr, "cd\r"); got != "/home/u" {
		t.Fatalf("裸 cd -> %q, want /home/u", got)
	}
	if got := feed(t, tr, "cd ~/work\r"); got != "/home/u/work" {
		t.Fatalf("cd ~/work -> %q", got)
	}
}

func TestCwdTrackerQuotedAndBackspace(t *testing.T) {
	tr := newTracker()
	if got := feed(t, tr, "cd '/tmp/a b'\r"); got != "/tmp/a b" {
		t.Fatalf("quoted -> %q", got)
	}
	if got := feed(t, tr, "cd /tmpx\x7f\r"); got != "/tmp" {
		t.Fatalf("backspace -> %q, want /tmp", got)
	}
}

func TestCwdTrackerIgnoresOtherCommands(t *testing.T) {
	tr := newTracker()
	feed(t, tr, "cd /srv\r")
	if got := feed(t, tr, "ls -la\r"); got != "/srv" {
		t.Fatalf("ls 不应改变目录, got %q", got)
	}
	if got := feed(t, tr, "cdrom\r"); got != "/srv" {
		t.Fatalf("cdrom 不是 cd, got %q", got)
	}
	if got := feed(t, tr, "cd -\r"); got != "/home/u" {
		t.Fatalf("cd - 应回到上一个目录 /home/u, got %q", got)
	}
}

// 方向键 / 历史回溯是 ESC 序列，不能污染命令行缓冲
func TestCwdTrackerIgnoresArrowKeys(t *testing.T) {
	tr := newTracker()
	feed(t, tr, "cd /srv\r")
	if got := feed(t, tr, "\x1b[A\x1b[A\r"); got != "/srv" {
		t.Fatalf("方向键不应改变目录, got %q", got)
	}
	if got := feed(t, tr, "cd /opt\r"); got != "/opt" {
		t.Fatalf("方向键后正常输入 cd /opt -> %q", got)
	}
}

func TestCwdTrackerChainedCommand(t *testing.T) {
	tr := newTracker()
	if got := feed(t, tr, "cd /opt/app && ls\r"); got != "/opt/app" {
		t.Fatalf("chained -> %q", got)
	}
	if got := feed(t, tr, "cd /data; echo hi\r"); got != "/data" {
		t.Fatalf("semicolon -> %q", got)
	}
}

func TestCwdGlobalMap(t *testing.T) {
	SetCwd("conn-a", "/srv/app")
	if dir, ok := GetCwd("conn-a"); !ok || dir != "/srv/app" {
		t.Fatalf("GetCwd = %q, %v", dir, ok)
	}
	clearCwd("conn-a")
	if _, ok := GetCwd("conn-a"); ok {
		t.Fatalf("clearCwd 后不应再有记录")
	}
}

func TestCwdTrackerAbsoluteWithoutBase(t *testing.T) {
	tr := &cwdTracker{} // 未 seed（$HOME 探测失败时）
	if got := feed(t, tr, "cd /opt\r"); got != "/opt" {
		t.Fatalf("绝对路径无需基准目录, got %q", got)
	}
	tr2 := &cwdTracker{} // 基准目录仍未知
	if got := feed(t, tr2, "cd sub\r"); got != "" {
		t.Fatalf("未知基准时相对路径不应生效, got %q", got)
	}
}

// /api/ssh/:id/cwd 会用远端 `pwd -P` 校正追踪到的目录
func TestCwdResolvesRemotePath(t *testing.T) {
	srv := testutil.StartSSH(t, nil)
	connID := testutil.RegisterConnection(t, srv)
	conn, err := config.GetConnectionByID(connID)
	if err != nil {
		t.Fatal(err)
	}
	client, err := Dial(conn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	registerClient(connID, client)
	defer unregisterClient(connID)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/ssh/:id/cwd", Cwd)

	SetCwd(connID, "/tmp")
	if got := callCwd(t, r, connID); got != "/tmp" {
		t.Fatalf("cwd = %q, want /tmp", got)
	}

	// 目录已不存在时应回退到仍存在的父目录
	SetCwd(connID, "/tmp/rcm-not-exist-xyz")
	if got := callCwd(t, r, connID); got != "/tmp" {
		t.Fatalf("不存在的目录应回退到 /tmp, got %q", got)
	}
}

func callCwd(t *testing.T, r *gin.Engine, id string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/ssh/"+id+"/cwd", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var resp struct {
		Cwd   string `json:"cwd"`
		Known bool   `json:"known"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Cwd
}

// 提示符里带目录（zsh 的 ┌──(root)-[/home/kali] 样式）时应能识别，并优先于 cd 解析
func TestCwdFromPrompt(t *testing.T) {
	clearPromptCwd("conn-p")
	// 带 ANSI 颜色的提示符
	scanPromptCwd("conn-p", []byte("\x1b[32m┌──(root㉿host)-[\x1b[34m/home/kali\x1b[0m\x1b[32m]└─# \x1b[0m"))
	if dir, ok := GetPromptCwd("conn-p"); !ok || dir != "/home/kali" {
		t.Fatalf("GetPromptCwd = %q, %v", dir, ok)
	}
	// 后续命令输出不应清掉它
	scanPromptCwd("conn-p", []byte("ok\r\n"))
	if dir, _ := GetPromptCwd("conn-p"); dir != "/home/kali" {
		t.Fatalf("普通输出后应保留: %q", dir)
	}
	if dir, ok := GetPromptCwd("conn-p"); !ok || dir != "/home/kali" {
		t.Fatalf("unexpected %q", dir)
	}
}

func TestCwdPrefersPromptOverTracker(t *testing.T) {
	srv := testutil.StartSSH(t, nil)
	connID := testutil.RegisterConnection(t, srv)
	conn, err := config.GetConnectionByID(connID)
	if err != nil {
		t.Fatal(err)
	}
	client, err := Dial(conn)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	registerClient(connID, client)
	defer unregisterClient(connID)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/ssh/:id/cwd", Cwd)

	// cd 解析得到的目录不存在（如 Tab 补全路径只记录了一半）
	SetCwd(connID, "/tmp/rcm-typo-dir")
	// 提示符里是真实目录
	clearPromptCwd(connID)
	scanPromptCwd(connID, []byte("[\x1b[34m/tmp\x1b[0m] # "))

	if got := callCwd(t, r, connID); got != "/tmp" {
		t.Fatalf("cwd 应优先用提示符里的 /tmp, got %q", got)
	}
}
