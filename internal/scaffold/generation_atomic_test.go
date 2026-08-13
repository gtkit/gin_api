//go:build scaffold

package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestVerifyPlanUnchangedRejectsDrift 钉住提交前的乐观并发校验
// （module-scaffold-wiring spec）：规划与提交之间被并发改动的目标让提交
// 零写入中止——否则本进程会用陈旧内容覆盖另一个生成进程的产物。
func TestVerifyPlanUnchangedRejectsDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(t *testing.T, dir string) pendingWrite
		wantErr string
	}{
		{
			name: "更新目标内容漂移",
			prepare: func(t *testing.T, dir string) pendingWrite {
				t.Helper()
				path := filepath.Join(dir, "target.go")
				if err := os.WriteFile(path, []byte("drifted"), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return pendingWrite{path: path, content: []byte("new"), existed: true, original: []byte("planned")}
			},
			wantErr: "changed since it was read",
		},
		{
			name: "新建目标被并发创建",
			prepare: func(t *testing.T, dir string) pendingWrite {
				t.Helper()
				path := filepath.Join(dir, "created.go")
				if err := os.WriteFile(path, []byte("someone else"), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return pendingWrite{path: path, content: []byte("new"), existed: false}
			},
			wantErr: "was created since planning",
		},
		{
			name: "无漂移通过",
			prepare: func(t *testing.T, dir string) pendingWrite {
				t.Helper()
				path := filepath.Join(dir, "stable.go")
				if err := os.WriteFile(path, []byte("planned"), 0o644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return pendingWrite{path: path, content: []byte("new"), existed: true, original: []byte("planned")}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := tt.prepare(t, t.TempDir())
			err := verifyPlanUnchanged([]pendingWrite{w})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("verifyPlanUnchanged: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// TestGenerationLockRejectsConcurrentRun 钉住项目级建议锁：
// 锁被持有时第二次生成立即报错，不等待、不写入。
func TestGenerationLockRejectsConcurrentRun(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	release, err := acquireGenerationLock(root)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer release()

	if _, err := acquireGenerationLock(root); err == nil {
		t.Fatal("a second generation acquired the lock while the first still holds it")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v, want the concurrent-run explanation", err)
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

// TestGeneratorKeepsProjectBaselineConsistent 钉住生成物与项目基线一致
// （module-scaffold-wiring spec）：新模型进入迁移命令的 DDL 集合、
// 生成 SQL 的排序规则与项目基线一致。
func TestGeneratorKeepsProjectBaselineConsistent(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator: %v", err)
	}
	generator.now = func() time.Time { return time.Date(2026, 3, 24, 9, 8, 7, 0, time.UTC) }

	if _, err := generator.GenerateWithOptions("admin", GenerateOptions{Bootstrap: true, Public: true}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// 新模型必须能被 --from-model 发现：不注入的话使用者以为迁移这层也接好了。
	assertFileContains(t, filepath.Join(root, "cmd", "migrate.go"), "&models.Admin{},")

	// 排序规则对齐 database.collation 的项目基线：同一份 SQL 在不同默认
	// collation 的实例上，比较与排序结果会不同。
	upSQL := filepath.Join(root, "database", "migrations", "2026_03_24_090807_create_admins_table.up.sql")
	assertFileContains(t, upSQL, "COLLATE=utf8mb4_unicode_ci")
	assertFileNotContains(t, upSQL, "utf8mb4_0900_ai_ci")
}

// TestForceReportsModelDrift 钉住 --force 的分裂风险可见：
// 模型不被覆盖，其余层被重新生成，结果里必须有提示。
func TestForceReportsModelDrift(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	modelPath := filepath.Join(root, "internal", "models", "admin.go")
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// 使用者手写演进过的模型。
	if err := os.WriteFile(modelPath, []byte("package models\n\ntype Admin struct{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	generator, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator: %v", err)
	}
	result, err := generator.GenerateWithOptions("admin", GenerateOptions{
		Force: true, Bootstrap: true, Public: true,
	})
	if err != nil {
		t.Fatalf("Generate with --force: %v (refreshing generated layers is what --force is for)", err)
	}

	var noted bool
	for _, note := range result.Notes {
		if strings.Contains(note, "admin.go") && strings.Contains(note, "不会") {
			noted = true
		}
	}
	if !noted {
		t.Fatalf("no drift note was reported; notes = %v", result.Notes)
	}
}

// TestProjectRootResolvesSymlinks 钉住项目根经符号链接归一化
// （module-scaffold-wiring spec）：根路径同时决定并发锁的路径，两条指向
// 同一项目的等价路径若算出两把不同的锁，并发防护就被绕过了。
func TestProjectRootResolvesSymlinks(t *testing.T) {
	t.Parallel()

	root := createProjectFixture(t)
	link := filepath.Join(t.TempDir(), "linked-project")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	direct, err := NewModuleGenerator(root)
	if err != nil {
		t.Fatalf("NewModuleGenerator(root): %v", err)
	}
	viaLink, err := NewModuleGenerator(link)
	if err != nil {
		t.Fatalf("NewModuleGenerator(symlink): %v", err)
	}

	if direct.root != viaLink.root {
		t.Fatalf("root via symlink = %q, want the same as direct %q: two different roots mean "+
			"two different generation locks, and the concurrency guard is bypassed",
			viaLink.root, direct.root)
	}
}
