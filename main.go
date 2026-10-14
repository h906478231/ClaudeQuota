package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var intervalChoices = []struct {
	Label string
	Secs  int
}{
	{"2 秒", 2},
	{"5 秒", 5},
	{"15 秒", 15},
	{"1 分钟", 60},
}

var labelModes = []string{"最吃紧", "两个", "5 小时", "每周"}

// 账号选择的两个特殊档；>= 0 时是 accounts 里的具体下标。
const (
	// mostConstrainedChoice 跟着「离上限最近」的账号走：
	// 哪个已用百分比最高就显示哪个，不用手动切换。
	mostConstrainedChoice = -1
	// currentChoice 跟着「最近在用」的账号走，也就是你正在用的那个。
	currentChoice = -2
)

const (
	mostConstrainedLabel = "最吃紧的账号"
	currentLabel         = "当前使用的账号"
)

// app 持有界面所显示的状态。跨 goroutine 读的字段走原子量或锁，
// 其余只在主线程上动：托盘回调、菜单回调和视图。
type app struct {
	win  *mygo.Window
	tray *mygo.Tray

	mu       sync.Mutex
	accounts []accountQuota
	failed   string

	// 界面控件直接绑的是下面这几个字段，只在主线程动；
	// 带原子量的是给托盘 goroutine 读的镜像，两边必须一起改。
	labelModeValue   int
	menuAccountValue string
	intervalValue    string

	labelMode   atomic.Int32 // labelModes 的下标
	menuAccount atomic.Int32 // -1 表示「最吃紧的账号」，否则 accounts 的下标
	intervalIdx atomic.Int32 // intervalChoices 的下标

	visible  atomic.Bool
	quitting bool
}

// newApp 建一个带默认值的 app：默认盯最吃紧的账号、每 5 秒读一次。
// 默认值集中在这里，别散落在 main 里，否则换个入口构造就会走样。
func newApp() *app {
	a := &app{
		labelModeValue:   0,
		menuAccountValue: mostConstrainedLabel,
		intervalValue:    intervalChoices[1].Label,
	}
	a.labelMode.Store(0)
	a.menuAccount.Store(mostConstrainedChoice)
	a.intervalIdx.Store(1)
	return a
}

func (a *app) state() ([]accountQuota, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.accounts, a.failed
}

// refresh 重新读一遍 magpie 的额度文件。读的是本地文件，很便宜。
func (a *app) refresh() {
	accounts, err := loadAccounts()
	// 存的就是界面要的顺序：最近在用的在前，其余按周剩余升序，
	// 用尽的沉底。这样下拉框、托盘菜单和列表三处的顺序天然一致。
	orderAccounts(accounts)

	a.mu.Lock()
	if err != nil {
		// 读失败时留着上一次的数据，界面照旧，只在顶部报错。
		a.failed = err.Error()
	} else {
		a.accounts, a.failed = accounts, ""
	}
	a.mu.Unlock()

	a.applyTray()
	a.redraw()
}

// redraw 让窗口重画一帧。窗口还没建好时什么也不做，
// 这样状态变更逻辑可以脱离界面单独驱动和验证。
func (a *app) redraw() {
	if a.win == nil {
		return
	}
	a.win.Invalidate()
}

func (a *app) currentInterval() time.Duration {
	idx := int(a.intervalIdx.Load())
	if idx < 0 || idx >= len(intervalChoices) {
		idx = 1
	}
	return time.Duration(intervalChoices[idx].Secs) * time.Second
}

func (a *app) loop() {
	for {
		a.refresh()
		time.Sleep(a.currentInterval())
	}
}

// tick 每秒重绘一次，让重置倒计时真的在跳；窗口藏着就不做。
func (a *app) tick() {
	for range time.Tick(time.Second) {
		if a.visible.Load() {
			a.redraw()
		}
	}
}

// selectedAccount 返回菜单栏要显示的那个账号。
func (a *app) selectedAccount() *accountQuota {
	a.mu.Lock()
	defer a.mu.Unlock()
	return pickAccount(a.accounts, int(a.menuAccount.Load()))
}

// pickAccount 按选择挑账号。下标 >= 0 是某个具体账号；
// currentChoice 挑最近在用的那个；其余按最吃紧的挑。
func pickAccount(accounts []accountQuota, choice int) *accountQuota {
	if len(accounts) == 0 {
		return nil
	}
	if choice >= 0 && choice < len(accounts) {
		return &accounts[choice]
	}
	if choice == currentChoice {
		if idx := currentAccountIndex(accounts); idx >= 0 {
			return &accounts[idx]
		}
		// 一条使用记录都没有时退回第一个，别让菜单栏空着。
		return &accounts[0]
	}
	return mostConstrained(accounts)
}

// applyTray 更新菜单栏上的数字、圆环和悬停提示。任何 goroutine 都能调。
func (a *app) applyTray() {
	// 没有托盘（比如测试里直接驱动状态）就只更新数据，不碰界面。
	if a.tray == nil {
		return
	}
	mode := int(a.labelMode.Load())
	acc := a.selectedAccount()

	a.tray.SetTitle(menuBarTitle(acc, mode))
	if icon := gaugeIcon(gaugeFraction(acc, mode)); icon != nil {
		if err := a.tray.SetIcon(icon, true); err != nil {
			log.Println("菜单栏图标：", err)
		}
	}

	accounts, failed := a.state()
	switch {
	case failed != "":
		a.tray.SetToolTip("Claude 额度 · " + failed)
	case acc != nil:
		a.tray.SetToolTip(tooltip(acc, mode, len(accounts)))
	default:
		a.tray.SetToolTip("Claude 额度 · 正在读取 magpie 的数据…")
	}
}

func (a *app) toggle() {
	if a.win.IsVisible() {
		a.win.Hide()
		return
	}
	a.show()
}

// show 把面板贴在菜单栏图标下面。
func (a *app) show() {
	anchor := a.tray.Bounds()
	if anchor.Width == 0 || anchor.Height == 0 {
		// 拿不到图标位置时退而求其次，居中显示。
		a.win.Center()
	} else {
		area := mygo.Screen.DisplayNearestPoint(mygo.Point{
			X: anchor.X + anchor.Width/2,
			Y: anchor.Y + anchor.Height/2,
		}).WorkArea
		b := a.win.Bounds()
		x := anchor.X + (anchor.Width-b.Width)/2
		y := anchor.Y + anchor.Height + 6
		if y+b.Height > area.Y+area.Height {
			y = anchor.Y - b.Height - 6
		}
		x = max(area.X, min(x, area.X+max(0, area.Width-b.Width)))
		y = max(area.Y, min(y, area.Y+max(0, area.Height-b.Height)))
		a.win.SetPosition(x, y)
	}
	a.win.Show()
	a.win.Focus()
}

// trayMenu 每次右键现建，勾选状态才跟得上当前选择。
func (a *app) trayMenu() *mygo.Menu {
	accounts, _ := a.state()
	current := int(a.menuAccount.Load())
	mode := int(a.labelMode.Load())
	interval := int(a.intervalIdx.Load())

	check := func(active bool, label string) string {
		if active {
			return "✓ " + label
		}
		return label
	}

	items := []*mygo.MenuItem{
		{Label: "立即刷新", Click: func(*mygo.MenuItem, *mygo.Window) { go a.refresh() }},
		mygo.Separator(),
	}

	split := firstWeeklyFull(accounts)
	if split < 0 {
		split = len(accounts)
	}
	accountItem := func(index int) *mygo.MenuItem {
		i := index
		return &mygo.MenuItem{
			Label: check(current == i, accounts[i].Email),
			Click: func(*mygo.MenuItem, *mygo.Window) { a.setMenuAccount(i) },
		}
	}
	choiceItem := func(choice int, label string) *mygo.MenuItem {
		c := choice
		return &mygo.MenuItem{
			Label: check(current == c, label),
			Click: func(*mygo.MenuItem, *mygo.Window) { a.setMenuAccount(c) },
		}
	}

	// 还能用的账号一个子菜单，两个特殊档排在最前。
	usableItems := []*mygo.MenuItem{
		choiceItem(mostConstrainedChoice, mostConstrainedLabel),
		choiceItem(currentChoice, currentLabel),
	}
	for i := 0; i < split; i++ {
		usableItems = append(usableItems, accountItem(i))
	}
	items = append(items, &mygo.MenuItem{Label: "菜单栏账号", Submenu: usableItems})

	// 本周用尽的另立一个子菜单，不跟还能用的混在一起。
	if split < len(accounts) {
		exhaustedItems := make([]*mygo.MenuItem, 0, len(accounts)-split)
		for i := split; i < len(accounts); i++ {
			exhaustedItems = append(exhaustedItems, accountItem(i))
		}
		items = append(items, &mygo.MenuItem{
			Label:   fmt.Sprintf("本周已用尽（%d）", len(exhaustedItems)),
			Submenu: exhaustedItems,
		})
	}

	modeItems := make([]*mygo.MenuItem, 0, len(labelModes))
	for i, name := range labelModes {
		index, label := i, name
		modeItems = append(modeItems, &mygo.MenuItem{
			Label: check(mode == index, label),
			Click: func(*mygo.MenuItem, *mygo.Window) { a.setLabelMode(index) },
		})
	}
	items = append(items, &mygo.MenuItem{Label: "菜单栏显示", Submenu: modeItems})

	intervalItems := make([]*mygo.MenuItem, 0, len(intervalChoices))
	for i, choice := range intervalChoices {
		index, label := i, choice.Label
		intervalItems = append(intervalItems, &mygo.MenuItem{
			Label: check(interval == index, label),
			Click: func(*mygo.MenuItem, *mygo.Window) { a.setInterval(index) },
		})
	}
	items = append(items, &mygo.MenuItem{Label: "刷新间隔", Submenu: intervalItems})

	items = append(items, mygo.Separator(), &mygo.MenuItem{Role: mygo.RoleQuit})
	return mygo.NewMenu(items)
}

func (a *app) setMenuAccount(choice int) {
	a.menuAccount.Store(int32(choice))
	a.menuAccountValue = a.choiceLabel(choice)
	a.applyTray()
	a.redraw()
}

// choiceLabel 把内部选择翻成下拉框里显示的那一项。
func (a *app) choiceLabel(choice int) string {
	accounts, _ := a.state()
	if choice >= 0 && choice < len(accounts) {
		return accounts[choice].Email
	}
	if choice == currentChoice {
		return currentLabel
	}
	return mostConstrainedLabel
}

func (a *app) setLabelMode(index int) {
	if index < 0 || index >= len(labelModes) {
		return
	}
	a.labelModeValue = index
	a.labelMode.Store(int32(index))
	a.applyTray()
	a.redraw()
}

func (a *app) setInterval(index int) {
	if index < 0 || index >= len(intervalChoices) {
		return
	}
	a.intervalValue = intervalChoices[index].Label
	a.intervalIdx.Store(int32(index))
	a.redraw()
}

func (a *app) ready() {
	var err error
	a.tray, err = mygo.NewTray(mygo.TrayOptions{
		Icon:           gaugeIcon(0),
		IconIsTemplate: true,
		ToolTip:        "Claude 套餐额度",
	})
	if err != nil {
		log.Fatal("托盘图标：", err)
	}

	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:             "Claude 套餐额度",
		Width:             360,
		Height:            680,
		Hidden:            true,
		Frameless:         true,
		DisableResize:     true,
		DisableMinimize:   true,
		DisableMaximize:   true,
		DisableFullScreen: true,
		AlwaysOnTop:       true,
		SkipTaskbar:       true,
		Content:           ui.View(a.view),
	})
	// 面板像弹出层一样，失焦就收起；自动弹窗模式下不收起，
	// 好让人来得及真正看见它。
	if os.Getenv("CQ_SHOW_ON_START") == "" {
		a.win.OnBlur(a.win.Hide)
	}
	a.win.OnShow(func() {
		a.visible.Store(true)
		go a.refresh()
	})
	a.win.OnHide(func() { a.visible.Store(false) })
	a.win.OnClose(func(e *mygo.CloseEvent) {
		// Cmd+W 只是收起面板，真正的退出走 App.Quit。
		if !a.quitting {
			e.PreventDefault()
			a.win.Hide()
		}
	})

	a.tray.OnClick(a.toggle)
	a.tray.OnRightClick(func() {
		a.win.Hide()
		// 只为这次弹出装上菜单，弹完撤掉，主键点击才能继续只是开合面板。
		a.tray.SetMenu(a.trayMenu())
		defer a.tray.SetMenu(nil)
		a.tray.PopUpMenu()
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { a.tray.Destroy() })

	go a.loop()
	go a.tick()

	// 设 CQ_SHOW_ON_START=1 时，启动后直接把面板弹出来，
	// 方便在点不动状态栏项的环境里先看一眼。默认不开。
	if os.Getenv("CQ_SHOW_ON_START") != "" {
		go func() {
			time.Sleep(3 * time.Second)
			a.show()
			time.Sleep(1500 * time.Millisecond)

			icon := a.tray.Bounds()
			panel := a.win.Bounds()
			accounts, failed := a.state()
			log.Printf("菜单栏图标 rect={x:%d y:%d w:%d h:%d}",
				icon.X, icon.Y, icon.Width, icon.Height)
			log.Printf("面板窗口 visible=%v rect={x:%d y:%d w:%d h:%d}",
				a.win.IsVisible(), panel.X, panel.Y, panel.Width, panel.Height)
			log.Printf("读到 %d 个 Claude 账号，failed=%q", len(accounts), failed)
			// 设 CQ_CAPTURE=路径 时把面板导成 PNG，用于确认渲染。
			if path := os.Getenv("CQ_CAPTURE"); path != "" {
				if data, err := a.win.CapturePage(); err != nil {
					log.Println("导出面板失败：", err)
				} else if err := os.WriteFile(path, data, 0o644); err != nil {
					log.Println("写图失败：", err)
				} else {
					log.Printf("已把面板存成 %s（%d 字节）", path, len(data))
				}
			}

			// 盯一段时间，只在数据真的变新时打一行：
			// 既证明面板一直开着，也证明 magpie 一重写文件这边就跟上。
			newest := func() time.Time {
				latest := time.Time{}
				list, _ := a.state()
				for _, acc := range list {
					if acc.ReadAt.After(latest) {
						latest = acc.ReadAt
					}
				}
				return latest
			}
			last := newest()
			log.Printf("起观察：面板 visible=%v，最新一份数据 %s",
				a.win.IsVisible(), ageText(last, time.Now()))
			for deadline := time.Now().Add(6 * time.Minute); time.Now().Before(deadline); {
				time.Sleep(5 * time.Second)
				if latest := newest(); latest.After(last) {
					last = latest
					log.Printf("数据跟着更新到 %s（面板 visible=%v）",
						ageText(latest, time.Now()), a.win.IsVisible())
				}
			}
		}()
	}
}

// ---------- 菜单栏内容 ----------

func percentText(w *quotaWindow) string {
	if w == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", w.Used)
}

func menuBarTitle(acc *accountQuota, mode int) string {
	if acc == nil {
		return ""
	}
	switch mode {
	case 1:
		return fmt.Sprintf("%s·%s", percentText(acc.session()), percentText(acc.weekly()))
	case 2:
		return percentText(acc.session())
	case 3:
		return percentText(acc.weekly())
	}
	return percentText(acc.mostUsed())
}

func gaugeFraction(acc *accountQuota, mode int) float64 {
	if acc == nil {
		return 0
	}
	var w *quotaWindow
	switch mode {
	case 2:
		w = acc.session()
	case 3:
		w = acc.weekly()
	default:
		w = acc.mostUsed()
	}
	if w == nil {
		return 0
	}
	return math.Min(1, math.Max(0, w.Used/100))
}

func tooltip(acc *accountQuota, mode int, total int) string {
	if acc == nil {
		return "Claude 套餐额度"
	}
	text := fmt.Sprintf("Claude %s · %s", acc.Plan, acc.Email)
	if w := acc.session(); w != nil {
		text += fmt.Sprintf(" · 5 小时 %s", percentText(w))
	}
	if w := acc.weekly(); w != nil {
		text += fmt.Sprintf(" · 每周 %s", percentText(w))
	}
	text += fmt.Sprintf(" · 数据 %s", ageText(acc.ReadAt, time.Now()))
	if total > 1 {
		text += fmt.Sprintf("（共 %d 个账号）", total)
	}
	return text
}

// gaugeIcon 画一个圆环，已用的部分填实，作模板图交给 macOS 上色。
func gaugeIcon(fraction float64) []byte {
	const size = 32
	const rOuter, rInner = 15.0, 9.5

	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2
	start := -math.Pi / 2 // 从 12 点方向顺时针填
	sweep := 2 * math.Pi * math.Min(1, math.Max(0, fraction))

	for y := range size {
		for x := range size {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			if r := math.Hypot(dx, dy); r < rInner || r > rOuter {
				continue
			}
			rel := math.Atan2(dy, dx) - start
			for rel < 0 {
				rel += 2 * math.Pi
			}
			alpha := uint8(64) // 空环淡一点，看得出总刻度
			if rel <= sweep {
				alpha = 255
			}
			img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: alpha})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Println("生成菜单栏图标：", err)
		return nil
	}
	return buf.Bytes()
}

func main() {
	a := newApp()

	mygo.App.SetName("Claude 额度")
	// 只在菜单栏待着，不占 Dock。
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.quitting = true })
	mygo.App.WhenReady(a.ready)
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
