package ssh

import "testing"

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
	if got := feed(t, tr, "cd -\r"); got != "/srv" {
		t.Fatalf("cd - 无法推断，应保持不变, got %q", got)
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
