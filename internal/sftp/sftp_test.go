package sftp

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"remote-connect-manager/internal/testutil"
)

func newRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/sftp/:id/list", List)
	r.GET("/api/sftp/:id/download", Download)
	r.POST("/api/sftp/:id/upload", Upload)
	r.POST("/api/sftp/:id/mkdir", Mkdir)
	r.POST("/api/sftp/:id/remove", Remove)
	return r
}

// setup 启动测试 SSH 服务端并返回路由、连接 ID 与远端根目录
func setup(t *testing.T) (*gin.Engine, string, string) {
	t.Helper()
	srv := testutil.StartSSH(t, testutil.ServeSFTP(t))
	id := testutil.RegisterConnection(t, srv)
	return newRouter(), id, t.TempDir()
}

func doJSON(t *testing.T, r *gin.Engine, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestListSortsDirsFirst(t *testing.T) {
	r, id, root := setup(t)

	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "zdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, r, http.MethodGet, "/api/sftp/"+id+"/list?path="+url.QueryEscape(root), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Path    string      `json:"path"`
		Entries []fileEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path != root {
		t.Fatalf("path = %q, want %q", resp.Path, root)
	}
	if len(resp.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(resp.Entries))
	}
	want := []string{"zdir", "a.txt", "b.txt"}
	for i, name := range want {
		if resp.Entries[i].Name != name {
			t.Fatalf("entries[%d] = %q, want %q", i, resp.Entries[i].Name, name)
		}
	}
	if !resp.Entries[0].Dir {
		t.Errorf("zdir 应标记为目录")
	}
	if resp.Entries[1].Dir {
		t.Errorf("a.txt 不应标记为目录")
	}
	if resp.Entries[1].Path != filepath.Join(root, "a.txt") {
		t.Errorf("path = %q", resp.Entries[1].Path)
	}
}

func TestListErrors(t *testing.T) {
	r, id, root := setup(t)

	if rec := doJSON(t, r, http.MethodGet, "/api/sftp/"+id+"/list?path="+url.QueryEscape(filepath.Join(root, "nope")), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("不存在的目录 status = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, r, http.MethodGet, "/api/sftp/unknown/list", ""); rec.Code != http.StatusNotFound {
		t.Errorf("未知连接 status = %d, want 404", rec.Code)
	}
}

func TestDownload(t *testing.T) {
	r, id, root := setup(t)
	content := []byte("hello sftp\n")
	file := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, r, http.MethodGet, "/api/sftp/"+id+"/download?path="+url.QueryEscape(file), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(rec.Body.Bytes(), content) {
		t.Fatalf("body = %q, want %q", rec.Body.String(), content)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "hello.txt") {
		t.Errorf("Content-Disposition = %q", cd)
	}

	// Range 请求应返回 206，供大文件断点续传
	req := httptest.NewRequest(http.MethodGet, "/api/sftp/"+id+"/download?path="+url.QueryEscape(file), nil)
	req.Header.Set("Range", "bytes=0-4")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusPartialContent {
		t.Fatalf("Range status = %d, want 206", rr.Code)
	}
	if rr.Body.String() != "hello" {
		t.Fatalf("Range body = %q, want %q", rr.Body.String(), "hello")
	}
}

func TestDownloadErrors(t *testing.T) {
	r, id, root := setup(t)

	if rec := doJSON(t, r, http.MethodGet, "/api/sftp/"+id+"/download", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("缺少 path status = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, r, http.MethodGet, "/api/sftp/"+id+"/download?path="+url.QueryEscape(filepath.Join(root, "missing.txt")), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("文件不存在 status = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, r, http.MethodGet, "/api/sftp/"+id+"/download?path="+url.QueryEscape(root), ""); rec.Code != http.StatusBadRequest {
		t.Errorf("下载目录 status = %d, want 400", rec.Code)
	}
}

func TestUpload(t *testing.T) {
	r, id, root := setup(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("path", root); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", "up.txt")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("uploaded by test")
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sftp/"+id+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, err := os.ReadFile(filepath.Join(root, "up.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("远端文件内容 = %q, want %q", got, payload)
	}
}

// 拖拽/粘贴上传文件夹时，rel 带子目录，后端应自动补齐父目录
func TestUploadWithRelCreatesParents(t *testing.T) {
	r, id, root := setup(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("path", root)
	_ = mw.WriteField("rel", "docs/sub/a.txt")
	fw, err := mw.CreateFormFile("file", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("nested upload")
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/sftp/"+id+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, err := os.ReadFile(filepath.Join(root, "docs", "sub", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("内容 = %q, want %q", got, payload)
	}
}

func TestUploadMissingFile(t *testing.T) {
	r, id, _ := setup(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("path", "/tmp")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/sftp/"+id+"/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestMkdirAndRemove(t *testing.T) {
	r, id, root := setup(t)

	dir := filepath.Join(root, "deep", "nested")
	rec := doJSON(t, r, http.MethodPost, "/api/sftp/"+id+"/mkdir", `{"path":`+quote(dir)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mkdir status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("目录未创建: %v", err)
	}

	file := filepath.Join(root, "to-delete.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, r, http.MethodPost, "/api/sftp/"+id+"/remove", `{"path":`+quote(file)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("remove file status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("文件应已删除, err = %v", err)
	}

	if rec := doJSON(t, r, http.MethodPost, "/api/sftp/"+id+"/remove", `{"path":`+quote(dir)+`,"dir":true}`); rec.Code != http.StatusOK {
		t.Fatalf("remove dir status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("目录应已删除, err = %v", err)
	}

	// 删除不存在的路径应幂等返回 200
	if rec := doJSON(t, r, http.MethodPost, "/api/sftp/"+id+"/remove", `{"path":`+quote(filepath.Join(root, "ghost"))+`}`); rec.Code != http.StatusOK {
		t.Fatalf("删除不存在文件 status = %d, want 200", rec.Code)
	}
}

func TestRemoveBatch(t *testing.T) {
	r, id, root := setup(t)

	f1 := filepath.Join(root, "batch-1.txt")
	f2 := filepath.Join(root, "batch-2.txt")
	dir := filepath.Join(root, "batch-dir")
	for _, f := range []string{f1, f2} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	body := `{"paths":[{"path":` + quote(f1) + `},{"path":` + quote(f2) + `},{"path":` + quote(dir) + `,"dir":true}]}`
	rec := doJSON(t, r, http.MethodPost, "/api/sftp/"+id+"/remove", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch remove status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Removed []string `json:"removed"`
		Failed  []struct {
			Path string `json:"path"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Removed) != 3 || len(resp.Failed) != 0 {
		t.Fatalf("removed = %v, failed = %+v", resp.Removed, resp.Failed)
	}
	for _, p := range []string{f1, f2, dir} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s 应已删除, err = %v", p, err)
		}
	}
}

func TestRemoveBatchPartialFailure(t *testing.T) {
	r, id, root := setup(t)

	f := filepath.Join(root, "keep-me.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 非空目录按“文件”方式删除会失败（SFTP REMOVE 拒绝非空目录），用来触发失败分支
	ghost := filepath.Join(root, "not-empty")
	if err := os.MkdirAll(ghost, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghost, "child.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"paths":[{"path":` + quote(f) + `},{"path":` + quote(ghost) + `}]}`
	rec := doJSON(t, r, http.MethodPost, "/api/sftp/"+id+"/remove", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Removed []string `json:"removed"`
		Failed  []struct {
			Path  string `json:"path"`
			Error string `json:"error"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Removed) != 1 || resp.Removed[0] != f {
		t.Fatalf("removed = %v, want [%s]", resp.Removed, f)
	}
	if len(resp.Failed) != 1 || resp.Failed[0].Path != ghost || resp.Failed[0].Error == "" {
		t.Fatalf("failed = %+v, want path %s 且带错误原因", resp.Failed, ghost)
	}
}

func TestMkdirRequiresPath(t *testing.T) {
	r, id, _ := setup(t)
	if rec := doJSON(t, r, http.MethodPost, "/api/sftp/"+id+"/mkdir", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
