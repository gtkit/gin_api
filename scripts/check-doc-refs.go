//go:build ignore

// check-doc-refs.go —— 文档内引用的**存在性**检查。
//
// 为什么需要它：`docs/` 自称工程约定的单一来源，文档之间与文档到代码的引用密集。
// 文件改名或移动之后，这些引用不会报错、不会进日志，只会在有人照着文档找不到东西时
// 才被发现——而那时读者得到的结论通常是「文档没用」，而不是「这一条过期了」。
// 这正是 docs/README.md 自己写的那句：只写在文档里的约束会被无声违反。
//
// 用法：
//
//	go run ./scripts/check-doc-refs.go              校验
//	go run ./scripts/check-doc-refs.go --print-scope 打印覆盖范围
//	go run ./scripts/check-doc-refs.go --self-test   自检：证明判据确实会失败
//
// ── 覆盖范围 ────────────────────────────────────────────────────────────
// 扫描**被 git 跟踪的** `*.md`，检查两类引用：
//
//  1. Markdown 行内链接 `[文字](相对路径)` —— 按 Markdown 语义相对文档所在目录解析。
//  2. 反引号里的仓内路径 `docs/foo.md` —— 先按仓库根解析，再按文档所在目录解析，
//     两者都不存在才算失败。
//
// 第 2 类是本仓真正会腐烂的那一类：文档表格里的路径大多是反引号裸路径，不是链接。
// 只查第 1 类会让判据在 33 条链接上全绿，而 300 多条裸路径引用无人看管——
// 那是虚假的完整性保证，比没有判据更糟。
//
// ── 能力边界（必须如实声明，不得表述为「文档引用已被自动化保证」）──────
// 它是一条**存在性**判据。它检测不到：
//   - 引用指向了一个存在但错误的文件（拼错成另一个真实文件）；
//   - 锚点（`#小节`）是否真的存在——锚点被剥离后只校验文件部分；
//   - 不在反引号里、也不是链接的散文式路径提及；
//   - 符号引用（`internal/pkg/log.AccessLog()` 这类 `包路径.符号`）是否仍然存在——
//     那需要解析 Go 符号，属另一件事，这里刻意排除以免误报淹没真实缺陷。
//
// 判定为「仓内路径」的规则刻意保守：必须含 `/`，且末段带已知扩展名或以 `/` 结尾。
// 因此 `gorm.io/gorm`（末段无扩展名）与 `internal/platform/data.OpenDB`
// （`.OpenDB` 不是已知扩展名）都不参与判定——排除符号与外部模块靠的是这条规则，
// 不是维护一份例外名单。含 `{}`/`*`/`<>` 的形态（花括号展开、glob、占位符）同样跳过。
//
// 不硬编码任何顶层目录名或模块路径：那种正则在派生项目里可能永远匹配不到东西，
// 判据会静默全绿。规则只依赖「像不像路径」与「在不在」。
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// allowlistPath 是豁免登记表。
//
// 存在的理由：有些被引用的路径**合法地不存在**——运行时生成且被 git 忽略的产物目录
// 就是一例。它们必须能被登记，否则判据会逼人把文档里正确的说明删掉。
const allowlistPath = "scripts/doc-refs-allowlist.txt"

// pathExtensions 是「这个 token 是不是仓内路径」的判据之一。
//
// 用扩展名而不是顶层目录前缀：前缀清单会在目录被删除后失效（引用一个已删目录里的文件
// 恰恰是最该报的情况，而前缀判定此时会跳过它）。扩展名不随目录存亡变化。
var pathExtensions = map[string]bool{
	".go": true, ".md": true, ".sh": true, ".sql": true, ".yml": true,
	".yaml": true, ".mk": true, ".txt": true, ".json": true, ".toml": true,
	".example": true, ".ps1": true, ".dockerignore": true,
}

var (
	linkRe   = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	inlineRe = regexp.MustCompile("`([^`\n]+)`")
)

type ref struct {
	doc  string // 引用所在文档
	line int
	text string // 原始引用文本（含锚点）
	kind string // "link" | "path"
}

// allowKey 是登记键：文档 + 引用的**文件部分**，刻意不含行号，也刻意剥掉锚点。
//
// 不含行号：行号会随无关改动漂移，让登记表变成需要频繁维护的噪声，而一个总在
// 报假警的门禁最终会被整体放宽。
//
// 剥掉锚点是必需的，不只是为了整洁：登记表以 `#` 起注释，键里带 `#` 会被解析器
// 截断，于是 `[x](missing.md#sec)` 这类引用将**无法被登记**——判据给出一条
// 没有出路的失败。剥掉锚点后键正是被校验的那个文件部分，两者同源。
func (r ref) allowKey() string { return r.doc + ":" + stripAnchor(r.text) }

func main() {
	if err := run(); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	switch {
	case len(os.Args) > 1 && os.Args[1] == "--print-scope":
		return printScope()
	case len(os.Args) > 1 && os.Args[1] == "--self-test":
		return selfTest()
	}

	// 一律相对 git 顶层解析，不依赖当前工作目录。
	//
	// 从子目录运行时，`git ls-files` 只列该子树、登记表也读不到——结果是一整屏
	// 「引用解析不到」的假失败，而真正的原因（跑错了目录）一个字都不会出现。
	root, err := repoRoot()
	if err != nil {
		return fmt.Errorf("FAIL: %w", err)
	}

	docs, err := trackedDocs(root)
	if err != nil {
		return fmt.Errorf("FAIL: %w", err)
	}
	// 空集合即失败：本仓必然有被跟踪的 markdown，扫到 0 份只可能是枚举坏了。
	// 那种坏法的表现是"全绿"——自检覆盖的是判定逻辑，覆盖不到这里的枚举。
	if len(docs) == 0 {
		return errors.New("FAIL: no tracked *.md found — the scan set is empty, which cannot be right")
	}

	dead, err := findDead(root, docs)
	if err != nil {
		return fmt.Errorf("FAIL: %w", err)
	}

	allowed, err := loadAllowlist(filepath.Join(root, allowlistPath))
	if err != nil {
		return fmt.Errorf("FAIL: %w", err)
	}

	var unlisted []string
	claimed := map[string]bool{}
	for _, r := range dead {
		key := r.allowKey()
		if allowed[key] {
			claimed[key] = true
			continue
		}
		// 失败信息带上登记键：键剥了锚点，与引用原文不一定一致，
		// 不给出来会让「按提示登记」这一步自己变成一次试错。
		unlisted = append(unlisted, fmt.Sprintf(
			"%s:%d: %s reference %q does not resolve (allowlist key: %s)",
			r.doc, r.line, r.kind, r.text, key))
	}

	// 豁免不得比现实活得久：一条不再命中的豁免要么是引用已修好，要么是文档已删——
	// 两种情况下它都只是一条没人看的例外，留着会掩盖下一次真实缺陷。
	var stale []string
	for key := range allowed {
		if !claimed[key] {
			stale = append(stale, key)
		}
	}

	failed := false
	if len(unlisted) > 0 {
		sort.Strings(unlisted)
		if _, writeErr := fmt.Println("FAIL: documentation references must resolve to real paths"); writeErr != nil {
			return fmt.Errorf("write tool output: %w", writeErr)
		}
		if _, writeErr := fmt.Println("  Either fix the reference, or claim it in " + allowlistPath); writeErr != nil {
			return fmt.Errorf("write tool output: %w", writeErr)
		}
		if _, writeErr := fmt.Println("  with an explicit owner and reason."); writeErr != nil {
			return fmt.Errorf("write tool output: %w", writeErr)
		}
		for _, f := range unlisted {
			if _, writeErr := fmt.Println(" ", f); writeErr != nil {
				return fmt.Errorf("write tool output: %w", writeErr)
			}
		}
		failed = true
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		if _, writeErr := fmt.Println("FAIL: stale entries in " + allowlistPath + " (the reference now resolves, or is gone)"); writeErr != nil {
			return fmt.Errorf("write tool output: %w", writeErr)
		}
		if _, writeErr := fmt.Println("  Remove them — an exemption that no longer applies hides the next real defect."); writeErr != nil {
			return fmt.Errorf("write tool output: %w", writeErr)
		}
		for _, s := range stale {
			if _, writeErr := fmt.Println(" ", s); writeErr != nil {
				return fmt.Errorf("write tool output: %w", writeErr)
			}
		}
		failed = true
	}
	if failed {
		return errors.New("documentation reference check failed")
	}

	if _, writeErr := fmt.Printf("doc reference check OK (%d docs, %d claimed exemptions)\n", len(docs), len(allowed)); writeErr != nil {
		return fmt.Errorf("write tool output: %w", writeErr)
	}
	return nil
}

func printScope() error {
	if _, writeErr := fmt.Println("scope: git-tracked *.md"); writeErr != nil {
		return fmt.Errorf("write tool output: %w", writeErr)
	}
	if _, writeErr := fmt.Println("  kind=link  markdown inline links [text](path), resolved against the doc's directory"); writeErr != nil {
		return fmt.Errorf("write tool output: %w", writeErr)
	}
	if _, writeErr := fmt.Println("  kind=path  backticked repo paths, resolved against repo root then the doc's directory"); writeErr != nil {
		return fmt.Errorf("write tool output: %w", writeErr)
	}
	if _, writeErr := fmt.Println("existence only: anchors, wrong-but-existing targets and Go symbol references are out of scope"); writeErr != nil {
		return fmt.Errorf("write tool output: %w", writeErr)
	}
	return nil
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-toplevel: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// trackedDocs 列出被跟踪的 markdown，路径相对仓库根。
//
// git 的执行目录钉在 root：`git ls-files` 的 pathspec 相对**当前目录**解析，
// 从子目录运行只会列出该子树——`--full-name` 只改输出格式，不改扫描范围。
// 两者都要，前者保证范围是全仓，后者保证输出可直接与 root 拼接。
func trackedDocs(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--full-name", "-z", "*.md")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var docs []string
	for f := range strings.SplitSeq(string(out), "\x00") {
		if strings.HasSuffix(f, ".md") {
			docs = append(docs, f)
		}
	}
	sort.Strings(docs)
	return docs, nil
}

// findDead 返回 docs 中所有解析不到的引用。root 是仓库根（自检时是临时目录）。
func findDead(root string, docs []string) ([]ref, error) {
	var dead []ref
	for _, doc := range docs {
		data, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", doc, err)
		}
		for _, r := range scanDoc(doc, string(data)) {
			if !resolves(root, r) {
				dead = append(dead, r)
			}
		}
	}
	return dead, nil
}

// scanDoc 提取一份文档里的全部候选引用，跳过围栏代码块。
func scanDoc(doc, text string) []ref {
	var refs []ref
	inFence := false
	for i, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		lineNo := i + 1

		// 先定位行内代码跨度：链接匹配必须跳过落在其中的命中。
		//
		// 介绍 Markdown 写法的文档会把 `[文字](路径)` 写成行内代码，那不是链接，
		// 而是一段示例文本。不排除的话，「讲链接的文档」这一类必然被误报——
		// 而它恰恰是最需要这条判据的文档。
		codeSpans := inlineRe.FindAllStringIndex(line, -1)
		inCode := func(pos int) bool {
			for _, s := range codeSpans {
				if pos >= s[0] && pos < s[1] {
					return true
				}
			}
			return false
		}

		for _, m := range linkRe.FindAllStringSubmatchIndex(line, -1) {
			if inCode(m[0]) {
				continue
			}
			if target := line[m[2]:m[3]]; isRelativeLink(target) {
				refs = append(refs, ref{doc: doc, line: lineNo, text: target, kind: "link"})
			}
		}
		for _, m := range inlineRe.FindAllStringSubmatch(line, -1) {
			if token := strings.TrimSpace(m[1]); looksLikeRepoPath(token) {
				refs = append(refs, ref{doc: doc, line: lineNo, text: token, kind: "path"})
			}
		}
	}
	return refs
}

func isRelativeLink(target string) bool {
	if target == "" || hasPlaceholder(target) {
		return false
	}
	lower := strings.ToLower(target)
	for _, scheme := range []string{"http://", "https://", "mailto:", "tel:", "ftp://", "//"} {
		if strings.HasPrefix(lower, scheme) {
			return false
		}
	}
	// 纯锚点指向本文档内部，没有文件部分可校验。
	return !strings.HasPrefix(target, "#")
}

// looksLikeRepoPath 判断反引号 token 是否为仓内路径。判据见文件头「能力边界」。
//
// 三类形似路径的 token 被排除，它们都不是仓内引用：
//
//	绝对路径打头（`/debug/pprof/`）——HTTP 端点路径与绝对文件路径都不相对本仓；
//	家目录 / 环境变量（`~/pprof/`、`$HOME/pprof/`）——解析结果因机器而异；
//	单段目录（`profiles/`、`bootstrap/`）——那是章节内简写，不是可解析的引用。
//
// 单段目录这条会放过对顶层目录的引用（如 `docs/`），是有意的取舍：顶层目录名
// 稳定且极少腐烂，而把简写一律报成缺陷会让判据在真实缺陷之外产出十几条噪音——
// 一个总在报假警的门禁最终会被整体放宽。
func looksLikeRepoPath(token string) bool {
	if token == "" || strings.ContainsAny(token, " \t") || hasPlaceholder(token) {
		return false
	}
	if strings.HasPrefix(token, "/") || strings.HasPrefix(token, "~") || strings.Contains(token, "$") {
		return false
	}
	// 至少一个**内部**分隔符：仅以 `/` 结尾的单段目录不算引用。
	if !strings.Contains(strings.TrimSuffix(token, "/"), "/") {
		return false
	}
	if strings.HasSuffix(token, "/") {
		return true
	}
	base := filepath.Base(stripAnchor(token))
	return pathExtensions[strings.ToLower(filepath.Ext(base))]
}

func hasPlaceholder(s string) bool { return strings.ContainsAny(s, "{}*<>?|") }

func stripAnchor(s string) string {
	before, _, _ := strings.Cut(s, "#")
	return before
}

// resolves 报告引用是否指向一个真实存在的路径。
//
// 链接按 Markdown 语义只相对文档目录解析；反引号裸路径两种写法都常见，
// 因此先按仓库根再按文档目录试——只有两者都不存在才算失败。
func resolves(root string, r ref) bool {
	target := stripAnchor(r.text)
	if target == "" {
		return true // 只有锚点，文件部分为空
	}
	target = strings.TrimSuffix(target, "/")
	docDir := filepath.Dir(r.doc)

	candidates := []string{filepath.Join(root, docDir, target)}
	if r.kind == "path" {
		candidates = append([]string{filepath.Join(root, target)}, candidates...)
	}
	for _, c := range candidates {
		if _, err := os.Lstat(c); err == nil {
			return true
		}
	}
	return false
}

func loadAllowlist(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("read allowlist: %w", err)
	}
	allowed := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, comment, _ := strings.Cut(line, "#")
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// 每条登记必须写明所有者与理由，否则它只是一个没人负责的豁免。
		if strings.TrimSpace(comment) == "" {
			return nil, fmt.Errorf("allowlist entry %q has no owner/reason comment", entry)
		}
		allowed[entry] = true
	}
	return allowed, nil
}

// ── 自检 ────────────────────────────────────────────────────────────────
//
// 本判据的失效**不会有任何症状**：正则写坏、扫描集合取空、解析规则收得过紧，
// 表现都是「全绿」，与文档引用全部正确一模一样。因此自检必须证明两件事：
// 真实缺陷会被抓到，且正确的引用不会被误报。缺了后半条，判据会靠误报把自己
// 逼进被整体放宽的结局。
func selfTest() (err error) {
	dir, err := os.MkdirTemp("", "doc-refs-selftest-")
	if err != nil {
		return fmt.Errorf("FAIL: self-test: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(dir); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove self-test fixture %s: %w", dir, removeErr))
		}
	}()

	write := func(rel, content string) error {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("FAIL: self-test: %w", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return fmt.Errorf("FAIL: self-test: %w", err)
		}
		return nil
	}

	// 真实存在的被引用目标。
	if err := write("docs/real.md", "real\n"); err != nil {
		return err
	}
	if err := write("scripts/real.sh", "#!/bin/sh\n"); err != nil {
		return err
	}

	if err := write("docs/probe.md", strings.Join([]string{
		"[dead link](missing.md)",               // 应报：链接目标不存在
		"[live link](real.md)",                  // 不应报
		"see `docs/missing.md` for details",     // 应报：反引号裸路径，仓库根解析失败
		"see `docs/real.md` for details",        // 不应报：仓库根解析成功
		"see `real.sh` no slash",                // 不应报：无 `/`，不判定为路径
		"see `scripts/real.sh` ok",              // 不应报
		"symbol `internal/pkg/log.AccessLog()`", // 不应报：符号引用
		"module `gorm.io/gorm` external",        // 不应报：末段无扩展名
		"brace `cmd/{a,b}.go` skipped",          // 不应报：占位形态
		"glob `docs/*.md` skipped",              // 不应报：占位形态
		"endpoint `/debug/pprof/` absolute",     // 不应报：绝对路径打头
		"home `~/pprof/` tilde",                 // 不应报：家目录
		"env `$HOME/pprof/` expansion",          // 不应报：环境变量
		"shorthand `bootstrap/` single segment", // 不应报：单段目录简写
		"```",                                   // 围栏开始
		"[fenced dead](nope.md) `docs/nope.md`", // 不应报：围栏内
		"```",                                   // 围栏结束
		"[anchored](real.md#section)",           // 不应报：锚点被剥离后存在
		"[anchored dead](gone.md#sec)",          // 应报，且登记键须不含 `#`
		"syntax demo `[文字](路径)` inline",         // 不应报：行内代码里的链接示例，不是链接
	}, "\n")+"\n"); err != nil {
		return err
	}

	// docs/nested/ 下的文档用文档目录相对写法引用 —— 证明两种解析都生效。
	if err := write("docs/nested/n.md", "see `sub/there.md` here\n"); err != nil {
		return err
	}
	if err := write("docs/nested/sub/there.md", "there\n"); err != nil {
		return err
	}

	docs := []string{"docs/probe.md", "docs/nested/n.md"}
	dead, err := findDead(dir, docs)
	if err != nil {
		return fmt.Errorf("FAIL: self-test: %w", err)
	}

	got := map[string]bool{}
	for _, r := range dead {
		got[r.kind+" "+r.text] = true
	}

	wantDead := []string{"link missing.md", "path docs/missing.md", "link gone.md#sec"}
	wantAlive := []string{
		"link real.md", "path docs/real.md", "path scripts/real.sh",
		"path internal/pkg/log.AccessLog()", "path gorm.io/gorm",
		"path cmd/{a,b}.go", "path docs/*.md",
		"path /debug/pprof/", "path ~/pprof/", "path $HOME/pprof/", "path bootstrap/",
		"link nope.md", "path docs/nope.md",
		"link real.md#section", "path sub/there.md",
		"link 路径", // 行内代码里的链接示例不得被当成链接
	}

	var problems []string
	for _, w := range wantDead {
		if !got[w] {
			problems = append(problems, "not detected as dead: "+w)
		}
	}
	for _, w := range wantAlive {
		if got[w] {
			problems = append(problems, "false positive: "+w)
		}
	}
	if len(dead) != len(wantDead) {
		for k := range got {
			unexpected := true
			for _, w := range wantDead {
				if k == w {
					unexpected = false
				}
			}
			if unexpected {
				problems = append(problems, "unexpected finding: "+k)
			}
		}
	}

	// 每一条失败都必须有可登记的出路：登记表以 `#` 起注释，键里带 `#` 的失败
	// 无法被登记，判据就成了一条没有出路的死路。
	for _, r := range dead {
		if strings.Contains(r.allowKey(), "#") {
			problems = append(problems, "allowlist key is not registrable: "+r.allowKey())
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		if _, writeErr := fmt.Println("FAIL: self-test: the judge does not behave as documented"); writeErr != nil {
			return fmt.Errorf("write tool output: %w", writeErr)
		}
		for _, p := range problems {
			if _, writeErr := fmt.Println(" ", p); writeErr != nil {
				return fmt.Errorf("write tool output: %w", writeErr)
			}
		}
		return errors.New("documentation reference self-test failed")
	}

	if _, writeErr := fmt.Printf("doc reference self-test OK (%d dead detected, %d benign forms not flagged, all keys registrable)\n",
		len(wantDead), len(wantAlive)); writeErr != nil {
		return fmt.Errorf("write tool output: %w", writeErr)
	}
	return nil
}
