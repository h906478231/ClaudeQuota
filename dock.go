package main

import (
	"log"
	"math"

	"github.com/egoist/mygo"
)

// Dock 图标默认显示，用的是彩色老虎头：和菜单栏一样按用量从下往上灌色，
// 角标上是菜单栏那串数字。不想占 Dock 的话，右键菜单里能收起来。

// setDockVisible 显示或收起 Dock 图标。在主线程调。
func (a *app) setDockVisible(on bool) {
	a.dockShown.Store(on)
	if !on {
		mygo.App.Dock.Hide()
		return
	}
	mygo.App.Dock.Show()
	// 收起期间没跟着更新，重新亮出来时按当前用量重画一遍。
	a.dockDrawn.Store(-1)
	a.dockBadge = ""
	go a.applyTray()
}

// applyDock 把用量画到 Dock 图标和角标上。任何 goroutine 都能调：
// 画图在调用方这边做，真正改 Dock 交给主线程。
func (a *app) applyDock(acc *accountQuota, mode int) {
	if !a.dockShown.Load() {
		return
	}
	level := int(math.Round(gaugeFraction(acc, mode) * 100))
	badge := menuBarTitle(acc, mode)
	// 画一张 512 像素的图要几十毫秒，用量没变就别重画；
	// 这里看到的值可能稍旧，最坏也只是多画一张。
	var icon []byte
	if level != int(a.dockDrawn.Load()) {
		icon = tigerDockIcon(float64(level) / 100)
	}
	mygo.RunOnMain(func() {
		if !a.dockShown.Load() {
			return
		}
		if icon != nil {
			if err := mygo.App.Dock.SetIcon(icon); err != nil {
				log.Println("Dock 图标：", err)
			} else {
				a.dockDrawn.Store(int32(level))
			}
		}
		if badge != a.dockBadge {
			mygo.App.Dock.SetBadge(badge)
			a.dockBadge = badge
		}
	})
}

// dockMenu 是在 Dock 图标上右键时，系统菜单上面多出来的几项。
func (a *app) dockMenu() *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Label: "打开面板", Click: func(*mygo.MenuItem, *mygo.Window) { a.show() }},
		{Label: "立即刷新", Click: func(*mygo.MenuItem, *mygo.Window) { go a.refresh() }},
		mygo.Separator(),
		{Label: "不在 Dock 显示", Click: func(*mygo.MenuItem, *mygo.Window) { a.setDockVisible(false) }},
	})
}
