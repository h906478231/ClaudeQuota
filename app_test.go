package main

import (
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 这一组是验收测试：对着真实的 magpie 数据跑，检查程序的行为对不对。
// 没有 magpie 数据时跳过，不算失败。

func realAccounts(t *testing.T) []accountQuota {
	t.Helper()
	if dir := magpieDir(); dir == "" {
		t.Skip("拿不到用户目录")
	} else if _, err := os.Stat(filepath.Join(dir, "quotas.json")); err != nil {
		t.Skip("这台机器上没有 magpie 的额度文件")
	}
	accounts, err := loadAccounts()
	if err != nil {
		t.Fatalf("读 magpie 数据失败：%v", err)
	}
	if len(accounts) == 0 {
		t.Fatal("一个账号都没读到")
	}
	return accounts
}

// rawWindows 用一条和 loadAccounts 完全不同的路径重新解析原始文件：
// 走 map[string]any，靠字段名硬找，避免和被测代码犯同一个错。
func rawWindows(t *testing.T) map[string]map[string]float64 {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(magpieDir(), "quotas.json"))
	if err != nil {
		t.Fatalf("读原始文件失败：%v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("解析原始文件失败：%v", err)
	}

	// 登记表里已经删掉的账号，程序不会显示，这里也要同样跳过，
	// 否则两边比对的就不是同一份东西了。
	live := map[string]bool{}
	if logins, err := os.ReadFile(filepath.Join(magpieDir(), "logins.json")); err == nil {
		var records []map[string]any
		if json.Unmarshal(logins, &records) == nil {
			for _, record := range records {
				if record["agent"] == "claude" {
					if user, ok := record["user"].(string); ok && user != "" {
						live[user] = true
					}
				}
			}
		}
	}

	out := map[string]map[string]float64{}
	for key, value := range root {
		email, ok := strings.CutPrefix(key, "claude/")
		if !ok {
			continue
		}
		if len(live) > 0 && !live[email] {
			continue
		}
		entry, _ := value.(map[string]any)
		quota, _ := entry["quota"].(map[string]any)
		windows, _ := quota["windows"].([]any)
		if len(windows) == 0 {
			continue
		}
		got := map[string]float64{}
		for _, raw := range windows {
			w, _ := raw.(map[string]any)
			name, _ := w["name"].(string)
			used, _ := w["used"].(float64)
			got[strings.ToLower(name)] = used
		}
		out[email] = got
	}
	return out
}

// TestAcceptDataMatchesRawFile 是验收的核心：程序读出来的每个数字，
// 都要能被另一条独立路径从原始文件里重新算出来。
func TestAcceptDataMatchesRawFile(t *testing.T) {
	accounts := realAccounts(t)
	raw := rawWindows(t)

	if len(accounts) != len(raw) {
		t.Fatalf("账号数对不上：程序读到 %d 个，原始文件里有 %d 个", len(accounts), len(raw))
	}

	for _, acc := range accounts {
		windows, ok := raw[acc.Email]
		if !ok {
			t.Errorf("程序多出了原始文件里没有的账号 %s", acc.Email)
			continue
		}
		if len(acc.Windows) != len(windows) {
			t.Errorf("%s 的窗口数对不上：%d vs %d", acc.Email, len(acc.Windows), len(windows))
		}
		for _, w := range acc.Windows {
			// 程序把 "5 hours" 归一成 5 小时窗口，两边按同一归属比对。
			var want float64
			var found bool
			for rawName, used := range windows {
				if w.isSession() && strings.Contains(rawName, "5") {
					want, found = used, true
				}
				if !w.isSession() && strings.Contains(rawName, "7") {
					want, found = used, true
				}
			}
			if !found {
				t.Errorf("%s 的窗口 %q 在原始文件里找不到对应项", acc.Email, w.Name)
				continue
			}
			if w.Used != want {
				t.Errorf("%s %s：程序读到 %.0f%%，原始文件是 %.0f%%", acc.Email, w.title(), w.Used, want)
			}
		}
	}
	t.Logf("逐账号逐窗口比对通过，共 %d 个账号", len(accounts))
}

// TestAcceptFreshnessOrdering 检查最近读到过的排在前面。
func TestAcceptFreshnessOrdering(t *testing.T) {
	accounts := realAccounts(t)
	for i := 1; i < len(accounts); i++ {
		if accounts[i-1].ReadAt.Before(accounts[i].ReadAt) {
			t.Errorf("排序不对：第 %d 个 %s 比第 %d 个 %s 更旧",
				i-1, accounts[i-1].Email, i, accounts[i].Email)
		}
	}
	for _, acc := range accounts {
		if acc.ReadAt.IsZero() {
			t.Errorf("%s 没有读到时间戳", acc.Email)
		}
	}
}

// TestAcceptMenuBarModes 四档显示都要能出东西，格式要对。
func TestAcceptMenuBarModes(t *testing.T) {
	acc := realAccounts(t)[0]

	for mode := 0; mode < len(labelModes); mode++ {
		title := menuBarTitle(&acc, mode)
		if title == "" {
			t.Errorf("第 %d 档（%s）菜单栏标题是空的", mode, labelModes[mode])
			continue
		}
		// 正常形态是 "59%" 或 "0%·59%"，单条窗口缺失时才是 "—"。
		for _, part := range strings.Split(title, "·") {
			if part == "—" {
				continue
			}
			if !strings.HasSuffix(part, "%") {
				t.Errorf("第 %d 档标题 %q 里的 %q 不像百分比", mode, title, part)
			}
		}
		t.Logf("  %-6s -> %q", labelModes[mode], title)
	}
}

// TestAcceptGaugeMatchesTitle 菜单栏圆环填充比例必须和数字一致，
// 否则图标和文字会各说各话。
func TestAcceptGaugeModes(t *testing.T) {
	acc := realAccounts(t)[0]

	for mode := 0; mode < len(labelModes); mode++ {
		title := menuBarTitle(&acc, mode)
		fraction := gaugeFraction(&acc, mode)

		if fraction < 0 || fraction > 1 {
			t.Errorf("第 %d 档圆环比例 %v 越界", mode, fraction)
		}
		if strings.Contains(title, "·") {
			continue // 「两个」档显示两条，本来就对不上一个比例
		}
		if title == "—" {
			if fraction != 0 {
				t.Errorf("第 %d 档没有数据，圆环却是 %v", mode, fraction)
			}
			continue
		}
		want, err := strconv.ParseFloat(strings.TrimSuffix(title, "%"), 64)
		if err != nil {
			t.Errorf("第 %d 档标题 %q 解析失败：%v", mode, title, err)
			continue
		}
		if got := fraction * 100; got < want-0.5 || got > want+0.5 {
			t.Errorf("第 %d 档数字 %v%% 与圆环 %v%% 对不上", mode, want, got)
		}
	}
}

// TestAcceptAdjustableInterval 刷新间隔四档都要落到实处。
func TestAcceptAdjustableInterval(t *testing.T) {
	a := newApp()
	a.intervalIdx.Store(1)

	for i, choice := range intervalChoices {
		a.setInterval(i)
		want := time.Duration(choice.Secs) * time.Second
		if got := a.currentInterval(); got != want {
			t.Errorf("第 %d 档（%s）：间隔 = %v, 期望 %v", i, choice.Label, got, want)
		}
		if got := a.intervalLabel(); got != choice.Label {
			t.Errorf("第 %d 档标签 = %q, 期望 %q", i, got, choice.Label)
		}
	}

	// 越界的下标不该被接受，状态要保持在上一个有效档。
	a.setInterval(3)
	a.setInterval(99)
	if got := a.intervalIdx.Load(); got != 3 {
		t.Errorf("越界下标被接受了，档位 = %d", got)
	}
	if got := a.currentInterval(); got != time.Minute {
		t.Errorf("越界后间隔 = %v, 期望仍是 1 分钟", got)
	}
	// 万一原子量被写坏了，读的时候也要有兜底。
	a.intervalIdx.Store(99)
	if got := a.currentInterval(); got != 5*time.Second {
		t.Errorf("原子量被写坏时应当退回 5 秒，得到 %v", got)
	}
}

// TestAcceptAccountSelection 「当前使用的账号」和指定账号都要切得动。
func TestAcceptAccountSelection(t *testing.T) {
	accounts := realAccounts(t)

	a := newApp()
	a.mu.Lock()
	a.accounts = accounts
	a.mu.Unlock()

	// 默认是「当前使用的账号」。
	if got := a.accountChoice(); got != currentLabel {
		t.Errorf("默认选择 = %q, 期望 %q", got, currentLabel)
	}

	acc := a.selectedAccount()
	if acc == nil {
		t.Fatal("默认档没选出账号")
	}
	// 默认档应当挑最近在用的那个；一条使用记录都没有时退回第一个。
	want := accounts[0].Email
	if idx := currentAccountIndex(accounts); idx >= 0 {
		want = accounts[idx].Email
	}
	if acc.Email != want {
		t.Errorf("默认挑中的是 %s，期望最近在用的 %s", acc.Email, want)
	}

	// 逐个指定账号。
	for i, want := range accounts {
		a.setMenuAccount(i)
		if got := a.accountChoice(); got != want.Email {
			t.Errorf("指定第 %d 个：选择 = %q, 期望 %q", i, got, want.Email)
		}
		if got := a.selectedAccount(); got == nil || got.Email != want.Email {
			t.Errorf("指定第 %d 个：selectedAccount 没跟上", i)
		}
	}

	// 从下拉框回来的字符串也要能映射回下标。
	a.setMenuAccount(currentChoice)
	a.chooseAccount(accounts[0].Email, accounts)
	if got := a.accountChoice(); got != accounts[0].Email {
		t.Errorf("按名字选择失败，得到 %q", got)
	}
	a.chooseAccount(currentLabel, accounts)
	if got := a.accountChoice(); got != currentLabel {
		t.Errorf("切回当前账号失败，得到 %q", got)
	}
	// 不认识的字符串不该把选择弄坏。
	a.chooseAccount("nobody@example.com", accounts)
	if got := a.accountChoice(); got != currentLabel {
		t.Errorf("未知名字应当保持原选择，得到 %q", got)
	}
}

// TestAcceptManualRefresh 手动刷新这条路要真的能把数据换进来。
func TestAcceptManualRefresh(t *testing.T) {
	realAccounts(t) // 没有数据就跳过

	a := newApp()
	if accounts, _ := a.state(); len(accounts) != 0 {
		t.Fatal("新起的 app 不该已经有数据")
	}

	a.refresh() // 界面为空时也不能崩，这一条本身就是验收点

	accounts, failed := a.state()
	if failed != "" {
		t.Fatalf("刷新报错：%s", failed)
	}
	if len(accounts) == 0 {
		t.Fatal("刷新之后仍然没有账号")
	}
	t.Logf("刷新后拿到 %d 个账号", len(accounts))

	// 再刷一次不该出问题，数据也该保持。
	a.refresh()
	again, _ := a.state()
	if len(again) != len(accounts) {
		t.Errorf("二次刷新账号数变了：%d -> %d", len(accounts), len(again))
	}
}

// TestAcceptStaleMarking 久未更新的账号要被标成过期，好沉到底部并置灰。
func TestAcceptStaleMarking(t *testing.T) {
	accounts := realAccounts(t)
	now := time.Now()

	var fresh, stale []string
	for _, acc := range accounts {
		if acc.stale(now) {
			stale = append(stale, fmt.Sprintf("%s（%s）", acc.Email, ageText(acc.ReadAt, now)))
		} else {
			fresh = append(fresh, fmt.Sprintf("%s（%s）", acc.Email, ageText(acc.ReadAt, now)))
		}
	}
	t.Logf("新鲜 %d 个：%s", len(fresh), strings.Join(fresh, ", "))
	t.Logf("过期 %d 个：%s", len(stale), strings.Join(stale, ", "))

	// 过期的一定排在新鲜的后头。
	firstStale := len(accounts)
	for i, acc := range accounts {
		if acc.stale(now) {
			firstStale = i
			break
		}
	}
	for i := firstStale; i < len(accounts); i++ {
		if !accounts[i].stale(now) {
			t.Errorf("第 %d 个 %s 是新鲜的，却排在了过期账号后面", i, accounts[i].Email)
		}
	}
}

// TestAcceptPicksUpFileChanges 把真实的文件复制到临时目录再改一改，
// 验证读路径确实跟着文件走，而不是拿着一份旧数据不放。
func TestAcceptPicksUpFileChanges(t *testing.T) {
	real := magpieDir()
	if real == "" {
		t.Skip("拿不到用户目录")
	}
	data, err := os.ReadFile(filepath.Join(real, "quotas.json"))
	if err != nil {
		t.Skip("这台机器上没有 magpie 的额度文件")
	}

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("解析原始文件失败：%v", err)
	}

	var target string
	for key := range root {
		if strings.HasPrefix(key, "claude/") {
			target = key
			break
		}
	}
	if target == "" {
		t.Skip("原始文件里没有 claude 账号")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "quotas.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := loadAccountsFrom(dir)
	if err != nil {
		t.Fatalf("首次读取失败：%v", err)
	}

	// 改掉目标账号第一条窗口的用量，并把读取时间推后。
	entry, _ := root[target].(map[string]any)
	quota, _ := entry["quota"].(map[string]any)
	windows, _ := quota["windows"].([]any)
	if len(windows) == 0 {
		t.Skip("目标账号没有额度窗口")
	}
	first, _ := windows[0].(map[string]any)
	original, _ := first["used"].(float64)
	first["used"] = original + 42
	quota["readAt"] = time.Now().UTC().Format(time.RFC3339Nano)

	updated, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "quotas.json"), updated, 0o600); err != nil {
		t.Fatal(err)
	}

	after, err := loadAccountsFrom(dir)
	if err != nil {
		t.Fatalf("再次读取失败：%v", err)
	}

	email := strings.TrimPrefix(target, "claude/")
	var was, is *accountQuota
	for i := range before {
		if before[i].Email == email {
			was = &before[i]
		}
	}
	for i := range after {
		if after[i].Email == email {
			is = &after[i]
		}
	}
	if was == nil || is == nil {
		t.Fatalf("改动前后都应当有 %s", email)
	}
	if was.Windows[0].Used != original {
		t.Errorf("改动前读到 %.0f%%，期望 %.0f%%", was.Windows[0].Used, original)
	}
	if is.Windows[0].Used != original+42 {
		t.Errorf("改动后读到 %.0f%%，期望 %.0f%% —— 读路径没跟着文件走",
			is.Windows[0].Used, original+42)
	}
	if !is.ReadAt.After(was.ReadAt) {
		t.Error("读取时间没有跟着更新")
	}
	t.Logf("%s 的%s：改动前 %.0f%% → 改动后 %.0f%%",
		email, is.Windows[0].title(), original, original+42)
}

func TestAgeTextShort(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, "从未读到"},
		{now.Add(-30 * time.Second), "30 秒前"},
		{now.Add(-5 * time.Minute), "5 分钟前"},
		{now.Add(-3 * time.Hour), "3 小时前"},
		{now.Add(-48 * time.Hour), "2 天前"},
	}
	for _, c := range cases {
		if got := ageTextShort(c.at, now); got != c.want {
			t.Errorf("ageTextShort(%v) = %q, 期望 %q", c.at, got, c.want)
		}
	}
}

// 下面几条用 mygo 的无窗口测试器跑真实界面：
// 它按文字找到控件，真的点下去，所以「点一下 → 状态变」这条链路是真被测到的，
// 不是靠读代码推断。

func readyApp(t *testing.T) (*app, []accountQuota) {
	t.Helper()
	realAccounts(t)

	a := newApp()
	a.refresh()
	loaded, failed := a.state()
	if failed != "" {
		t.Fatalf("载入真实数据失败：%s", failed)
	}
	if len(loaded) == 0 {
		t.Fatal("没有载入任何账号")
	}
	return a, loaded
}

// TestAcceptPanelRenders 每个账号和它的数字都要真的画出来。
func TestAcceptPanelRenders(t *testing.T) {
	a, accounts := readyApp(t)
	tt := ui.NewTester(a.view, 360, 520)

	for _, acc := range accounts {
		if !tt.HasText(acc.Email) {
			t.Errorf("面板里没画出账号 %s", acc.Email)
		}
	}
	for _, text := range []string{
		"Claude 套餐额度",
		fmt.Sprintf("%d 个账号", len(accounts)),
		"菜单栏显示", "监控账号", "刷新间隔", "退出",
	} {
		if !tt.HasText(text) {
			t.Errorf("面板里缺了 %q", text)
		}
	}
	t.Logf("面板画出了 %d 个账号", len(accounts))
}

// TestAcceptPanelShowsNumbers 界面上写的百分比要和数据一致。
func TestAcceptPanelShowsNumbers(t *testing.T) {
	a, accounts := readyApp(t)
	tt := ui.NewTester(a.view, 360, 520)

	// 第一个账号的两条窗口数字都要出现在面板上。
	first := accounts[0]
	for _, w := range first.Windows {
		want := fmt.Sprintf("%.0f%%", w.Used)
		if !tt.HasText(want) {
			t.Errorf("%s 的%s 应当显示 %s，面板上没有", first.Email, w.title(), want)
		}
	}
}

// TestAcceptPanelSwitchesLabelMode 点菜单栏显示的分段控件，状态要跟着变。
func TestAcceptPanelSwitchesLabelMode(t *testing.T) {
	a, _ := readyApp(t)

	if got := int(a.labelMode.Load()); got != 0 {
		t.Fatalf("初始档位 = %d, 期望 0（最吃紧）", got)
	}

	tt := ui.NewTester(a.view, 360, 520)
	for _, target := range []struct {
		text string
		want int
	}{
		{"每周", 3},
		{"5 小时", 2},
		{"两个", 1},
		{"最吃紧", 0},
	} {
		if err := tt.Click(target.text); err != nil {
			t.Fatalf("点不到 %q：%v", target.text, err)
		}
		if got := int(a.labelMode.Load()); got != target.want {
			t.Errorf("点 %q 之后档位 = %d, 期望 %d", target.text, got, target.want)
		}
	}
}

// TestAcceptPanelRefreshButton 点「刷新」要真的重新读一遍数据。
func TestAcceptPanelRefreshButton(t *testing.T) {
	a, accounts := readyApp(t)
	tt := ui.NewTester(a.view, 360, 520)

	if err := tt.Click("刷新"); err != nil {
		t.Fatalf("点不到「刷新」：%v", err)
	}

	// 刷新是丢到 goroutine 里跑的，等它回来。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if list, failed := a.state(); failed == "" && len(list) == len(accounts) {
			t.Logf("点「刷新」后仍然读到 %d 个账号", len(list))
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("点了「刷新」之后等不到数据")
}

// TestAcceptPanelIntervalSelect 刷新间隔的下拉要能选动。
func TestAcceptPanelIntervalSelect(t *testing.T) {
	a, _ := readyApp(t)
	tt := ui.NewTester(a.view, 360, 520)

	if got := a.intervalLabel(); got != "5 秒" {
		t.Fatalf("初始间隔 = %q, 期望 5 秒", got)
	}
	if err := tt.Click("5 秒"); err != nil {
		t.Fatalf("点不开间隔下拉：%v", err)
	}
	// 选项是渲染在下面、可以直接点的一行行文字。
	if err := tt.Click("15 秒"); err != nil {
		t.Fatalf("点不到「15 秒」选项：%v", err)
	}
	if got := a.intervalLabel(); got != "15 秒" {
		t.Errorf("选完之后间隔 = %q, 期望 15 秒", got)
	}
	if got := a.currentInterval(); got != 15*time.Second {
		t.Errorf("轮询间隔 = %v, 期望 15 秒", got)
	}
}

// TestAcceptPanelAccountSelect 账号下拉要能真的切到某个账号。
// 这里走键盘：选项文字和上面的账号标题一模一样，按文字点击会打到标题上。
func TestAcceptPanelAccountSelect(t *testing.T) {
	a, accounts := readyApp(t)
	tt := ui.NewTester(a.view, 360, 520)

	if got := a.accountChoice(); got != currentLabel {
		t.Fatalf("初始账号选择 = %q, 期望 %q", got, currentLabel)
	}

	target := accounts[len(accounts)-1].Email

	// 点开下拉（这一项的文字是唯一的），End 跳到最后一个账号，回车选中。
	if err := tt.Click(currentLabel); err != nil {
		t.Fatalf("点不开账号下拉：%v", err)
	}
	tt.Key(0, ui.KeyEnd)
	tt.Key(0, ui.KeyEnter)

	if got := a.accountChoice(); got != target {
		t.Errorf("切换后账号 = %q, 期望 %q", got, target)
	}
	if acc := a.selectedAccount(); acc == nil || acc.Email != target {
		t.Errorf("selectedAccount 没跟上")
	}

	// 焦点还在下拉上：方向键重新打开，Home 回到第一项「当前使用的账号」。
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyHome)
	tt.Key(0, ui.KeyEnter)
	if got := a.accountChoice(); got != currentLabel {
		t.Errorf("切回当前账号后 = %q, 期望 %q", got, currentLabel)
	}
}

// TestAcceptPanelHasOneOfEachControl 底部每个控件只能有一个。
// 紧排版时挪控件很容易留下重复的一份，这条把这类疏漏锁住。
func TestAcceptPanelHasOneOfEachControl(t *testing.T) {
	a, _ := readyApp(t)
	tt := ui.NewTester(a.view, 360, 520)

	counts := map[string]int{}
	for _, text := range tt.Texts() {
		counts[text]++
	}
	for _, want := range []string{"退出", "刷新", "菜单栏显示", "监控账号", "刷新间隔"} {
		if got := counts[want]; got != 1 {
			t.Errorf("「%s」出现了 %d 次，期望 1 次", want, got)
		}
	}
	// 四档显示各出现一次。
	for _, mode := range labelModes {
		if got := counts[mode]; got != 1 {
			t.Errorf("档位「%s」出现了 %d 次，期望 1 次", mode, got)
		}
	}
}

// 菜单项文字可能带勾选前缀，比较时去掉。
func trimCheck(label string) string {
	return strings.TrimPrefix(label, "✓ ")
}

func submenuEmails(items []*mygo.MenuItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, trimCheck(it.Label))
	}
	return out
}

// TestAcceptTrayMenuGroupsFullAccounts 本周用尽的账号要在托盘菜单里
// 单独一个子菜单，不跟还能用的混在一起。
func TestAcceptTrayMenuGroupsFullAccounts(t *testing.T) {
	a, accounts := readyApp(t)

	var usable, exhausted *mygo.MenuItem
	for _, item := range a.trayMenu().Items() {
		switch {
		case item.Label == "菜单栏账号":
			usable = item
		case strings.HasPrefix(item.Label, "本周已用尽"):
			exhausted = item
		}
	}
	if usable == nil {
		t.Fatal("托盘菜单里没有「菜单栏账号」")
	}

	split := firstWeeklyFull(accounts)
	if split < 0 {
		split = len(accounts)
	}

	// 还能用的那个子菜单：第一项是「当前使用的账号」，之后是能用的账号。
	wantUsable := append([]string{currentLabel}, emails(accounts[:split])...)
	if got := submenuEmails(usable.Submenu); !slices.Equal(got, wantUsable) {
		t.Errorf("「菜单栏账号」内容不对：\n  得到 %v\n  期望 %v", got, wantUsable)
	}

	// 用尽的那些全在另一个子菜单里。
	wantFull := emails(accounts[split:])
	if len(wantFull) == 0 {
		if exhausted != nil {
			t.Errorf("没有用尽的账号，却出现了分栏：%q", exhausted.Label)
		}
		return
	}
	if exhausted == nil {
		t.Fatalf("有 %d 个本周用尽的账号，却没有单独的子菜单", len(wantFull))
	}
	if got := submenuEmails(exhausted.Submenu); !slices.Equal(got, wantFull) {
		t.Errorf("「%s」内容不对：\n  得到 %v\n  期望 %v", exhausted.Label, got, wantFull)
	}
	// 还能用的子菜单里不能再夹带用尽的账号。
	for _, email := range wantFull {
		if slices.Contains(submenuEmails(usable.Submenu), email) {
			t.Errorf("用尽的 %s 同时出现在能用的子菜单里", email)
		}
	}
	t.Logf("「菜单栏账号」%d 项，「%s」%d 项", len(usable.Submenu), exhausted.Label, len(exhausted.Submenu))
}

// TestAcceptAccountDropdownGroupsFull 面板里的「监控账号」下拉展开后，
// 用尽的账号也要和能用的分开，而且那行标题点不中。
func TestAcceptAccountDropdownGroupsFull(t *testing.T) {
	a, accounts := readyApp(t)
	split := firstWeeklyFull(accounts)
	if split < 0 {
		t.Skip("当前没有本周用尽的账号")
	}

	tt := ui.NewTester(a.view, 360, 680)

	// 收起时只有面板那栏标题（「本周额度已用尽」），没有下拉里的这行。
	if tt.HasText("本周已用尽") {
		t.Error("下拉还没展开，就已经有下拉里的分栏标题了")
	}

	if err := tt.Click(currentLabel); err != nil {
		t.Fatalf("点不开监控账号下拉：%v", err)
	}
	if !tt.HasText("本周已用尽") {
		t.Fatal("下拉展开后没有「本周已用尽」分栏标题")
	}

	// 设 CQ_SHOT=路径 时把展开的这一帧存成 PNG，肉眼确认弹层排版。
	if path := os.Getenv("CQ_SHOT"); path != "" {
		img := tt.Image()
		if img == nil {
			t.Fatal("测试器没有可供截图的帧")
		}
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
		t.Logf("已存快照 %s（%dx%d）", path, img.Bounds().Dx(), img.Bounds().Dy())
	}

	// 展开状态下，用尽的账号仍然可选。
	last := accounts[len(accounts)-1].Email
	tt.Key(0, ui.KeyEnd)
	tt.Key(0, ui.KeyEnter)
	if got := a.accountChoice(); got != last {
		t.Errorf("用尽的账号选不中：得到 %q, 期望 %q", got, last)
	}

	// 分栏标题只是一行文字，点它不该改变选择。
	// 放在最后做：点它会顺手关掉弹层，后面就不方便再选东西了。
	before := a.accountChoice()
	tt.Key(0, ui.KeyDown) // 焦点还在下拉上，方向键重新展开
	if err := tt.Click("本周已用尽"); err != nil {
		t.Fatalf("点不到分栏标题：%v", err)
	}
	if got := a.accountChoice(); got != before {
		t.Errorf("点分栏标题竟把账号改成了 %q（原来是 %q）", got, before)
	}
	t.Logf("下拉共 %d 个账号，其中 %d 个排在本周已用尽的分栏下", len(accounts), len(accounts)-split)
}

// TestAcceptDropdownPicksCurrentAccount 「当前使用的账号」这一档要真的
// 跟着最近在用的账号走，而且能在下拉里选中。
func TestAcceptDropdownPicksCurrentAccount(t *testing.T) {
	a, accounts := readyApp(t)

	idx := currentAccountIndex(accounts)
	if idx < 0 {
		t.Skip("magpie 没有使用记录，没法测这一档")
	}
	want := accounts[idx].Email
	if idx == 0 {
		// 列表第一个本来就是最近的，换个账号来验证「跟谁走」。
		t.Logf("最近在用的正好是列表第一个 %s", want)
	}

	// 默认就是这一档，先切到别的账号，再从下拉里选回来。
	tt := ui.NewTester(a.view, 360, 680)
	if err := tt.Click(currentLabel); err != nil {
		t.Fatalf("点不开监控账号下拉：%v", err)
	}
	tt.Key(0, ui.KeyEnd)
	tt.Key(0, ui.KeyEnter)
	if got := a.accountChoice(); got == currentLabel {
		t.Fatal("没能先切到别的账号")
	}
	tt.Key(0, ui.KeyDown) // 焦点还在下拉上，方向键重新展开
	if err := tt.Click(currentLabel); err != nil {
		t.Fatalf("点不到「%s」：%v", currentLabel, err)
	}

	if got := a.accountChoice(); got != currentLabel {
		t.Errorf("选择 = %q, 期望 %q", got, currentLabel)
	}
	acc := a.selectedAccount()
	if acc == nil || acc.Email != want {
		t.Errorf("这一档没跟着最近在用的账号走：得到 %v, 期望 %s", acc, want)
	}
	t.Logf("「%s」-> %s", currentLabel, want)
}
