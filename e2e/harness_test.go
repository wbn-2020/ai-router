package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"ai-router/internal/config"
	"ai-router/internal/gateway"
	"ai-router/internal/observability"
	"ai-router/internal/routing"
	"ai-router/internal/secret"
	"ai-router/internal/server"
	"ai-router/internal/store"
	"ai-router/internal/upstream"
)

const (
	// testClientKey 是 harness 下发给 Codex 的测试客户端 Key（>=24 字符）。
	testClientKey = "e2e-client-key-0123456789abcdef"
	// testAdminPassword 只用于初始化临时库的管理员；数据面测试不使用管理端。
	testAdminPassword = "e2e-admin-password-123456"
	// fakeUpstreamAPIKey 是 harness 加密写入渠道的上游 Key。
	fakeUpstreamAPIKey = "fake-upstream-api-key"

	// codexRunTimeout 是单次真实 Codex 进程的上限（计划要求 120s）。
	codexRunTimeout = 120 * time.Second
	// codexVersionTimeout 是 codex --version 的上限。
	codexVersionTimeout = 10 * time.Second

	recorderBodyLimit = 1 << 20
)

// seedChannel 描述 harness 要写入 channels 表的一行。
type seedChannel struct {
	Name           string
	Protocol       string
	BaseURL        string
	Priority       int
	MaxConcurrency int
	Enabled        bool
}

// seedRoute 描述 harness 要写入 model_routes 表的一行。
type seedRoute struct {
	ClientModel   string
	UpstreamModel string
	ChannelName   string
	Priority      int
	Enabled       bool
}

// seedConfig 是 harness 的完整 seed 计划。
type seedConfig struct {
	channels []seedChannel
	routes   []seedRoute
}

// defaultSeed 生成 T27 默认 seed：两个启用的 openai_responses 渠道，四个
// 不在 Codex 内置目录中的自定义客户端模型。
//
// disconnect 模型额外映射到第二个渠道：如果网关在语义提交后错误地换渠道，
// 假上游会收到第二个请求，测试即可发现。
func defaultSeed(baseURL string) seedConfig {
	const (
		primaryChannel   = "fake-openai-responses"
		secondaryChannel = "fake-openai-responses-secondary"
	)
	return seedConfig{
		channels: []seedChannel{
			{
				Name:           primaryChannel,
				Protocol:       store.ChannelProtocolOpenAIResponses,
				BaseURL:        baseURL,
				Priority:       10,
				MaxConcurrency: 4,
				Enabled:        true,
			},
			{
				Name:           secondaryChannel,
				Protocol:       store.ChannelProtocolOpenAIResponses,
				BaseURL:        baseURL,
				Priority:       20,
				MaxConcurrency: 4,
				Enabled:        true,
			},
		},
		routes: []seedRoute{
			{ClientModel: modelText, UpstreamModel: modelText, ChannelName: primaryChannel, Enabled: true},
			{ClientModel: modelImage, UpstreamModel: modelImage, ChannelName: primaryChannel, Enabled: true},
			{ClientModel: modelTool, UpstreamModel: modelTool, ChannelName: primaryChannel, Enabled: true},
			{ClientModel: modelDisconnect, UpstreamModel: modelDisconnect, ChannelName: primaryChannel, Enabled: true},
			{ClientModel: modelDisconnect, UpstreamModel: modelDisconnect, ChannelName: secondaryChannel, Priority: 10, Enabled: true},
		},
	}
}

// preflight 在启动 Codex 之前检查 seed 至少有一个启用的 Responses 渠道。
//
// 没有原生 Responses 候选时继续运行只会得到 404 model_not_available，
// 无法证明 Codex 的 Responses 兼容性，因此直接失败。
func preflight(seed seedConfig) error {
	for _, channel := range seed.channels {
		if channel.Enabled && channel.Protocol == store.ChannelProtocolOpenAIResponses &&
			strings.TrimSpace(channel.BaseURL) != "" {
			return nil
		}
	}
	return fmt.Errorf(
		"e2e preflight：harness seed 中没有启用的 %s 渠道；T27 需要真实 Responses 候选",
		store.ChannelProtocolOpenAIResponses,
	)
}

// newTempDir 创建测试临时目录，并在清理时忽略删除失败。
//
// Codex 在 Windows 上可能短暂留下持有文件句柄的后代进程；t.TempDir 的
// RemoveAll 失败会让 E2E 因无关的清理问题失败。这里仍然尽力删除，但不把
// 删除失败当成协议验收失败。
func newTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ai-router-e2e-*")
	if err != nil {
		t.Fatalf("创建测试临时目录失败：%v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// harness 是一个已迁移、已 seed 且正在监听的临时网关。
type harness struct {
	baseURL string

	fake     *fakeUpstream
	recorder *gatewayRecorder
}

// newHarness 启动本地假上游、临时 SQLite、内存态调度器和网关 HTTP 服务。
func newHarness(t *testing.T) *harness {
	t.Helper()

	// 数据面 Go 客户端使用 ProxyFromEnvironment；显式把本机地址排除在代理之外。
	t.Setenv("NO_PROXY", "127.0.0.1,localhost,::1")
	t.Setenv("no_proxy", "127.0.0.1,localhost,::1")

	fake := newFakeUpstream(t)
	seed := defaultSeed(fake.url() + "/v1")
	if err := preflight(seed); err != nil {
		t.Fatalf("%v", err)
	}

	dataDir := newTempDir(t)
	masterKey := make([]byte, secret.MasterKeySize)
	if _, err := rand.Read(masterKey); err != nil {
		t.Fatalf("生成测试主密钥失败：%v", err)
	}

	env := map[string]string{
		config.EnvListenAddr:    "127.0.0.1:8080",
		config.EnvDataDir:       dataDir,
		config.EnvDBPath:        filepath.Join(dataDir, "gateway.db"),
		config.EnvMasterKey:     base64.StdEncoding.EncodeToString(masterKey),
		config.EnvClientKey:     testClientKey,
		config.EnvAdminPassword: testAdminPassword,
	}
	cfg, err := config.LoadFrom(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("加载测试配置失败：%v", err)
	}

	st, err := store.Open(store.Paths{DataDir: cfg.DataDir, DBPath: cfg.DBPath})
	if err != nil {
		t.Fatalf("打开临时数据库失败：%v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	migrateCtx, cancelMigrate := context.WithTimeout(context.Background(), 30*time.Second)
	_, err = st.Migrate(migrateCtx)
	cancelMigrate()
	if err != nil {
		t.Fatalf("迁移临时数据库失败：%v", err)
	}

	envelope, err := secret.NewEnvelope(cfg.MasterKey)
	if err != nil {
		t.Fatalf("初始化测试密钥信封失败：%v", err)
	}
	if err := seedStore(context.Background(), st, seed, envelope); err != nil {
		t.Fatalf("写入 harness seed 失败：%v", err)
	}

	client, err := upstream.NewClient(upstream.OptionsFromConfig(cfg))
	if err != nil {
		t.Fatalf("初始化测试上游客户端失败：%v", err)
	}
	routes := st.ModelRoutes()
	channels := st.Channels()
	scheduler, err := routing.New(routing.Options{
		Routes:       routes,
		Channels:     channels,
		LastUsed:     channels,
		CapacityWait: cfg.CapacityWait,
	})
	if err != nil {
		t.Fatalf("初始化测试调度器失败：%v", err)
	}
	journal := observability.New(st.DB())
	decryptKey := func(ciphertext string) (string, error) {
		return envelope.DecryptString(ciphertext, secret.PurposeChannelKey)
	}
	stream, err := gateway.NewStreamProxy(gateway.StreamProxyOptions{
		Scheduler:  scheduler,
		Upstream:   client,
		Journal:    journal,
		DecryptKey: decryptKey,
		Logger:     discardLogger(),
	})
	if err != nil {
		t.Fatalf("初始化测试流式代理失败：%v", err)
	}
	proxy, err := gateway.NewProxy(gateway.ProxyOptions{
		Scheduler:     scheduler,
		Upstream:      client,
		Journal:       journal,
		DecryptKey:    decryptKey,
		StreamHandler: stream,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("初始化测试代理失败：%v", err)
	}
	router, err := gateway.New(gateway.Options{
		ClientKey:          cfg.ClientKey,
		Routes:             routes,
		Forwarder:          proxy,
		CodexContextWindow: cfg.CodexContextWindow,
	})
	if err != nil {
		t.Fatalf("初始化测试数据面路由失败：%v", err)
	}
	observed, err := gateway.ObserveGateway(router, proxy, stream, journal)
	if err != nil {
		t.Fatalf("初始化测试观测包装失败：%v", err)
	}

	srv, err := server.New(server.Options{
		ListenAddr:      "127.0.0.1:0",
		Store:           st,
		MasterKeyLoaded: true,
		AdminPassword:   testAdminPassword,
		Logger:          discardLogger(),
	})
	if err != nil {
		t.Fatalf("初始化测试 HTTP 服务失败：%v", err)
	}
	recorder := newGatewayRecorder()
	srv.Handle("/v1/", recorder.wrap(observed))
	srv.Handle("/backend-api/codex/", recorder.wrap(observed))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听临时端口失败：%v", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
	t.Cleanup(func() {
		if err := srv.Shutdown(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Logf("关闭测试 HTTP 服务：%v", err)
		}
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Logf("测试 HTTP 服务退出：%v", err)
			}
		default:
		}
	})

	rootURL := "http://" + listener.Addr().String()
	waitHealthy(t, rootURL+"/healthz")

	return &harness{
		baseURL:  rootURL + "/v1",
		fake:     fake,
		recorder: recorder,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func seedStore(ctx context.Context, st *store.Store, seed seedConfig, envelope *secret.Envelope) error {
	ciphertext, err := envelope.EncryptString(fakeUpstreamAPIKey, secret.PurposeChannelKey)
	if err != nil {
		return fmt.Errorf("加密假上游 Key 失败：%w", err)
	}

	channelIDs := make(map[string]int64, len(seed.channels))
	for _, channel := range seed.channels {
		created, err := st.Channels().Create(ctx, store.ChannelInput{
			ChannelConfig: store.ChannelConfig{
				Name:           channel.Name,
				BaseURL:        channel.BaseURL,
				Protocol:       channel.Protocol,
				Priority:       channel.Priority,
				MaxConcurrency: channel.MaxConcurrency,
				Enabled:        channel.Enabled,
			},
			KeyCiphertext: ciphertext,
			KeyMask:       "sk-...fake",
		})
		if err != nil {
			return fmt.Errorf("创建 seed 渠道 %q 失败：%w", channel.Name, err)
		}
		channelIDs[channel.Name] = created.ID
	}

	for _, route := range seed.routes {
		channelID, ok := channelIDs[route.ChannelName]
		if !ok {
			return fmt.Errorf("seed 映射 %q 引用了未知渠道 %q", route.ClientModel, route.ChannelName)
		}
		if _, err := st.ModelRoutes().Create(ctx, store.ModelRouteInput{
			ClientModel:   route.ClientModel,
			ChannelID:     channelID,
			UpstreamModel: route.UpstreamModel,
			Priority:      route.Priority,
			Enabled:       route.Enabled,
		}); err != nil {
			return fmt.Errorf("创建 seed 映射 %q 失败：%w", route.ClientModel, err)
		}
	}
	return nil
}

func waitHealthy(t *testing.T, healthURL string) {
	t.Helper()
	client := newLocalHTTPClient()
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		response, err := client.Get(healthURL)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("healthz 返回 %d", response.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("等待测试网关就绪超时：%v", lastErr)
}

func newLocalHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
}

// recordedRequest 是网关侧记录的一次入站请求。
type recordedRequest struct {
	Method         string
	Path           string
	RawQuery       string
	Headers        http.Header
	ResponseHeader http.Header
	Status         int
	Body           []byte
	Truncated      bool
}

// gatewayRecorder 包装数据面 Handler，记录网关实际收到的 method/path/header
// 与写出的响应字节。
type gatewayRecorder struct {
	mu    sync.Mutex
	items []recordedRequest
}

func newGatewayRecorder() *gatewayRecorder {
	return &gatewayRecorder{}
}

func (g *gatewayRecorder) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recordingResponseWriter{ResponseWriter: w}
		defer func() {
			g.mu.Lock()
			g.items = append(g.items, recordedRequest{
				Method:         r.Method,
				Path:           r.URL.Path,
				RawQuery:       r.URL.RawQuery,
				Headers:        r.Header.Clone(),
				ResponseHeader: rec.ResponseWriter.Header().Clone(),
				Status:         rec.statusCode(),
				Body:           append([]byte(nil), rec.body.Bytes()...),
				Truncated:      rec.truncated,
			})
			g.mu.Unlock()
		}()
		next.ServeHTTP(rec, r)
	})
}

func (g *gatewayRecorder) reset() {
	g.mu.Lock()
	g.items = nil
	g.mu.Unlock()
}

func (g *gatewayRecorder) snapshot() []recordedRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]recordedRequest, len(g.items))
	copy(out, g.items)
	return out
}

type recordingResponseWriter struct {
	http.ResponseWriter

	status    int
	body      bytes.Buffer
	truncated bool
}

func (w *recordingResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *recordingResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *recordingResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if remaining := recorderBodyLimit - w.body.Len(); remaining > 0 {
		if len(data) <= remaining {
			_, _ = w.body.Write(data)
		} else {
			_, _ = w.body.Write(data[:remaining])
			w.truncated = true
		}
	} else if len(data) > 0 {
		w.truncated = true
	}
	return w.ResponseWriter.Write(data)
}

func (w *recordingResponseWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *recordingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// codexConfigData 是 config.toml.tmpl 的渲染数据。
type codexConfigData struct {
	Model           string
	BaseURL         string
	ModelCatalogURL string
	ClientKey       string
}

// writeCodexHome 渲染临时 CODEX_HOME/config.toml，并断言没有 model_catalog_json。
func writeCodexHome(t *testing.T, baseURL, clientKey, defaultModel string) string {
	t.Helper()

	templatePath := filepath.Join("testdata", "codex-0.164.0", "config.toml.tmpl")
	raw, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("读取 Codex 配置模板失败：%v", err)
	}
	tmpl, err := template.New("codex-config").Parse(string(raw))
	if err != nil {
		t.Fatalf("解析 Codex 配置模板失败：%v", err)
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, codexConfigData{
		Model:           defaultModel,
		BaseURL:         baseURL,
		ModelCatalogURL: strings.TrimRight(baseURL, "/") + "/models",
		ClientKey:       clientKey,
	}); err != nil {
		t.Fatalf("渲染 Codex 配置模板失败：%v", err)
	}
	configText := rendered.String()
	if strings.Contains(configText, "model_catalog_json") {
		t.Fatalf("Codex 配置模板不得设置 model_catalog_json，否则会绕过 /v1/models 模型发现")
	}
	for _, required := range []string{
		`wire_api = "responses"`,
		"model_catalog_url",
		"supports_websockets = false",
		"requires_openai_auth = false",
		"experimental_bearer_token",
	} {
		if !strings.Contains(configText, required) {
			t.Fatalf("Codex 配置模板缺少 %q", required)
		}
	}

	home := newTempDir(t)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), rendered.Bytes(), 0o600); err != nil {
		t.Fatalf("写入临时 CODEX_HOME 失败：%v", err)
	}
	return home
}

// codexResult 是一次真实 Codex 进程的结果。
type codexResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
}

// runCodex 用临时 CODEX_HOME 运行真实 codex exec，上限 120s。
func runCodex(t *testing.T, codexPath, codexHome, workDir, model, prompt string, images []string) codexResult {
	t.Helper()

	args := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		"--ephemeral",
		// 插件同步与 Responses 兼容性无关，且会启动后台进程占用 cwd 临时文件；
		// 关闭它以稳定 E2E 并缩短启动时间。
		"--disable", "plugins",
		"-C", workDir,
		"--sandbox", "read-only",
		"-m", model,
	}
	// codex exec 的 -i 是可变参数，提示词必须放在 -i 之前，否则会被吞掉。
	if len(images) > 0 {
		args = append(args, prompt)
		for _, image := range images {
			args = append(args, "-i", image)
		}
	} else {
		args = append(args, prompt)
	}

	ctx, cancel := context.WithTimeout(context.Background(), codexRunTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, codexPath, args...)
	cmd.Dir = workDir
	cmd.Env = codexEnv(codexHome)
	// Windows 上 codex.cmd 会派生 node 子进程；用临时文件而不是管道承接
	// stdout/stderr，避免后代进程继承管道导致 Wait 永久阻塞。
	cmd.WaitDelay = 15 * time.Second

	logDir := newTempDir(t)
	stdoutFile, err := os.CreateTemp(logDir, "codex-stdout-*.log")
	if err != nil {
		t.Fatalf("创建 Codex stdout 临时文件失败：%v", err)
	}
	stderrFile, err := os.CreateTemp(logDir, "codex-stderr-*.log")
	if err != nil {
		_ = stdoutFile.Close()
		t.Fatalf("创建 Codex stderr 临时文件失败：%v", err)
	}
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	runErr := cmd.Run()
	_ = stdoutFile.Close()
	_ = stderrFile.Close()

	stdout, readErr := os.ReadFile(stdoutFile.Name())
	if readErr != nil {
		t.Fatalf("读取 Codex stdout 失败：%v", readErr)
	}
	stderr, readErr := os.ReadFile(stderrFile.Name())
	if readErr != nil {
		t.Fatalf("读取 Codex stderr 失败：%v", readErr)
	}

	result := codexResult{
		Stdout: string(stdout),
		Stderr: string(stderr),
	}
	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
	}
	if runErr == nil || errors.Is(runErr, exec.ErrWaitDelay) {
		result.ExitCode = 0
	} else {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}
	}
	return result
}

// codexEnv 隔离用户级 Codex/OpenAI 配置，并强制本机地址不使用代理。
func codexEnv(codexHome string) []string {
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		switch {
		case upper == "CODEX_HOME":
			continue
		case strings.HasPrefix(upper, "CODEX_"):
			continue
		case strings.HasPrefix(upper, "OPENAI_"):
			continue
		case upper == "NO_PROXY":
			continue
		}
		env = append(env, entry)
	}
	env = append(env,
		"CODEX_HOME="+codexHome,
		"NO_COLOR=1",
		"NO_PROXY=127.0.0.1,localhost,::1",
		"no_proxy=127.0.0.1,localhost,::1",
	)
	return env
}

// codexEvent 是 codex exec --json 输出的一行事件。
type codexEvent struct {
	Type  string          `json:"type"`
	Item  *codexEventItem `json:"item"`
	Error *codexEventItem `json:"error"`
}

type codexEventItem struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Message string `json:"message"`
}

func parseCodexEvents(stdout string) []codexEvent {
	events := make([]codexEvent, 0)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event codexEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events
}

func agentMessageTexts(events []codexEvent) []string {
	texts := make([]string, 0)
	for _, event := range events {
		if event.Type == "item.completed" && event.Item != nil && event.Item.Type == "agent_message" {
			texts = append(texts, event.Item.Text)
		}
	}
	return texts
}

func hasCodexEventType(events []codexEvent, kind string) bool {
	for _, event := range events {
		if event.Type == kind {
			return true
		}
	}
	return false
}

func requireAgentMessage(t *testing.T, result codexResult, want string) {
	t.Helper()
	if result.TimedOut {
		t.Fatalf("codex exec 超过 %s 未结束\nstdout:\n%s\nstderr:\n%s", codexRunTimeout, result.Stdout, result.Stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("codex exec 退出码 %d，期望 0\nstdout:\n%s\nstderr:\n%s",
			result.ExitCode, result.Stdout, result.Stderr)
	}
	events := parseCodexEvents(result.Stdout)
	for _, text := range agentMessageTexts(events) {
		if strings.Contains(text, want) {
			return
		}
	}
	t.Fatalf("codex 输出中没有包含 %q 的 agent_message\nagent_messages=%q\nstdout:\n%s\nstderr:\n%s",
		want, agentMessageTexts(events), result.Stdout, result.Stderr)
}

// requireManifestAccepted 确认 Codex 从 /v1/models 拿到了可用的完整 manifest，
// 而不是退回内置 fallback metadata。
func requireManifestAccepted(t *testing.T, result codexResult) {
	t.Helper()
	for _, event := range parseCodexEvents(result.Stdout) {
		if event.Item != nil && event.Item.Type == "error" &&
			strings.Contains(event.Item.Message, "Model metadata") {
			t.Fatalf("Codex 未接受完整 manifest，退回了 fallback metadata：%s", event.Item.Message)
		}
	}
}

var codexVersionPattern = regexp.MustCompile(`(?i)\bcodex-cli\s+(\d+)\.(\d+)\.(\d+)`)

// requireCodexVersion 要求真实 Codex 为 0.164.x；不匹配或无法解析时失败。
func requireCodexVersion(output string) error {
	match := codexVersionPattern.FindStringSubmatch(output)
	if match == nil {
		return fmt.Errorf("无法从 codex --version 输出解析版本：%q", output)
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	if major != 0 || minor != 164 {
		return fmt.Errorf("Codex 版本不匹配：得到 %s，要求 0.164.x（必须失败而不是 skip）", match[0])
	}
	return nil
}

func codexVersion(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("T27 需要真实 codex CLI 0.164.x，但未找到 codex 可执行文件：%v（必须失败而不是 skip）", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexVersionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("运行 codex --version 失败：%v；输出：%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func codexExecutable(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("T27 需要真实 codex CLI 0.164.x，但未找到 codex 可执行文件：%v（必须失败而不是 skip）", err)
	}
	return path
}
