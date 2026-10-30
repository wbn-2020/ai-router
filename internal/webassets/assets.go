// Package webassets 嵌入管理端 SPA 的构建产物。
//
// 项目只允许这一个 embed 包：Go 的 //go:embed 模式相对包目录解析，
// 因此 Vite 必须把产物输出到 internal/webassets/dist，不能引用包目录
// 之外的路径。
package webassets

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
)

// distFS 是编译期嵌入的 dist 目录。
//
//go:embed dist/*
var distFS embed.FS

// Dist 返回以 dist 为根的只读文件系统。
//
// T05 阶段 dist 中只有占位 index.html；T10 起由 Vite 构建产物覆盖。
func Dist() (fs.FS, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, fmt.Errorf("webassets: 打开嵌入的 dist 目录失败：%w", err)
	}
	return sub, nil
}

// Index 返回嵌入的 dist/index.html 内容。
func Index() ([]byte, error) {
	data, err := fs.ReadFile(distFS, "dist/index.html")
	if err != nil {
		return nil, fmt.Errorf("webassets: 读取嵌入的 index.html 失败：%w", err)
	}
	if len(data) == 0 {
		return nil, errors.New("webassets: 嵌入的 index.html 为空")
	}
	return data, nil
}
