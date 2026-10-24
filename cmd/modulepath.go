package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// resolveModulePath 返回**当前项目自己的** Go module path。
//
// 它被 migratex 的 WithProjectName 消费，用作生成代码的 import 路径。硬编码母版路径
// 会让派生项目 `migrate make model X` 生成 import 母版 module path 的代码，而且是**静默**成功。
//
// 三段来源，顺序不可调换：
//
//  1. debug.ReadBuildInfo().Main.Path —— 主来源。编译期写入二进制，生产镜像内同样可用。
//  2. 向上查找 go.mod 的 module 行 —— **仅作开发/测试兜底**（`go run` 的某些形态下
//     Main.Path 为 "command-line-arguments"）。绝不能作主来源：生产镜像里没有 go.mod。
//  3. 都失败则报错退出 —— 不回落任何硬编码值。
//
// 明确不做：从 APP_NAME 推导。module path 由 gonew 决定，与应用名相互独立
// （github.com/acme/foo 的 APP_NAME 完全可以是 bar）。
func resolveModulePath() (string, error) {
	if info, ok := debug.ReadBuildInfo(); ok {
		if p := strings.TrimSpace(info.Main.Path); p != "" && p != "command-line-arguments" {
			return p, nil
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve module path: build info unavailable and working directory unreadable: %w", err)
	}
	p, err := modulePathFromGoMod(wd)
	if err != nil {
		return "", fmt.Errorf("cannot determine the module path from build info or go.mod: %w", err)
	}
	return p, nil
}

// modulePathFromGoMod 从 startDir 向上查找 go.mod 并解析其 module 行。
func modulePathFromGoMod(startDir string) (string, error) {
	dir := startDir
	for {
		path := filepath.Join(dir, "go.mod")
		if p, err := readModuleDirective(path); err == nil {
			return p, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read module directive: %w", err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found upward from %q", startDir)
		}
		dir = parent
	}
}

func readModuleDirective(goModPath string) (_ string, err error) {
	f, err := os.Open(goModPath)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", goModPath, closeErr))
		}
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			if p := strings.TrimSpace(rest); p != "" {
				return p, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan %s: %w", goModPath, err)
	}
	return "", fmt.Errorf("module directive not found in %s", goModPath)
}
