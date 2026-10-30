// Package compliance 提供仓库级许可证与 clean-room 基线检查。
//
// 注意：自动扫描只能发现明显的复制、导入与依赖痕迹，不能替代人工
// 许可证、来源与代码审计。LICENSES.md 中的参考项目名称不会进入生产
// 源码扫描范围。
package compliance

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// referenceLicenses 是四个只读参考项目的许可证基线。
var referenceLicenses = []struct {
	project string
	license string
}{
	{project: "sub2api", license: "lgpl-3.0-or-later"},
	{project: "new-api", license: "agpl"},
	{project: "juhe-ai", license: "apache-2.0"},
	{project: "claude-code-hub", license: "mit"},
}

// forbiddenDependencyFragments 是不允许出现在 go.mod / go.sum 的模块路径片段。
// 覆盖 Redis、消息队列与多租户相关依赖；SQLite 是唯一允许的存储。
var forbiddenDependencyFragments = []string{
	"redis",
	"redigo",
	"miniredis",
	"nats.io",
	"nats-io",
	"rabbitmq",
	"streadway/amqp",
	"amqp091",
	"kafka",
	"sarama",
	"nsqio",
	"go-nsq",
	"asynq",
	"machinery",
	"message-queue",
	"pubsub",
	"multi-tenant",
	"multitenant",
}

// referenceIdentifiers 是参考项目专有标识；生产源码中不应出现。
var referenceIdentifiers = []string{
	"sub2api",
	"new-api",
	"juhe-ai",
	"claude-code-hub",
}

// productionRoots 与 productionExtensions 定义 clean-room 扫描范围。
var productionRoots = []string{
	"cmd",
	"internal",
	"web/src",
	"e2e",
}

var productionExtensions = map[string]bool{
	".go":  true,
	".ts":  true,
	".tsx": true,
	".js":  true,
}

// excludedProductionPaths 显式排除且不遍历的目录/文件。
var excludedProductionPaths = []string{
	"reference",
	".zcode",
	"web/node_modules",
	"internal/webassets/dist",
	"docs",
	"LICENSES.md",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件路径")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readRepoFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", name, err)
	}
	return string(data)
}

func TestLicensesDocumentReferenceProjects(t *testing.T) {
	root := repoRoot(t)
	licenses := strings.ToLower(readRepoFile(t, root, "LICENSES.md"))
	if strings.TrimSpace(licenses) == "" {
		t.Fatal("LICENSES.md 为空")
	}

	lines := strings.Split(licenses, "\n")
	for _, ref := range referenceLicenses {
		found := false
		for _, line := range lines {
			if strings.Contains(line, ref.project) && strings.Contains(line, ref.license) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("LICENSES.md 缺少参考项目 %s 的许可证标记 %q（需同一行）", ref.project, ref.license)
		}
	}
}

func TestGoModuleBaseline(t *testing.T) {
	root := repoRoot(t)
	gomod := readRepoFile(t, root, "go.mod")

	if !regexp.MustCompile(`(?m)^module\s+ai-router\s*$`).MatchString(gomod) {
		t.Errorf("go.mod module 必须为 ai-router:\n%s", gomod)
	}
	if !regexp.MustCompile(`(?m)^go\s+1\.26(\.\d+)?\s*$`).MatchString(gomod) {
		t.Errorf("go.mod 必须声明 Go 1.26.x:\n%s", gomod)
	}

	direct := directRequires(gomod)
	hasSQLite := false
	for _, module := range direct {
		if module == "modernc.org/sqlite" {
			hasSQLite = true
		}
	}
	if !hasSQLite {
		t.Errorf("modernc.org/sqlite 必须作为直接依赖固定在 go.mod 中，当前直接依赖: %v", direct)
	}

	gosum := readRepoFile(t, root, "go.sum")
	if strings.TrimSpace(gosum) == "" {
		t.Fatal("go.sum 为空；请运行 go mod tidy / go mod download 固定依赖校验和")
	}
	if !strings.Contains(gosum, "modernc.org/sqlite v") {
		t.Errorf("go.sum 缺少 modernc.org/sqlite 的校验和")
	}
}

func TestDirectDependenciesHaveLicenses(t *testing.T) {
	root := repoRoot(t)
	gomod := readRepoFile(t, root, "go.mod")
	licenses := readRepoFile(t, root, "LICENSES.md")
	lines := strings.Split(licenses, "\n")

	licenseMarkers := []string{
		"MIT", "Apache-2.0", "BSD-3-Clause", "BSD-2-Clause",
		"ISC", "MPL-2.0", "Unlicense", "LGPL", "AGPL", "GPL",
	}

	for _, module := range directRequires(gomod) {
		var line string
		for _, candidate := range lines {
			if strings.Contains(candidate, module) {
				line = candidate
				break
			}
		}
		if line == "" {
			t.Errorf("LICENSES.md 未记录直接依赖 %s", module)
			continue
		}

		licensed := false
		for _, marker := range licenseMarkers {
			if strings.Contains(line, marker) {
				licensed = true
				break
			}
		}
		if !licensed {
			t.Errorf("直接依赖 %s 的许可证行缺少许可证标识: %q", module, line)
		}
	}
}

func TestNoForbiddenDependencies(t *testing.T) {
	root := repoRoot(t)

	modules := make(map[string]string)
	for _, req := range parseGoModRequires(readRepoFile(t, root, "go.mod")) {
		modules[req.path] = "go.mod"
	}
	for _, module := range parseGoSumModules(readRepoFile(t, root, "go.sum")) {
		if _, ok := modules[module]; !ok {
			modules[module] = "go.sum"
		}
	}

	for module, source := range modules {
		lower := strings.ToLower(module)
		for _, fragment := range forbiddenDependencyFragments {
			if strings.Contains(lower, fragment) {
				t.Errorf("%s 包含禁用依赖标识 %q（模块 %s）", source, fragment, module)
			}
		}
	}
}

func TestProductionSourcesHaveNoReferenceIdentifiers(t *testing.T) {
	root := repoRoot(t)
	scanned := 0

	for _, scanRoot := range productionRoots {
		base := filepath.Join(root, filepath.FromSlash(scanRoot))
		err := filepath.WalkDir(base, func(filePath string, entry fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}

			rel, err := filepath.Rel(root, filePath)
			if err != nil {
				return err
			}
			relSlash := filepath.ToSlash(rel)

			if entry.IsDir() {
				if isExcludedProductionPath(relSlash) {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.Type().IsRegular() {
				return nil
			}
			if isExcludedProductionPath(relSlash) {
				return nil
			}
			if !productionExtensions[strings.ToLower(filepath.Ext(entry.Name()))] {
				return nil
			}

			data, err := os.ReadFile(filePath)
			if err != nil {
				return err
			}
			scanned++
			content := strings.ToLower(string(data))
			if strings.Contains(content, "reference/") {
				t.Errorf("%s 包含 reference/ 路径或导入", relSlash)
			}
			for _, identifier := range referenceIdentifiers {
				if strings.Contains(content, identifier) {
					t.Errorf("%s 包含参考项目专有标识 %q", relSlash, identifier)
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("扫描 %s 失败: %v", scanRoot, err)
		}
	}

	if scanned == 0 {
		t.Fatal("未扫描到任何生产源码，请检查扫描根目录")
	}
}

func isExcludedProductionPath(relSlash string) bool {
	relSlash = path.Clean(strings.TrimPrefix(relSlash, "./"))
	if relSlash == "." {
		return false
	}

	// 测试文件与扫描器自身不参与生产源码标识检查。
	base := path.Base(relSlash)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, ".test.ts"):
		return true
	case strings.HasSuffix(base, ".test.tsx"):
		return true
	case strings.HasSuffix(base, ".test.js"):
		return true
	case relSlash == "internal/compliance/licenses_test.go":
		return true
	}

	for _, excluded := range excludedProductionPaths {
		if relSlash == excluded || strings.HasPrefix(relSlash, excluded+"/") {
			return true
		}
	}
	return false
}

// moduleRequirement 是 go.mod require 条目；indirect 表示仅被依赖间接引入。
type moduleRequirement struct {
	path     string
	indirect bool
}

// parseGoModRequires 解析 go.mod 的 require 块与单行 require 指令。
func parseGoModRequires(gomod string) []moduleRequirement {
	var requirements []moduleRequirement
	inRequireBlock := false

	for _, rawLine := range strings.Split(gomod, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		indirect := false
		if comment := strings.Index(line, "//"); comment >= 0 {
			indirect = strings.Contains(line[comment:], "indirect")
			line = strings.TrimSpace(line[:comment])
		}

		switch {
		case strings.HasPrefix(line, "require ("):
			inRequireBlock = true
		case inRequireBlock && line == ")":
			inRequireBlock = false
		case inRequireBlock:
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				requirements = append(requirements, moduleRequirement{path: fields[0], indirect: indirect})
			}
		case strings.HasPrefix(line, "require "):
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				requirements = append(requirements, moduleRequirement{path: fields[1], indirect: indirect})
			}
		}
	}

	return requirements
}

// directRequires 返回 go.mod 中未标记 // indirect 的直接依赖模块路径。
func directRequires(gomod string) []string {
	var modules []string
	for _, requirement := range parseGoModRequires(gomod) {
		if !requirement.indirect {
			modules = append(modules, requirement.path)
		}
	}
	return modules
}

// parseGoSumModules 返回 go.sum 中出现的模块路径（去重前；调用方按需去重）。
func parseGoSumModules(gosum string) []string {
	var modules []string
	for _, rawLine := range strings.Split(gosum, "\n") {
		fields := strings.Fields(rawLine)
		if len(fields) >= 2 && strings.HasPrefix(fields[1], "v") {
			modules = append(modules, fields[0])
		}
	}
	return modules
}
