package main

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"testing"
)

// TestTigerDockIconFills Dock 上的彩色老虎头也是量杯：用得越多，
// 带颜色（没褪成灰）的像素越多。
func TestTigerDockIconFills(t *testing.T) {
	colored := func(f float64) int {
		img, err := png.Decode(bytes.NewReader(tigerDockIcon(f)))
		if err != nil {
			t.Fatal(err)
		}
		if b := img.Bounds(); b.Dx() != dockIconSize || b.Dy() != dockIconSize {
			t.Fatalf("Dock 图标应当是 %d 见方，得到 %v", dockIconSize, b)
		}
		return countColored(img)
	}
	empty, half, full := colored(0), colored(0.5), colored(1)
	if !(empty < half && half < full) {
		t.Errorf("带颜色的像素应随用量增加：0%%=%d 50%%=%d 100%%=%d", empty, half, full)
	}
	if empty != 0 {
		t.Errorf("一点没用时老虎应当全是灰的，却有 %d 个带颜色的像素", empty)
	}
}

// TestTigerDockIconMargin 四周要留边，否则在 Dock 里比别的图标大一圈。
func TestTigerDockIconMargin(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(tigerDockIcon(1)))
	if err != nil {
		t.Fatal(err)
	}
	margin := dockIconSize / 16
	for i := 0; i < dockIconSize; i++ {
		for _, p := range []image.Point{{i, margin}, {margin, i}, {i, dockIconSize - 1 - margin}, {dockIconSize - 1 - margin, i}} {
			if _, _, _, a := img.At(p.X, p.Y).RGBA(); a != 0 {
				t.Fatalf("边上 %v 不透明，图像顶到了边", p)
			}
		}
	}
}

// TestAppIconUpToDate 仓库里的 icon.png 必须是 tigerAppIcon 画出来的那张，
// 改了老虎的画法却忘了重新生成，打出来的 .app 就会和 Dock 上的对不上。
// 用 UPDATE_ICON=1 go test -run TestAppIconUpToDate 重新生成。
func TestAppIconUpToDate(t *testing.T) {
	want := tigerAppIcon()
	if os.Getenv("UPDATE_ICON") != "" {
		if err := os.WriteFile("icon.png", want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile("icon.png")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("icon.png 不是最新的，用 UPDATE_ICON=1 go test -run TestAppIconUpToDate 重新生成")
	}
	img, err := png.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 1024 || b.Dy() != 1024 {
		t.Errorf("应用图标应当是 1024 见方，得到 %v", b)
	}
}

// TestTrayMenuDockToggle 托盘右键菜单里有 Dock 开关，勾选跟着当前状态走。
func TestTrayMenuDockToggle(t *testing.T) {
	a := newApp()
	find := func() string {
		for _, item := range a.trayMenu().Items() {
			if item.Label == "在 Dock 显示" || item.Label == "✓ 在 Dock 显示" {
				return item.Label
			}
		}
		t.Fatal("托盘菜单里没有 Dock 开关")
		return ""
	}
	if got := find(); got != "✓ 在 Dock 显示" {
		t.Errorf("默认应当显示在 Dock，菜单项却是 %q", got)
	}
	a.dockShown.Store(false)
	if got := find(); got != "在 Dock 显示" {
		t.Errorf("收起 Dock 后菜单项应当不打勾，却是 %q", got)
	}
}

// countColored 数一数明显带颜色、不是灰的像素。
func countColored(img image.Image) int {
	n := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a > 0x8000 && int(r)-int(bl) > 0x4000 && r >= g {
				n++
			}
		}
	}
	return n
}
