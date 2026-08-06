package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// assertNoModuleArtifacts 断言一次失败的生成没有留下任何模块产物。
func assertNoModuleArtifacts(t *testing.T, root, name string) {
	t.Helper()
	for _, dir := range []string{
		filepath.Join(root, "internal", "module", name),
		filepath.Join(root, "internal", "repository", name),
	} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("failed generation left artifacts at %s (stat err=%v)", dir, err)
		}
	}
	cfgFile := filepath.Join(root, "internal", "platform", "config", "module_"+name+".go")
	if _, err := os.Stat(cfgFile); !os.IsNotExist(err) {
		t.Fatalf("failed generation left config file %s (stat err=%v)", cfgFile, err)
	}
}

// TestGenerateZeroWritesWhenAnchorMissing 是"要么全部落盘、要么零落盘"的负向证明：
// 任一锚点缺失时生成失败，且工作区没有任何新建或被修改的文件——
// 此前的实现先落盘模块文件、后校验接线锚点，缺锚点会留下半成品。
func TestGenerateZeroWritesWhenAnchorMissing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		file   string // 被破坏锚点所在文件（相对 root）
		anchor string
	}{
		{"providers 校验型锚点缺失", "internal/bootstrap/providers.go", "\t// scaffold:module-workers\n"},
		{"providers 注入型锚点缺失", "internal/bootstrap/providers.go", "\t// scaffold:module-services\n"},
		{"config 字段锚点缺失", "internal/platform/config/modules.go", "\t// scaffold:module-config-fields\n"},
		{"config 示例锚点缺失", "config/env.yml.example", "  # scaffold:module-config-example\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root := createProjectFixture(t)
			target := filepath.Join(root, filepath.FromSlash(tc.file))
			before, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			broken := strings.Replace(string(before), tc.anchor, "", 1)
			if broken == string(before) {
				t.Fatalf("fixture 不含锚点 %q，测试前提不成立", strings.TrimSpace(tc.anchor))
			}
			if err := os.WriteFile(target, []byte(broken), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			// 记录三个被注入文件的破坏后内容，失败后必须原样。
			watched := []string{
				"internal/bootstrap/providers.go",
				"internal/platform/config/modules.go",
				"config/env.yml.example",
			}
			snapshot := make(map[string]string, len(watched))
			for _, rel := range watched {
				content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil {
					t.Fatalf("ReadFile(%s): %v", rel, err)
				}
				snapshot[rel] = string(content)
			}

			generator, err := NewModuleGenerator(root)
			if err != nil {
				t.Fatalf("NewModuleGenerator: %v", err)
			}
			if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Bootstrap: true, Public: true}); err == nil {
				t.Fatal("Generate succeeded although an anchor is missing")
			}

			assertNoModuleArtifacts(t, root, "admin")
			for _, rel := range watched {
				content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
				if err != nil {
					t.Fatalf("ReadFile(%s): %v", rel, err)
				}
				if string(content) != snapshot[rel] {
					t.Fatalf("%s was modified by a failed generation", rel)
				}
			}
		})
	}
}

// TestGenerateRestoresFilesWhenCommitFails 钉住提交失败的恢复：
// 落盘中途失败时，先写入的文件被恢复——更新的回到原内容、新建的被删除。
//
// 不并行：通过包级写入缝注入失败。
func TestGenerateRestoresFilesWhenCommitFails(t *testing.T) {
	root := createProjectFixture(t)

	providersPath := filepath.Join(root, "internal", "bootstrap", "providers.go")
	modulesPath := filepath.Join(root, "internal", "platform", "config", "modules.go")
	examplePath := filepath.Join(root, "config", "env.yml.example")
	originals := map[string]string{}
	for _, p := range []string{providersPath, modulesPath, examplePath} {
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", p, err)
		}
		originals[p] = string(content)
	}

	// providers.go 是提交序列的最后一个目标：让它失败，前面的全部已写入，
	// 恢复因此必须真正发生。
	original := writeGeneratedFile
	defer func() { writeGeneratedFile = original }()
	writeGeneratedFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(path, filepath.Join("bootstrap", "providers.go")) {
			return errors.New("injected write failure")
		}
		return os.WriteFile(path, data, perm)
	}

	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator: %v", err)
	}
	_, err = generator.GenerateWithOptions("admin", GenerateOptions{Bootstrap: true, Public: true})
	if err == nil {
		t.Fatal("Generate succeeded although the final write failed")
	}
	if !strings.Contains(err.Error(), "injected write failure") {
		t.Fatalf("error %q does not carry the write failure cause", err)
	}

	assertNoModuleArtifacts(t, root, "admin")
	for p, want := range originals {
		content, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", p, err)
		}
		if string(content) != want {
			t.Fatalf("%s was not restored after the commit failure", p)
		}
	}
}

// TestGeneratePartialWriteDoesNotCorruptTarget 钉住单文件写入的原子性
// （module-scaffold-wiring spec）：磁盘错误导致部分写入时，目标文件要么是
// 完整新内容、要么原样不动，且不残留临时文件——直接截断写会把使用者的
// 既有文件毁成半截，而该文件因"未写成功"不在已写清单里、不会被回滚恢复。
//
// 不并行：通过包级临时文件写入缝注入失败。
func TestGeneratePartialWriteDoesNotCorruptTarget(t *testing.T) {
	root := createProjectFixture(t)
	providersPath := filepath.Join(root, "internal", "bootstrap", "providers.go")
	before, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// providers.go 是既有文件的更新目标：让它的临时文件"写一半后报磁盘错误"。
	original := tempFileWriter
	defer func() { tempFileWriter = original }()
	tempFileWriter = func(f *os.File, data []byte) (int, error) {
		if strings.Contains(f.Name(), "providers.go-") {
			n, _ := f.Write(data[:len(data)/2])
			return n, errors.New("injected disk failure")
		}
		return f.Write(data)
	}

	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator: %v", err)
	}
	if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Bootstrap: true, Public: true}); err == nil {
		t.Fatal("Generate succeeded although a write failed mid-way")
	} else if !strings.Contains(err.Error(), "injected disk failure") {
		t.Fatalf("error %q does not carry the write failure cause", err)
	}

	// 目标必须原样：部分写只允许污染临时文件。
	after, err := os.ReadFile(providersPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("providers.go was corrupted by a partial write")
	}

	// 不残留 .tmp。
	entries, err := os.ReadDir(filepath.Dir(providersPath))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file %q left behind after a failed write", e.Name())
		}
	}

	assertNoModuleArtifacts(t, root, "admin")
}

// TestGenerateRollbackKeepsForeignFilesInCreatedDirs 钉住回滚的边界（module-scaffold-wiring spec）：
// 失败回滚移除本次新建目录时只删已空目录；回滚窗口内出现在新建目录里的
// 非本次产物必须保留——恢复工作区的义务止于清除本次产物。
//
// 不并行：通过包级写入缝注入失败。
func TestGenerateRollbackKeepsForeignFilesInCreatedDirs(t *testing.T) {
	root := createProjectFixture(t)
	moduleDir := filepath.Join(root, "internal", "module", "admin")
	foreignFile := filepath.Join(moduleDir, "foreign.txt")

	// providers.go 是提交序列的最后一个目标：此前模块目录与文件均已写入。
	// 在失败前把一个外来文件放进新建的模块目录，模拟并发出现的使用者产物。
	original := writeGeneratedFile
	defer func() { writeGeneratedFile = original }()
	writeGeneratedFile = func(path string, data []byte, perm os.FileMode) error {
		if strings.HasSuffix(path, filepath.Join("bootstrap", "providers.go")) {
			if err := os.WriteFile(foreignFile, []byte("user content"), 0o644); err != nil {
				t.Fatalf("plant foreign file: %v", err)
			}
			return errors.New("injected write failure")
		}
		return os.WriteFile(path, data, perm)
	}

	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator: %v", err)
	}
	if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Bootstrap: true, Public: true}); err == nil {
		t.Fatal("Generate succeeded although the final write failed")
	}

	// 外来文件必须原样保留。
	content, err := os.ReadFile(foreignFile)
	if err != nil {
		t.Fatalf("foreign file was removed by rollback: %v", err)
	}
	if string(content) != "user content" {
		t.Fatalf("foreign file content changed: %q", content)
	}

	// 本次产物必须清除干净：模块目录下除外来文件不残留任何条目。
	entries, err := os.ReadDir(moduleDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", moduleDir, err)
	}
	for _, e := range entries {
		if e.Name() != "foreign.txt" {
			t.Fatalf("rollback left generated artifact %q in %s", e.Name(), moduleDir)
		}
	}
}

// TestGeneratedModuleConfigHasNoUnconsumedToggle 钉住"生成的配置不携带无消费者字段"：
// 模块配置不含模块级启用开关，配置示例注入块也没有对应条目。
func TestGeneratedModuleConfigHasNoUnconsumedToggle(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator: %v", err)
	}
	if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Bootstrap: true, Public: true}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	cfgPath := filepath.Join(root, "internal", "platform", "config", "module_admin.go")
	assertFileNotContains(t, cfgPath, "Enabled")
	assertFileNotContains(t, cfgPath, "enabled")

	examplePath := filepath.Join(root, "config", "env.yml.example")
	assertFileNotContains(t, examplePath, "enabled:")
	// 有消费者的字段仍在。
	assertFileContains(t, cfgPath, "PageSize")
	assertFileContains(t, examplePath, "page_size:")
}
