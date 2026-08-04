//go:build ignore

// security-eval.go —— 统一读取各扫描器的 JSON 产物，按 waiver 判定通过与否。
//
// 贯穿全文的一条原则：**扫描器执行/解析失败 ≠ 没有漏洞**。
// 前者永远 fail-closed 且**不可 waiver**——把它当成"没发现问题"，
// 会让一个坏掉的门禁看起来一直是绿的，那比没有门禁更危险。
// 只有真正被解析出来的 finding 才走 waiver 匹配。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type waiver struct {
	Scanner   string `yaml:"scanner"`
	ID        string `yaml:"id"`
	Component string `yaml:"component"`
	Version   string `yaml:"version"`
	Owner     string `yaml:"owner"`
	Reason    string `yaml:"reason"`
	Expires   string `yaml:"expires"`
}

type finding struct {
	Scanner   string
	ID        string
	Component string
	Detail    string
}

func main() {
	cacheDir := flag.String("cache", ".cache/security", "扫描产物目录")
	waiversPath := flag.String("waivers", "security/waivers.yml", "漏洞豁免登记表")
	secretsPath := flag.String("secret-allowlist", "security/secret-allowlist.yml", "密钥假阳性登记表")
	mode := flag.String("mode", "source", "source | release")
	selfTest := flag.Bool("self-test", false, "自检：在临时 fixture 上验证各判定分支")
	flag.Parse()

	if *selfTest {
		runSelfTest()
		return
	}

	var problems []string

	vulns, err := readGovulncheck(filepath.Join(*cacheDir, "govulncheck.json"))
	if err != nil {
		// 解析失败是门禁自身的故障，不可豁免。
		fatalf("govulncheck 产物无法解析（扫描器错误不可豁免）：%v", err)
	}

	secrets, err := readGitleaks(filepath.Join(*cacheDir, "gitleaks.json"))
	if err != nil {
		fatalf("gitleaks 产物无法解析（扫描器错误不可豁免）：%v", err)
	}
	if *mode == "release" {
		hist, herr := readGitleaks(filepath.Join(*cacheDir, "gitleaks-history.json"))
		if herr != nil {
			fatalf("gitleaks 全历史产物无法解析（扫描器错误不可豁免）：%v", herr)
		}
		secrets = append(secrets, hist...)

		// 镜像漏洞必须参与判定。
		//
		// 产出了 trivy-image.json 却不消费它，等于花时间扫了一遍然后把结果扔掉——
		// 门禁照样打印 OK，而基础镜像里的漏洞一条都没被看见。那比不扫更糟：
		// 不扫至少不会让人以为镜像已经被检查过。
		imageVulns, ierr := readTrivyImage(filepath.Join(*cacheDir, "trivy-image.json"))
		if ierr != nil {
			fatalf("trivy 镜像产物无法解析（扫描器错误不可豁免）：%v", ierr)
		}
		vulns = append(vulns, imageVulns...)
	}

	waivers, err := readWaivers(*waiversPath)
	if err != nil {
		fatalf("%v", err)
	}
	secretAllow, err := readWaivers(*secretsPath)
	if err != nil {
		fatalf("%v", err)
	}

	now := time.Now()

	// 漏洞：逐条按 scanner + id + component 精确匹配 waiver。
	usedWaiver := map[string]bool{}
	for _, f := range vulns {
		key := waiverKey(f.Scanner, f.ID, f.Component)
		w, ok := waivers[key]
		if !ok {
			problems = append(problems, fmt.Sprintf("未豁免的漏洞 %s (%s): %s", f.ID, f.Component, f.Detail))
			continue
		}
		if expired(w.Expires, now) {
			problems = append(problems, fmt.Sprintf("豁免已过期 %s (%s)，到期日 %s，负责人 %s",
				f.ID, f.Component, w.Expires, w.Owner))
			continue
		}
		usedWaiver[key] = true
	}

	// 密钥：真实泄漏不可豁免，只有登记为假阳性的才放行。
	usedAllow := map[string]bool{}
	for _, f := range secrets {
		key := waiverKey("gitleaks", f.ID, f.Component)
		w, ok := secretAllow[key]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"疑似密钥泄漏 %s @ %s —— 真实泄漏必须轮换并清理，只有人工确认的假阳性才能登记",
				f.ID, f.Component))
			continue
		}
		if expired(w.Expires, now) {
			problems = append(problems, fmt.Sprintf("假阳性登记已过期 %s @ %s，到期日 %s",
				f.ID, f.Component, w.Expires))
			continue
		}
		usedAllow[key] = true
	}

	// 陈旧条目：不再命中任何 finding 的登记必须删除。
	// 否则登记表会逐渐变成一份无人清理、越来越长、没人再认真看的清单。
	problems = append(problems, staleEntries(waivers, usedWaiver, "waiver")...)
	problems = append(problems, staleEntries(secretAllow, usedAllow, "secret allowlist")...)

	if len(problems) > 0 {
		sort.Strings(problems)
		fmt.Fprintln(os.Stderr, "[security-eval] FAIL:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  -", p)
		}
		os.Exit(1)
	}
	fmt.Printf("[security-eval] OK：漏洞 %d 条、密钥 %d 条，全部已处理\n", len(vulns), len(secrets))
}

func staleEntries(entries map[string]waiver, used map[string]bool, kind string) []string {
	var out []string
	for key, w := range entries {
		if !used[key] {
			out = append(out, fmt.Sprintf(
				"陈旧的 %s 条目 %s（负责人 %s）已不再命中任何 finding，请删除", kind, key, w.Owner))
		}
	}
	return out
}

func waiverKey(scanner, id, component string) string {
	return strings.ToLower(scanner + "|" + id + "|" + component)
}

// expired 判断到期日是否已过。
//
// **到期日当天仍然有效**，次日零点（UTC）起才算过期。
//
// 曾经写成 `now.After(t)`：t 是当天零点，于是 `expires: 2026-08-04` 在
// 2026-08-04 00:00:01 就失效了——而写下这个日期的人显然认为那一整天都还算数。
// 差一天听起来无关紧要，直到某次发布卡在门禁上，而豁免"明明还没到期"。
//
// 用 UTC 而不是本地时区：同一条豁免在不同时区的机器上必须给出同一个结论，
// 否则 CI 通过而本地失败（或反过来）会变成一桩查不清的悬案。
func expired(date string, now time.Time) bool {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(date))
	if err != nil {
		return true // 无法解析的到期日按已过期处理，逼迫修正。
	}
	return !now.UTC().Before(t.AddDate(0, 0, 1))
}

// readWaivers 读取登记表。
//
// 用极简解析而不是引 YAML 库：登记表的结构是固定的扁平列表，
// 为它增加一个第三方依赖不划算，而依赖越少供应链面越小——
// 这一点对**安全门禁自身**尤其重要。
func readWaivers(path string) (map[string]waiver, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]waiver{}, nil
		}
		return nil, fmt.Errorf("读取 %s：%w", path, err)
	}

	out := map[string]waiver{}
	var cur waiver
	var inItem bool

	flush := func() error {
		if !inItem {
			return nil
		}
		// 字段不全的条目一律失败：没有负责人与到期日的豁免会变成永久豁免。
		required := map[string]string{
			"scanner": cur.Scanner, "id": cur.ID, "component": cur.Component,
			"owner": cur.Owner, "reason": cur.Reason, "expires": cur.Expires,
		}
		// version 只对漏洞豁免必填。
		//
		// 锁版本会让每次依赖升级都需要重新评估豁免——**那个噪音正是想要的效果**：
		// 升级后同一个 CVE 编号在新版本上可能是完全不同的可达路径，旧评估不该
		// 自动延续。看到它吵而改回可选，等于把这条保护删掉。
		//
		// 密钥假阳性针对的是具体位置上的字符串，没有"受影响版本"这个概念。
		if cur.Scanner != "gitleaks" {
			required["version"] = cur.Version
		}

		missing := []string{}
		for name, v := range required {
			if strings.TrimSpace(v) == "" {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Errorf("%s 中的条目 %q 缺少字段：%s", path, cur.ID, strings.Join(missing, ", "))
		}

		// 重复条目直接失败，不静默取其一。
		//
		// 静默覆盖后两条看起来都在生效，实际只有一条有效；若被覆盖的那条到期日
		// 更早，它就不再参与过期检查——一条早该失效的豁免被静默复活。
		key := waiverKey(cur.Scanner, cur.ID, cur.Component)
		if prev, dup := out[key]; dup {
			return fmt.Errorf(
				"%s 中存在重复条目 %s（负责人 %s 与 %s）：重复会让其中一条静默失效，"+
					"包括它的到期日", path, key, prev.Owner, cur.Owner)
		}
		out[key] = cur
		cur = waiver{}
		inItem = false
		return nil
	}

	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			if err := flush(); err != nil {
				return nil, err
			}
			inItem = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if line == "" {
				continue
			}
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "scanner":
			cur.Scanner = value
		case "id":
			cur.ID = value
		case "component":
			cur.Component = value
		case "version":
			cur.Version = value
		case "owner":
			cur.Owner = value
		case "reason":
			cur.Reason = value
		case "expires":
			cur.Expires = value
		case "rule":
			cur.Scanner = "gitleaks"
			cur.ID = value
		case "fingerprint":
			cur.Component = value
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// readGovulncheck 解析 govulncheck 的 JSON 流。
//
// 只统计**真正影响本项目调用链**的 finding：govulncheck 会同时报告
// "依赖里存在该漏洞但你的代码没有调用到"的情形，把后者也算作阻断
// 会产生大量无法处理的噪声，最终逼人整体关掉门禁。
// blockingSeverities 是必须当场处理的镜像漏洞等级。
//
// 只挡 CRITICAL / HIGH：把每一级都设成阻断，结果是豁免表迅速膨胀成一份
// 没人再认真看的清单，而真正紧急的那几条会淹没在里面。低等级与
// UNKNOWN（多为"该包已不再维护"这类提示）记入产物与 SBOM 供审阅，
// 但不阻断发布——它们没有"现在必须做什么"的动作。
var blockingSeverities = map[string]bool{"CRITICAL": true, "HIGH": true}

// readTrivyImage 读出交付镜像里需要处理的漏洞。
//
// component 用 "包名@版本"：同一个 CVE 在不同包上是不同的处置对象，
// 只按 CVE 号豁免会让一条豁免顺带放过另一个包上的同名漏洞。
func readTrivyImage(path string) ([]finding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // source 模式不产出镜像扫描结果。
		}
		return nil, err
	}

	var report struct {
		Results []struct {
			Target          string `json:"Target"`
			Vulnerabilities []struct {
				VulnerabilityID  string `json:"VulnerabilityID"`
				PkgName          string `json:"PkgName"`
				InstalledVersion string `json:"InstalledVersion"`
				FixedVersion     string `json:"FixedVersion"`
				Severity         string `json:"Severity"`
				Title            string `json:"Title"`
			} `json:"Vulnerabilities"`
		} `json:"Results"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("解析 trivy 镜像输出：%w", err)
	}

	var out []finding
	for _, result := range report.Results {
		for _, v := range result.Vulnerabilities {
			if !blockingSeverities[strings.ToUpper(v.Severity)] {
				continue
			}
			detail := fmt.Sprintf("%s %s（%s）", v.Severity, v.Title, result.Target)
			if v.FixedVersion != "" {
				detail += fmt.Sprintf("，已有修复版本 %s", v.FixedVersion)
			}
			out = append(out, finding{
				Scanner:   "trivy",
				ID:        v.VulnerabilityID,
				Component: v.PkgName + "@" + v.InstalledVersion,
				Detail:    detail,
			})
		}
	}
	return out, nil
}

func readGovulncheck(path string) ([]finding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 被显式跳过时没有产物。
		}
		return nil, err
	}

	dec := json.NewDecoder(strings.NewReader(string(data)))
	var out []finding
	for dec.More() {
		var msg struct {
			Finding *struct {
				OSV          string `json:"osv"`
				FixedVersion string `json:"fixed_version"`
				Trace        []struct {
					Module   string `json:"module"`
					Function string `json:"function"`
				} `json:"trace"`
			} `json:"finding"`
		}
		if err := dec.Decode(&msg); err != nil {
			return nil, fmt.Errorf("解析 govulncheck 输出：%w", err)
		}
		if msg.Finding == nil || len(msg.Finding.Trace) == 0 {
			continue
		}
		// 有具体函数的 trace 才说明代码路径可达。
		if msg.Finding.Trace[0].Function == "" {
			continue
		}
		out = append(out, finding{
			Scanner:   "govulncheck",
			ID:        msg.Finding.OSV,
			Component: msg.Finding.Trace[0].Module,
			Detail:    "fixed in " + msg.Finding.FixedVersion,
		})
	}
	return out, nil
}

func readGitleaks(path string) ([]finding, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}

	var raw []struct {
		RuleID      string `json:"RuleID"`
		File        string `json:"File"`
		Fingerprint string `json:"Fingerprint"`
	}
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("解析 gitleaks 输出：%w", err)
	}

	out := make([]finding, 0, len(raw))
	for _, r := range raw {
		component := r.Fingerprint
		if component == "" {
			component = r.File
		}
		out = append(out, finding{
			Scanner: "gitleaks", ID: r.RuleID, Component: component,
			Detail: r.File,
		})
	}
	return out, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[security-eval] FAIL: "+format+"\n", args...)
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// 自检
// ---------------------------------------------------------------------------
// 这是唯一决定"发布能不能过"的判定逻辑，而它此前没有任何测试——
// 一个没被验证过的判定器，它说通过时没人知道那意味着什么。
//
// 在临时目录里造 fixture，因此完全不触碰真实登记表与扫描产物。

func runSelfTest() {
	dir, err := os.MkdirTemp("", "security-eval-selftest")
	if err != nil {
		fatalf("self-test: %v", err)
	}
	defer os.RemoveAll(dir)

	failed := false
	expect := func(name string, got, want any) {
		if fmt.Sprint(got) == fmt.Sprint(want) {
			fmt.Printf("[self-test] OK: %s\n", name)
		} else {
			fmt.Fprintf(os.Stderr, "[self-test] FAIL: %s => got %v, want %v\n", name, got, want)
			failed = true
		}
	}

	writeFile := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			fatalf("self-test write: %v", err)
		}
		return path
	}

	// --- 字段完整性 ---
	full := `waivers:
  - scanner: govulncheck
    id: GO-1
    component: example.com/x
    version: v1.0.0
    owner: someone
    reason: pending upstream fix
    expires: 2099-01-01
`
	if _, err := readWaivers(writeFile("full.yml", full)); err != nil {
		expect("a complete vulnerability waiver parses", err, nil)
	} else {
		expect("a complete vulnerability waiver parses", "ok", "ok")
	}

	// version 缺失必须失败——这条此前完全没有被校验。
	noVersion := strings.Replace(full, "    version: v1.0.0\n", "", 1)
	_, err = readWaivers(writeFile("noversion.yml", noVersion))
	expect("a vulnerability waiver without a version is rejected",
		err != nil && strings.Contains(fmt.Sprint(err), "version"), true)

	noOwner := strings.Replace(full, "    owner: someone\n", "", 1)
	_, err = readWaivers(writeFile("noowner.yml", noOwner))
	expect("a waiver without an owner is rejected",
		err != nil && strings.Contains(fmt.Sprint(err), "owner"), true)

	// 密钥假阳性不要求 version：它针对具体位置的字符串，没有版本概念。
	secretEntry := `allowlist:
  - rule: generic-api-key
    fingerprint: abc:file.go:generic-api-key:1
    owner: someone
    reason: test placeholder
    expires: 2099-01-01
`
	_, err = readWaivers(writeFile("secret.yml", secretEntry))
	expect("a secret allowlist entry needs no version", err, nil)

	// --- 重复检测 ---
	dup := full + `  - scanner: govulncheck
    id: GO-1
    component: example.com/x
    version: v1.0.0
    owner: another
    reason: duplicate
    expires: 2030-01-01
`
	_, err = readWaivers(writeFile("dup.yml", dup))
	expect("a duplicate waiver is rejected instead of silently overwriting",
		err != nil && strings.Contains(fmt.Sprint(err), "重复"), true)

	// --- 过期判定 ---
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	expect("a past date is expired", expired("2026-08-02", now), true)
	expect("today is not expired", expired("2026-08-03", now), false)
	expect("a future date is not expired", expired("2099-01-01", now), false)

	// 边界：到期日当天**整天**都有效，次日零点才失效。
	// 只测零点时刻会漏掉这一条——而差一天正是最容易被忽略、
	// 也最容易在发布当天造成意外的偏差。
	lateSameDay := time.Date(2026, 8, 3, 23, 59, 59, 0, time.UTC)
	expect("the expiry date stays valid all day", expired("2026-08-03", lateSameDay), false)
	nextMidnight := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	expect("expiry takes effect at the next midnight", expired("2026-08-03", nextMidnight), true)

	// 非 UTC 时区的同一时刻必须得到同一结论。
	shanghai := time.FixedZone("CST", 8*3600)
	sameInstant := nextMidnight.In(shanghai)
	expect("the verdict does not depend on the machine timezone",
		expired("2026-08-03", sameInstant), true)

	// --- 陈旧条目 ---
	entries := map[string]waiver{"k1": {ID: "GO-1"}, "k2": {ID: "GO-2"}}
	stale := staleEntries(entries, map[string]bool{"k1": true}, "waiver")
	expect("an unused waiver is reported as stale", len(stale), 1)
	fresh := staleEntries(entries, map[string]bool{"k1": true, "k2": true}, "waiver")
	expect("fully used waivers are not stale", len(fresh), 0)

	// --- 镜像漏洞分级 ---
	image := `{"Results":[{"Target":"t","Vulnerabilities":[
      {"VulnerabilityID":"C-1","PkgName":"a","InstalledVersion":"1","Severity":"CRITICAL"},
      {"VulnerabilityID":"H-1","PkgName":"b","InstalledVersion":"1","Severity":"HIGH"},
      {"VulnerabilityID":"M-1","PkgName":"c","InstalledVersion":"1","Severity":"MEDIUM"},
      {"VulnerabilityID":"U-1","PkgName":"d","InstalledVersion":"1","Severity":"UNKNOWN"}]}]}`
	found, ierr := readTrivyImage(writeFile("trivy.json", image))
	expect("image scan parses", ierr, nil)
	expect("only critical and high block the release", len(found), 2)

	// 缺文件不是错误：source 模式本就不产出镜像扫描结果。
	missing, merr := readTrivyImage(filepath.Join(dir, "absent.json"))
	expect("a missing image report is not an error", merr, nil)
	expect("a missing image report yields no findings", len(missing), 0)

	if failed {
		os.Exit(1)
	}
	fmt.Println("[self-test] OK: every decision branch behaves as specified")
}
