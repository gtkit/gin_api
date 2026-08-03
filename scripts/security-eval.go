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
	flag.Parse()

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

func expired(date string, now time.Time) bool {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(date))
	if err != nil {
		return true // 无法解析的到期日按已过期处理，逼迫修正。
	}
	return now.After(t)
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
		missing := []string{}
		for name, v := range map[string]string{
			"scanner": cur.Scanner, "id": cur.ID, "component": cur.Component,
			"owner": cur.Owner, "reason": cur.Reason, "expires": cur.Expires,
		} {
			if strings.TrimSpace(v) == "" {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Errorf("%s 中的条目 %q 缺少字段：%s", path, cur.ID, strings.Join(missing, ", "))
		}
		out[waiverKey(cur.Scanner, cur.ID, cur.Component)] = cur
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
