package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ai-router/internal/config"
	"ai-router/internal/server"
	"ai-router/internal/store"
	"ai-router/internal/webassets"
)

// Vite 生产构建会给入口脚本和样式表加内容哈希；T05 占位页两个都不会有。
var (
	viteEntryScriptPattern = regexp.MustCompile(`<script[^>]*\bsrc="(/assets/index-[A-Za-z0-9_-]+\.js)"`)
	viteStylesheetPattern  = regexp.MustCompile(`<link[^>]*\bhref="(/assets/index-[A-Za-z0-9_-]+\.css)"`)
)

// TestEmbeddedSPAIsProductionBuild 断言嵌入的 index.html 来自真实 Vite 构建，
// 而不是 T05 的静态占位页；并通过 HTTP 服务验证入口资源和 SPA fallback 可用。
func TestEmbeddedSPAIsProductionBuild(t *testing.T) {
	index, err := webassets.Index()
	if err != nil {
		t.Fatalf("webassets.Index: %v", err)
	}
	if !bytes.Contains(index, []byte(`id="root"`)) {
		t.Fatalf("嵌入的 index.html 缺少 #root 挂载点，可能不是管理端构建产物：%q", index)
	}

	script := viteEntryScriptPattern.FindSubmatch(index)
	if len(script) != 2 {
		t.Fatalf(
			"internal/webassets/dist/index.html 不是 npm run build 的产物：未找到 /assets/index-<hash>.js 引用。"+
				"当前内容可能是 T05 静态占位页；请先在 web 目录执行 npm run build（或 npm --prefix web run build），"+
				"再重新运行测试。当前 index.html：\n%s",
			index,
		)
	}
	scriptPath := string(script[1])

	stylesheet := viteStylesheetPattern.FindSubmatch(index)
	if len(stylesheet) != 2 {
		t.Fatalf(
			"internal/webassets/dist/index.html 缺少 Vite 生成的 /assets/index-<hash>.css 引用；"+
				"请先在 web 目录执行 npm run build。当前 index.html：\n%s",
			index,
		)
	}
	stylesheetPath := string(stylesheet[1])

	dist, err := webassets.Dist()
	if err != nil {
		t.Fatalf("webassets.Dist: %v", err)
	}
	scriptAsset, err := fs.ReadFile(dist, strings.TrimPrefix(scriptPath, "/"))
	if err != nil {
		t.Fatalf("index.html 引用的脚本 %s 不在嵌入产物中：%v（请重新执行 npm run build）", scriptPath, err)
	}
	if len(scriptAsset) < 1024 {
		t.Fatalf("嵌入的入口脚本 %s 只有 %d 字节，不像是真实构建产物；请重新执行 npm run build", scriptPath, len(scriptAsset))
	}
	stylesheetAsset, err := fs.ReadFile(dist, strings.TrimPrefix(stylesheetPath, "/"))
	if err != nil {
		t.Fatalf("index.html 引用的样式表 %s 不在嵌入产物中：%v（请重新执行 npm run build）", stylesheetPath, err)
	}

	s := newTestServer(t, server.Options{})
	handler := s.Handler()

	page := serveRequest(handler, http.MethodGet, "/admin/channels/42", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("GET /admin/channels/42 = %d, want 200（body=%q）", page.Code, page.Body.String())
	}
	if !bytes.Equal(page.Body.Bytes(), index) {
		t.Error("SPA fallback 返回的 HTML 与嵌入的 index.html 不一致")
	}

	for _, target := range []string{scriptPath, "/admin" + scriptPath} {
		rec := serveRequest(handler, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", target, rec.Code)
		}
		if !bytes.Equal(rec.Body.Bytes(), scriptAsset) {
			t.Errorf("GET %s 返回内容与嵌入的入口脚本不一致", target)
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
			t.Errorf("GET %s Content-Type = %q, want javascript", target, got)
		}
	}

	css := serveRequest(handler, http.MethodGet, stylesheetPath, nil)
	if css.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", stylesheetPath, css.Code)
	}
	if !bytes.Equal(css.Body.Bytes(), stylesheetAsset) {
		t.Errorf("GET %s 返回内容与嵌入的样式表不一致", stylesheetPath)
	}
	if got := css.Header().Get("Content-Type"); !strings.Contains(got, "text/css") {
		t.Errorf("GET %s Content-Type = %q, want text/css", stylesheetPath, got)
	}
}

// TestGatewayDataDirRelocatesDatabaseAndBackups 验证 GATEWAY_DATA_DIR 是唯一状态
// 根目录：数据库、迁移前备份和手动备份都落在该目录内，而不是进程 cwd。
func TestGatewayDataDirRelocatesDatabaseAndBackups(t *testing.T) {
	dataDir := t.TempDir()
	cfg := loadDeploymentTestConfig(t, map[string]string{
		config.EnvDataDir: dataDir,
	})

	wantDBPath := filepath.Join(dataDir, "gateway.db")
	if cfg.DataDir != dataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, dataDir)
	}
	if cfg.DBPath != wantDBPath {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, wantDBPath)
	}

	paths := store.Paths{DataDir: cfg.DataDir, DBPath: cfg.DBPath}
	st, err := store.Open(paths)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	result, err := st.Migrate(context.Background())
	if err != nil {
		_ = st.Close()
		t.Fatalf("store.Migrate: %v", err)
	}
	if result.BackupPath == "" {
		_ = st.Close()
		t.Fatal("首次迁移没有生成迁移前备份")
	}
	requirePathUnder(t, dataDir, cfg.DBPath)
	requirePathUnder(t, dataDir, result.BackupPath)
	if got, want := filepath.Dir(result.BackupPath), filepath.Join(dataDir, "backups"); got != want {
		t.Errorf("迁移前备份目录 = %q, want %q", got, want)
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		_ = st.Close()
		t.Errorf("数据库未落在 GATEWAY_DATA_DIR：%v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("关闭数据库: %v", err)
	}

	backupPath, err := store.Backup(context.Background(), paths, "")
	if err != nil {
		t.Fatalf("store.Backup: %v", err)
	}
	requirePathUnder(t, dataDir, backupPath)
	if got, want := filepath.Dir(backupPath), filepath.Join(dataDir, "backups"); got != want {
		t.Errorf("默认备份目录 = %q, want %q", got, want)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Errorf("备份文件不存在：%v", err)
	}
}

// TestListenAddressConfigurable 覆盖本机回环默认值与容器 0.0.0.0 覆盖值。
func TestListenAddressConfigurable(t *testing.T) {
	defaults := loadDeploymentTestConfig(t, nil)
	if defaults.ListenAddr != config.DefaultListenAddr {
		t.Fatalf("默认 ListenAddr = %q, want %q", defaults.ListenAddr, config.DefaultListenAddr)
	}
	local := newTestServer(t, server.Options{ListenAddr: defaults.ListenAddr})
	t.Cleanup(func() {
		_ = local.Shutdown()
	})
	if got := local.Addr(); got != "127.0.0.1:8080" {
		t.Errorf("默认监听地址 = %q, want 127.0.0.1:8080", got)
	}

	container := loadDeploymentTestConfig(t, map[string]string{
		config.EnvListenAddr: "0.0.0.0:8080",
	})
	if container.ListenAddr != "0.0.0.0:8080" {
		t.Errorf("容器 ListenAddr = %q, want 0.0.0.0:8080", container.ListenAddr)
	}
	allInterfaces := newTestServer(t, server.Options{ListenAddr: container.ListenAddr})
	t.Cleanup(func() {
		_ = allInterfaces.Shutdown()
	})
	if got := allInterfaces.Addr(); got != "0.0.0.0:8080" {
		t.Errorf("容器监听地址 = %q, want 0.0.0.0:8080", got)
	}
}

func loadDeploymentTestConfig(t *testing.T, overrides map[string]string) config.Config {
	t.Helper()
	env := map[string]string{
		config.EnvMasterKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32)),
		config.EnvClientKey: "gw_test_client_key_0123456789abcdef",
	}
	for name, value := range overrides {
		env[name] = value
	}
	cfg, err := config.LoadFrom(func(name string) string {
		return env[name]
	})
	if err != nil {
		t.Fatalf("config.LoadFrom: %v", err)
	}
	return cfg
}

func requirePathUnder(t *testing.T, root, path string) {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", root, path, err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		t.Fatalf("路径 %q 不在数据目录 %q 下（rel=%q）", path, root, rel)
	}
}
