package server_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// dockerStage 是 Dockerfile 中一个构建阶段的静态视图。
type dockerStage struct {
	name  string
	image string
	body  string
}

// TestDockerfilePackagingContract 静态检查镜像契约：多阶段构建、非 root 运行、
// /data 数据卷、容器内监听地址、端口与秘密隔离。
//
// 本测试不执行 docker build；真实构建与容器运行是发布前的人工验收项。
func TestDockerfilePackagingContract(t *testing.T) {
	root := deploymentRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("读取 Dockerfile 失败：%v", err)
	}
	dockerfile := strings.ReplaceAll(string(raw), "\r\n", "\n")

	stages := parseDockerStages(dockerfile)
	if len(stages) < 3 {
		t.Fatalf("Dockerfile 阶段数 = %d，want >= 3（web、go build、runtime）", len(stages))
	}

	webStage := findDockerStage(t, stages, "web")
	if !strings.Contains(webStage.body, "web/package-lock.json") {
		t.Error("web 阶段必须在构建前复制 web/package-lock.json")
	}
	if !strings.Contains(webStage.body, "npm --prefix web ci") {
		t.Errorf("web 阶段必须用 npm ci 安装锁定依赖，实际内容：\n%s", webStage.body)
	}
	if !strings.Contains(webStage.body, "npm --prefix web run build") {
		t.Errorf("web 阶段必须执行 npm run build，实际内容：\n%s", webStage.body)
	}

	buildStage := findDockerStage(t, stages, "build")
	if !strings.Contains(buildStage.body, "CGO_ENABLED=0") {
		t.Error("Go 构建阶段必须设置 CGO_ENABLED=0")
	}
	if !strings.Contains(buildStage.body, "go build") {
		t.Errorf("Go 构建阶段必须执行 go build，实际内容：\n%s", buildStage.body)
	}
	if !regexp.MustCompile(`-o\s+/out/gateway`).MatchString(buildStage.body) {
		t.Error("Go 构建输出必须为 /out/gateway")
	}
	if !strings.Contains(buildStage.body, "./cmd/gateway") {
		t.Error("Go 构建目标必须为 ./cmd/gateway")
	}

	copyDist := strings.Index(buildStage.body, "COPY --from=web /src/internal/webassets/dist ./internal/webassets/dist")
	if copyDist < 0 {
		t.Error("Go 构建阶段必须把 web 阶段生成的 internal/webassets/dist 复制进来")
	}
	goBuild := strings.Index(buildStage.body, "go build")
	if copyDist >= 0 && goBuild >= 0 && copyDist > goBuild {
		t.Error("必须在 go build 之前复制 web 构建产物，否则嵌入的是占位页或空目录")
	}

	final := stages[len(stages)-1]
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(final.image)), "scratch") {
		t.Errorf("运行阶段使用 scratch，无法包含 ca-certificates 与可写临时目录：%q", final.image)
	}
	if !strings.Contains(strings.ToLower(final.body), "ca-certificates") {
		t.Error("运行阶段必须安装 ca-certificates")
	}

	user := dockerDirectiveValue(final, "USER")
	if user == "" {
		t.Fatal("运行阶段缺少 USER，默认会以 root 运行")
	}
	uid := strings.SplitN(user, ":", 2)[0]
	if strings.EqualFold(uid, "root") || uid == "0" {
		t.Errorf("运行阶段 USER = %q，必须为非 root 用户", user)
	}

	volume := dockerDirectiveValue(final, "VOLUME")
	if !strings.Contains(volume, "/data") {
		t.Errorf("运行阶段 VOLUME = %q，必须包含 /data", volume)
	}

	envs := dockerEnvValues(final)
	if got := envs["GATEWAY_DATA_DIR"]; got != "/data" {
		t.Errorf("ENV GATEWAY_DATA_DIR = %q，want /data", got)
	}
	if got := envs["GATEWAY_LISTEN_ADDR"]; got != "0.0.0.0:8080" {
		t.Errorf("ENV GATEWAY_LISTEN_ADDR = %q，want 0.0.0.0:8080", got)
	}

	expose := dockerDirectiveValue(final, "EXPOSE")
	if !regexp.MustCompile(`\b8080\b`).MatchString(expose) {
		t.Errorf("EXPOSE = %q，必须包含 8080", expose)
	}

	entrypoint := dockerDirectiveValue(final, "ENTRYPOINT")
	if !strings.Contains(entrypoint, "/usr/local/bin/gateway") {
		t.Errorf("ENTRYPOINT = %q，必须指向 /usr/local/bin/gateway", entrypoint)
	}
	if cmd := dockerDirectiveValue(final, "CMD"); !strings.Contains(cmd, "serve") {
		t.Errorf("CMD = %q，默认子命令必须为 serve", cmd)
	}

	for _, name := range []string{
		"GATEWAY_MASTER_KEY",
		"GATEWAY_CLIENT_KEY",
		"GATEWAY_ADMIN_PASSWORD",
	} {
		if strings.Contains(dockerfile, name) {
			t.Errorf("Dockerfile 不得包含秘密变量名 %s；秘密只能运行时注入", name)
		}
	}
	if regexp.MustCompile(`(?im)^\s*(?:ENV|ARG)\s+[A-Za-z0-9_]*(?:PASSWORD|SECRET|MASTER_KEY|CLIENT_KEY)\s*=`).MatchString(dockerfile) {
		t.Error("Dockerfile 不得通过 ENV/ARG 写入密码或密钥")
	}

	ignoreRaw, err := os.ReadFile(filepath.Join(root, ".dockerignore"))
	if err != nil {
		t.Fatalf("读取 .dockerignore 失败：%v", err)
	}
	ignored := strings.ReplaceAll(string(ignoreRaw), "\r\n", "\n")
	for _, entry := range []string{"internal/webassets/dist", "web/node_modules"} {
		if !dockerignoreContains(ignored, entry) {
			t.Errorf(".dockerignore 必须排除 %s，避免本机残留产物或依赖进入构建上下文", entry)
		}
	}
}

func deploymentRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件路径")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func parseDockerStages(dockerfile string) []dockerStage {
	var stages []dockerStage
	for _, line := range strings.Split(dockerfile, "\n") {
		trimmed := strings.TrimSpace(line)
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "FROM") {
			stage := dockerStage{}
			for i := 1; i < len(fields); i++ {
				if strings.HasPrefix(fields[i], "--") {
					continue
				}
				if strings.EqualFold(fields[i], "AS") && i+1 < len(fields) {
					stage.name = fields[i+1]
					break
				}
				if stage.image == "" {
					stage.image = fields[i]
				}
			}
			stages = append(stages, stage)
			continue
		}
		if len(stages) > 0 {
			stages[len(stages)-1].body += line + "\n"
		}
	}
	return stages
}

func findDockerStage(t *testing.T, stages []dockerStage, name string) dockerStage {
	t.Helper()
	for _, stage := range stages {
		if strings.EqualFold(stage.name, name) {
			return stage
		}
	}
	t.Fatalf("Dockerfile 缺少名为 %q 的构建阶段", name)
	return dockerStage{}
}

// dockerDirectiveValue 返回某个阶段内第一条指定指令的参数部分（不含指令名）。
// 行尾反斜杠会先合并，避免把 HEALTHCHECK 的续行 CMD 误当成镜像 CMD。
func dockerDirectiveValue(stage dockerStage, directive string) string {
	for _, line := range dockerLogicalLines(stage.body) {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], directive) {
			return strings.TrimSpace(line[len(fields[0]):])
		}
	}
	return ""
}

// dockerEnvValues 解析阶段内的 `ENV key=value` 指令；不支持旧式 `ENV key value`。
func dockerEnvValues(stage dockerStage) map[string]string {
	values := make(map[string]string)
	for _, line := range dockerLogicalLines(stage.body) {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "ENV") {
			continue
		}
		for _, field := range fields[1:] {
			name, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			values[name] = strings.Trim(value, `"'`)
		}
	}
	return values
}

// dockerLogicalLines 把以反斜杠续行的 Dockerfile 行合并为逻辑行。
func dockerLogicalLines(body string) []string {
	var logical []string
	var current strings.Builder
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasSuffix(trimmed, "\\") {
			current.WriteString(strings.TrimSuffix(trimmed, "\\"))
			current.WriteString(" ")
			continue
		}
		current.WriteString(trimmed)
		if combined := strings.TrimSpace(current.String()); combined != "" {
			logical = append(logical, combined)
		}
		current.Reset()
	}
	if combined := strings.TrimSpace(current.String()); combined != "" {
		logical = append(logical, combined)
	}
	return logical
}

func dockerignoreContains(dockerignore, entry string) bool {
	for _, line := range strings.Split(dockerignore, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.TrimPrefix(strings.TrimPrefix(line, "./"), "/") == entry {
			return true
		}
	}
	return false
}
