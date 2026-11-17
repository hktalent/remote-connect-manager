package rdpdrive

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// driveDir 返回 guacd 共享盘在本机的落地目录。
// guacd 通过 RDP 参数 drive-path 把远端写入的文件放在这里，
// Docker 部署时两个容器挂载同一个宿主机目录。
func driveDir() string {
	if v := os.Getenv("RCM_DRIVE_PATH"); v != "" {
		return v
	}
	return "/tmp/rcm-drive"
}

// prepare 确保共享盘目录存在且 guacd 可写（容器内 guacd 用户与宿主机 UID 不同）
func prepare() (string, error) {
	dir := driveDir()
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	_ = os.Chmod(dir, 0o777)
	return dir, nil
}

// Ensure 启动时调用：创建共享盘目录并放开权限，保证 guacd 用户可写
func Ensure() error {
	_, err := prepare()
	return err
}

type entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Dir     bool   `json:"dir"`
	ModTime string `json:"mod_time"`
}

// resolve 把前端传入的相对路径限制在共享盘目录内，防止目录穿越
func resolve(rel string) (string, error) {
	dir, err := prepare()
	if err != nil {
		return "", err
	}
	clean := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	full := filepath.Join(dir, filepath.FromSlash(clean))
	if full != dir && !strings.HasPrefix(full, dir+string(os.PathSeparator)) {
		return "", os.ErrPermission
	}
	return full, nil
}

// List 列出共享盘目录内容（相对路径，根目录用 . 或空）
func List(c *gin.Context) {
	if _, err := prepare(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	rel := c.Query("path")
	full, err := resolve(rel)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法路径"})
		return
	}
	items, err := os.ReadDir(full)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	entries := make([]entry, 0, len(items))
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue
		}
		relPath := path.Join("/"+path.Clean("/"+rel), it.Name())
		entries = append(entries, entry{
			Name:    it.Name(),
			Path:    strings.TrimPrefix(relPath, "/"),
			Size:    info.Size(),
			Dir:     it.IsDir(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	shown := path.Join("/", path.Clean("/"+rel))
	c.JSON(http.StatusOK, gin.H{"path": shown, "entries": entries})
}

// Download 下载共享盘中的文件，支持 Range 断点续传
func Download(c *gin.Context) {
	full, err := resolve(c.Query("path"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "非法路径"})
		return
	}
	f, err := os.Open(full)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不是文件"})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+info.Name()+`"`)
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), f)
}
