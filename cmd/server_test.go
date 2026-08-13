package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestPrintBannerLayout 锁定横幅的版面契约。
//
// 值得测的不是"打印了东西"，而是三处会悄悄坏掉的地方：字形被截断、行间串色、
// 收边线与字形不等宽。这三样都只在真正启动服务时才看得见，而那时没人会去比对
// 字符数——所以只能靠测试。
func TestPrintBannerLayout(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	printBanner(&out)
	got := out.String()

	artLines := strings.Split(strings.Trim(_UI, "\n"), "\n")

	// 字形完整：每一行都必须原样出现，不能被 trim 或分割逻辑吃掉。
	for i, ln := range artLines {
		if !strings.Contains(got, ln) {
			t.Fatalf("banner line %d missing from output: %q", i+1, ln)
		}
	}

	// 不串色：每一行着色后必须复位。少一个 reset，后续所有终端输出都会
	// 继承 banner 的颜色，而那种故障看起来像"日志颜色不对"，很难联想到横幅。
	wantResets := len(artLines) + 1 // 六行字形 + 一条收边线
	if n := strings.Count(got, bannerReset); n != wantResets {
		t.Fatalf("reset sequence count = %d, want %d (one per colored line)", n, wantResets)
	}

	// 收边线与字形等宽。
	width := 0
	for _, ln := range artLines {
		if n := len([]rune(ln)); n > width {
			width = n
		}
	}
	if !strings.Contains(got, strings.Repeat("─", width)) {
		t.Fatalf("separator rule is not %d cells wide (must match the art width)", width)
	}
	// 反证宽度断言本身有效：多一格就应当匹配不上，否则上面那条 Contains
	// 对任何比它短的线都成立，等于没有校验宽度。
	if strings.Contains(got, strings.Repeat("─", width+1)) {
		t.Fatalf("separator rule is wider than the art (%d cells)", width)
	}
}

// TestBannerPaletteCoversEveryLine 保证色阶取用不会越界。
//
// 调色板与字形行数是两个独立可变的量：换字体会改行数，调配色会改色阶数。
// 打印逻辑按下标取模正是为此，本用例锁定这个性质——否则某次换字体会在
// **启动路径上** panic，而那是最不该崩的地方。
func TestBannerPaletteCoversEveryLine(t *testing.T) {
	t.Parallel()

	if len(bannerPalette) == 0 {
		t.Fatal("bannerPalette must not be empty; printBanner indexes into it")
	}

	artLines := strings.Split(strings.Trim(_UI, "\n"), "\n")
	for i := range artLines {
		if bannerPalette[i%len(bannerPalette)] == "" {
			t.Fatalf("palette entry for line %d is empty", i+1)
		}
	}
}

// TestBannerRuleColorIsOutsideThePalette 锁定「收边线不取色带里的任何一格」。
//
// 这是 bannerRule 注释里写下的声明，因此必须有一个「若声明为假就会失败」的
// 用例：分隔线的作用是收住版面，取了色带里的色就会与字形融成一片，那时它
// 既不分隔也不收边，而这种失效只在真正启动服务时用肉眼才看得出来。
func TestBannerRuleColorIsOutsideThePalette(t *testing.T) {
	t.Parallel()

	for i, c := range bannerPalette {
		if c == bannerRule {
			t.Fatalf("bannerRule equals palette entry %d (%q); the separator must not blend into the art",
				i, c)
		}
	}
}
