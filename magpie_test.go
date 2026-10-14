package main

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// writeFixture 往临时目录里放一份和 magpie 同形的 quotas.json + served.json。
func writeFixture(t *testing.T, quotas, served string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "quotas.json"), []byte(quotas), 0o600); err != nil {
		t.Fatal(err)
	}
	if served != "" {
		if err := os.WriteFile(filepath.Join(dir, "served.json"), []byte(served), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const fixtureQuotas = `{
  "claude/fresh@example.com": {
    "at": "2026-10-14T14:13:17.861141+08:00",
    "quota": {
      "provider": "claude", "plan": "pro", "user": "",
      "readAt": "2026-10-14T14:13:15.399371+08:00",
      "windows": [
        {"name": "5 hours", "used": 3, "resetsAt": "2026-10-14T18:50:00Z"},
        {"name": "7 days", "used": 59, "resetsAt": "2026-10-20T11:59:00Z"}
      ]
    }
  },
  "claude/old@example.com": {
    "at": "2026-09-29T11:12:13.387563+08:00",
    "quota": {
      "provider": "claude", "plan": "pro", "user": "",
      "windows": [
        {"name": "5 hours", "used": 0},
        {"name": "7 days", "used": 100, "resetsAt": "2026-09-30T04:00:00Z"}
      ]
    }
  },
  "claude/named@example.com": {
    "at": "2026-10-14T10:00:00+08:00",
    "quota": {
      "provider": "claude", "plan": "max", "user": "real@example.com",
      "readAt": "2026-10-14T10:00:00+08:00",
      "windows": [{"name": "7 days", "used": 12}]
    }
  },
  "codex/other@example.com": {
    "at": "2026-10-14T14:00:00+08:00",
    "quota": {"provider": "codex", "plan": "plus", "windows": [{"name": "7 days", "used": 50}]}
  }
}`

const fixtureServed = `{
  "claude@fresh@example.com": "2026-10-14T13:57:22+08:00",
  "codex@other@example.com": "2026-10-14T13:00:00+08:00"
}`

func TestLoadAccountsFiltersAndOrders(t *testing.T) {
	dir := writeFixture(t, fixtureQuotas, fixtureServed)
	accounts, err := loadAccountsFrom(dir)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}

	// codex 的不该混进来。
	if len(accounts) != 3 {
		t.Fatalf("应当有 3 个 Claude 账号，得到 %d 个", len(accounts))
	}
	for _, acc := range accounts {
		if acc.Email == "other@example.com" {
			t.Error("非 claude 的账号被算进来了")
		}
	}

	// 最近读到过的排前面：fresh(14:13) > named(10:00) > old(9-29)
	want := []string{"fresh@example.com", "named@example.com", "old@example.com"}
	for i, email := range want {
		if accounts[i].Email != email {
			t.Errorf("第 %d 个应当是 %s，得到 %s", i, email, accounts[i].Email)
		}
	}
}

func TestLoadAccountsFields(t *testing.T) {
	dir := writeFixture(t, fixtureQuotas, fixtureServed)
	accounts, _ := loadAccountsFrom(dir)
	byEmail := map[string]accountQuota{}
	for _, acc := range accounts {
		byEmail[acc.Email] = acc
	}

	fresh := byEmail["fresh@example.com"]
	if fresh.Plan != "pro" {
		t.Errorf("套餐 = %q, 期望 pro", fresh.Plan)
	}
	if s := fresh.session(); s == nil || s.Used != 3 {
		t.Errorf("5 小时窗口读错了：%+v", s)
	}
	if w := fresh.weekly(); w == nil || w.Used != 59 {
		t.Errorf("每周窗口读错了：%+v", w)
	}
	if fresh.ServedAt.IsZero() {
		t.Error("served.json 里的最近使用时间没读进来")
	}

	// 键里的邮箱优先，即使 quota.user 写了别的。
	named, ok := byEmail["named@example.com"]
	if !ok {
		t.Error("键里的邮箱没有被采用")
	}
	if named.Plan != "max" {
		t.Errorf("named 的套餐 = %q, 期望 max", named.Plan)
	}

	// 没有 readAt 的退回用 at。
	old := byEmail["old@example.com"]
	if old.ReadAt.IsZero() {
		t.Error("at 兜底没生效")
	}
	if old.weekly().Level != levelExceeded {
		t.Errorf("100%% 应当是已用尽，得到 %v", old.weekly().Level)
	}
}

func TestStaleDetection(t *testing.T) {
	dir := writeFixture(t, fixtureQuotas, fixtureServed)
	accounts, _ := loadAccountsFrom(dir)
	// 夹具里 fresh 读于 06:13 UTC、real 读于 02:00 UTC，都还在 6 小时内；
	// old 停在 9 月 29 日，早就过期了。
	now := time.Date(2026, 10, 14, 7, 0, 0, 0, time.UTC)

	for _, acc := range accounts {
		want := acc.Email == "old@example.com"
		if got := acc.stale(now); got != want {
			t.Errorf("%s: stale = %v, 期望 %v", acc.Email, got, want)
		}
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := loadAccountsFrom(t.TempDir()); err == nil {
		t.Error("目录里没有 quotas.json 时应当报错")
	}
}

func TestResolveLevel(t *testing.T) {
	cases := []struct {
		percent float64
		want    level
	}{
		{10, levelOK}, {74, levelOK}, {80, levelWarning},
		{95, levelCritical}, {100, levelExceeded}, {101, levelExceeded},
	}
	for _, c := range cases {
		if got := resolveLevel(c.percent); got != c.want {
			t.Errorf("resolveLevel(%.0f) = %v, 期望 %v", c.percent, got, c.want)
		}
	}
}

func TestWindowTitle(t *testing.T) {
	cases := []struct{ name, want string }{
		{"5 hours", "5 小时窗口"},
		{"7 days", "每周额度"},
		{"Weekly", "每周额度"},
		{"别的窗口", "别的窗口"},
	}
	for _, c := range cases {
		if got := (quotaWindow{Name: c.name}).title(); got != c.want {
			t.Errorf("title(%q) = %q, 期望 %q", c.name, got, c.want)
		}
	}
}

func TestCountdown(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"零值给空串", time.Time{}, ""},
		{"已过", now.Add(-time.Minute), "即将重置"},
		{"秒", now.Add(45 * time.Second), "45 秒后重置"},
		{"时分", now.Add(2*time.Hour + 15*time.Minute), "2 小时 15 分后重置"},
		{"天时", now.Add(6*24*time.Hour + 3*time.Hour), "6 天 3 小时后重置"},
	}
	for _, c := range cases {
		if got := countdown(c.at, now); got != c.want {
			t.Errorf("%s: countdown = %q, 期望 %q", c.name, got, c.want)
		}
	}
}

func TestAgeText(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, "从未读到"},
		{now.Add(-30 * time.Second), "30 秒前读取"},
		{now.Add(-5 * time.Minute), "5 分钟前读取"},
		{now.Add(-3 * time.Hour), "3 小时前读取"},
		{now.Add(-48 * time.Hour), "2 天前读取"},
	}
	for _, c := range cases {
		if got := ageText(c.at, now); got != c.want {
			t.Errorf("ageText(%v) = %q, 期望 %q", c.at, got, c.want)
		}
	}
}

func TestMenuBarTitle(t *testing.T) {
	acc := &accountQuota{
		Email: "a@example.com",
		Windows: []quotaWindow{
			{Name: "5 hours", Used: 0},
			{Name: "7 days", Used: 59.4},
		},
	}
	cases := []struct {
		mode int
		want string
	}{
		{modeBoth, "0%·59%"},
		{modeSession, "0%"},
		{modeWeekly, "59%"},
	}
	for _, c := range cases {
		if got := menuBarTitle(acc, c.mode); got != c.want {
			t.Errorf("menuBarTitle(mode=%d) = %q, 期望 %q", c.mode, got, c.want)
		}
	}
	if got := menuBarTitle(nil, modeBoth); got != "" {
		t.Errorf("没有账号时应当是空串，得到 %q", got)
	}
}

func TestGaugeFraction(t *testing.T) {
	acc := &accountQuota{Windows: []quotaWindow{{Name: "5 hours", Used: 20}, {Name: "7 days", Used: 80}}}
	if got := gaugeFraction(acc, modeSession); got != 0.2 {
		t.Errorf("5 小时档 = %v, 期望 0.2", got)
	}
	if got := gaugeFraction(acc, modeWeekly); got != 0.8 {
		t.Errorf("每周档 = %v, 期望 0.8", got)
	}
	if got := gaugeFraction(acc, modeBoth); got != 0.8 {
		t.Errorf("两个档应当取用得多的那条 = %v, 期望 0.8", got)
	}
	if got := gaugeFraction(nil, modeBoth); got != 0 {
		t.Errorf("没有账号时 = %v, 期望 0", got)
	}
}

func TestTigerIcon(t *testing.T) {
	icon := tigerIcon(0.5)
	if len(icon) == 0 || string(icon[1:4]) != "PNG" {
		t.Fatal("图标没有生成成 PNG")
	}
	for _, f := range []float64{0, 0.25, 1, 2, -1} {
		if len(tigerIcon(f)) == 0 {
			t.Errorf("fraction=%v 时图标生成失败", f)
		}
	}
}

// TestTigerIconFills 用得越多，老虎头里不透明的像素越多；花纹始终镂空。
func TestTigerIconFills(t *testing.T) {
	solid := func(f float64) int {
		img, err := png.Decode(bytes.NewReader(tigerIcon(f)))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		b := img.Bounds()
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				if _, _, _, a := img.At(x, y).RGBA(); a > 0xF000 {
					n++
				}
			}
		}
		return n
	}
	empty, half, full := solid(0), solid(0.5), solid(1)
	if !(empty < half && half < full) {
		t.Errorf("不透明像素应随用量增加：0%%=%d 50%%=%d 100%%=%d", empty, half, full)
	}
	// 正中额头那道纹在满格时也得是透明的。
	img, _ := png.Decode(bytes.NewReader(tigerIcon(1)))
	if _, _, _, a := img.At(16, 7).RGBA(); a != 0 {
		t.Errorf("额头花纹没有镂空，alpha=%d", a)
	}
}

// TestLiveMagpie 读真实的 magpie 文件，不需要网络，所以总是跑。
func TestLiveMagpie(t *testing.T) {
	dir := magpieDir()
	if dir == "" {
		t.Skip("拿不到用户目录")
	}
	if _, err := os.Stat(filepath.Join(dir, "quotas.json")); err != nil {
		t.Skip("这台机器上没有 magpie 的额度文件")
	}

	accounts, err := loadAccounts()
	if err != nil {
		t.Fatalf("读 magpie 数据失败：%v", err)
	}
	if len(accounts) == 0 {
		t.Fatal("一个 Claude 账号都没读到")
	}

	now := time.Now()
	t.Logf("从 %s 读到 %d 个 Claude 账号：", dir, len(accounts))
	for _, acc := range accounts {
		flag := ""
		if acc.stale(now) {
			flag = "（已过期）"
		}
		t.Logf("  %-38s %-5s %s%s",
			acc.Email, acc.Plan, ageText(acc.ReadAt, now), flag)
		for _, w := range acc.Windows {
			t.Logf("      %s %.0f%% %s", w.title(), w.Used, countdown(w.ResetsAt, now))
		}
	}
}

// ---------- 排序与分组 ----------

func weeklyAccount(email string, weekly float64, resetIn time.Duration, readAt, servedAt time.Time) accountQuota {
	w := quotaWindow{Name: "7 days", Used: weekly}
	if resetIn != 0 {
		w.ResetsAt = readAt.Add(resetIn)
	}
	return accountQuota{
		Email:    email,
		Plan:     "pro",
		ReadAt:   readAt,
		ServedAt: servedAt,
		Windows: []quotaWindow{
			{Name: "5 hours", Used: 0},
			w,
		},
	}
}

func emails(accounts []accountQuota) []string {
	out := make([]string, len(accounts))
	for i, acc := range accounts {
		out[i] = acc.Email
	}
	return out
}

func TestOrderAccounts(t *testing.T) {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

	accounts := []accountQuota{
		weeklyAccount("lots-left@x.com", 10, 0, at, time.Time{}),
		weeklyAccount("full@x.com", 100, 2*time.Hour, at, time.Time{}),
		weeklyAccount("active@x.com", 60, 0, at, at.Add(-time.Hour)),
		weeklyAccount("almost@x.com", 90, 0, at, time.Time{}),
		weeklyAccount("over@x.com", 130, time.Hour, at, time.Time{}),
		// 没有周窗口的账号：剩余量未知。
		{Email: "no-weekly@x.com", ReadAt: at, Windows: []quotaWindow{{Name: "5 hours", Used: 5}}},
	}

	orderAccounts(accounts)

	want := []string{
		"active@x.com",    // 最近在用，排第一
		"almost@x.com",    // 周剩余 10%
		"lots-left@x.com", // 周剩余 90%
		"no-weekly@x.com", // 没有周窗口，剩余未知，排在已知的后面
		"over@x.com",      // 本周用尽，1 小时后恢复
		"full@x.com",      // 本周用尽，2 小时后恢复
	}
	if got := emails(accounts); !slices.Equal(got, want) {
		t.Errorf("顺序不对：\n  得到 %v\n  期望 %v", got, want)
	}

	if got := firstWeeklyFull(accounts); got != 4 {
		t.Errorf("用尽的那一组应当从第 4 个开始，得到 %d", got)
	}
}

func TestOrderAccountsNoCurrentNoFull(t *testing.T) {
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	accounts := []accountQuota{
		weeklyAccount("b@x.com", 30, 0, at, time.Time{}),
		weeklyAccount("a@x.com", 70, 0, at, time.Time{}),
	}

	orderAccounts(accounts)

	// 没有 served 记录就别乱动，只按周剩余升序：a 剩 30% 排在 b 剩 70% 前。
	if got := emails(accounts); !slices.Equal(got, []string{"a@x.com", "b@x.com"}) {
		t.Errorf("顺序不对：%v", got)
	}
	if got := firstWeeklyFull(accounts); got != -1 {
		t.Errorf("没有用尽的账号时应当返回 -1，得到 %d", got)
	}
}

// TestAcceptStoredOrder 真实数据也要满足同样的顺序约束。
func TestAcceptStoredOrder(t *testing.T) {
	accounts := realAccounts(t)
	orderAccounts(accounts) // app 存的就是排好序的

	if got := firstWeeklyFull(accounts); got >= 0 {
		// 分栏之后，用尽的那一段里不允许再混进还能用的账号。
		for i := got; i < len(accounts); i++ {
			if !accounts[i].isWeeklyFull() {
				t.Errorf("第 %d 个 %s 还能用，却排在用尽的那一栏里", i, accounts[i].Email)
			}
		}
		// 用尽之前的那一段也不允许出现用尽的账号。
		for i := 0; i < got; i++ {
			if accounts[i].isWeeklyFull() {
				t.Errorf("第 %d 个 %s 已用尽，却排在还能用的那一栏里", i, accounts[i].Email)
			}
		}
	}

	// 最近在用的那个（如果有）必须排在最前。
	best, bestAt := -1, time.Time{}
	for i, acc := range accounts {
		if acc.ServedAt.After(bestAt) {
			best, bestAt = i, acc.ServedAt
		}
	}
	if !bestAt.IsZero() && best != 0 {
		t.Errorf("最近在用的 %s 排在第 %d 位，应当在第 0 位", accounts[best].Email, best)
	}

	// 除开排第一的那个「最近在用」，其余还能用的账号周剩余必须单调不减。
	split := firstWeeklyFull(accounts)
	if split < 0 {
		split = len(accounts)
	}
	start := 0
	if !bestAt.IsZero() {
		start = 1 // 第 0 位是被提到最前的在用账号，不参与这段排序
	}
	for i := start + 1; i < split; i++ {
		if accounts[i].weeklyRemaining() < accounts[i-1].weeklyRemaining() {
			t.Errorf("第 %d 个 %s 剩余 %.0f%% 比前一个 %s 的 %.0f%% 还少，顺序乱了",
				i, accounts[i].Email, accounts[i].weeklyRemaining(),
				accounts[i-1].Email, accounts[i-1].weeklyRemaining())
		}
	}

	for i, acc := range accounts {
		mark := "  "
		if acc.isWeeklyFull() {
			mark = "满"
		}
		t.Logf("  %s %d. %-38s 周剩余 %.0f%%", mark, i, acc.Email, acc.weeklyRemaining())
	}
}

// ---------- 与 magpie 的账号登记表对齐 ----------

// TestLoadAccountsSkipsDeleted 已经删掉的账号不能再显示出来。
// magpie 删号时不会清掉 quotas.json 里的额度快照，那记录会一直躺着。
func TestLoadAccountsSkipsDeleted(t *testing.T) {
	dir := writeFixture(t, fixtureQuotas, fixtureServed)
	logins := `[
		{"agent": "claude", "user": "fresh@example.com"},
		{"agent": "codex",  "user": "other@example.com"}
	]`
	if err := os.WriteFile(filepath.Join(dir, "logins.json"), []byte(logins), 0o600); err != nil {
		t.Fatal(err)
	}

	accounts, err := loadAccountsFrom(dir)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("应当只剩登记表里的 1 个账号，得到 %d 个：%v", len(accounts), emails(accounts))
	}
	if accounts[0].Email != "fresh@example.com" {
		t.Errorf("留下的账号不对：%s", accounts[0].Email)
	}
	// 其他 agent 名下的账号不能因为出现在登记表里就被放进来。
	if slices.Contains(emails(accounts), "other@example.com") {
		t.Error("codex 的账号被混进来了")
	}
}

// TestLoadAccountsWithoutLogins 登记表读不到时不该把账号全过滤光。
func TestLoadAccountsWithoutLogins(t *testing.T) {
	dir := writeFixture(t, fixtureQuotas, fixtureServed) // 故意不写 logins.json

	accounts, err := loadAccountsFrom(dir)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if len(accounts) != 3 {
		t.Errorf("没有登记表时应当照旧显示 3 个账号，得到 %d 个：%v", len(accounts), emails(accounts))
	}
}

// TestAcceptNoDeletedAccounts 真实数据里显示出来的账号，必须都在 magpie 的登记表里。
func TestAcceptNoDeletedAccounts(t *testing.T) {
	accounts := realAccounts(t)

	live := loadLogins(magpieDir(), "claude")
	if len(live) == 0 {
		t.Skip("读不到 magpie 的账号登记表")
	}

	for _, acc := range accounts {
		if !live[acc.Email] {
			t.Errorf("%s 显示在界面上，但 magpie 的登记表里已经没有它了", acc.Email)
		}
	}
	if len(accounts) != len(live) {
		t.Errorf("账号数对不上：显示 %d 个，登记表里有 %d 个", len(accounts), len(live))
	}
	t.Logf("显示的 %d 个账号全部在登记表里，无遗漏无残留", len(accounts))
}

// TestPickAccount 「当前使用的账号」挑最近在用的，不看谁已用得最多；
// 下标越界时也退回这一档。
func TestPickAccount(t *testing.T) {
	now := time.Now()
	accounts := []accountQuota{
		weeklyAccount("busy@x.com", 70, 0, now, now.Add(-2*time.Hour)),   // 用过，但不是最近
		weeklyAccount("active@x.com", 10, 0, now, now.Add(-time.Minute)), // 最近在用
		weeklyAccount("tight@x.com", 95, 0, now, time.Time{}),            // 已用最高
	}

	cases := []struct {
		name   string
		choice int
		want   string
	}{
		{"当前使用挑最近在用的", currentChoice, "active@x.com"},
		{"按下标挑具体账号", 2, "tight@x.com"},
		{"下标越界退回当前使用", 99, "active@x.com"},
	}
	for _, c := range cases {
		got := pickAccount(accounts, c.choice)
		if got == nil || got.Email != c.want {
			t.Errorf("%s：得到 %v，期望 %s", c.name, got, c.want)
		}
	}

	if got := pickAccount(nil, currentChoice); got != nil {
		t.Error("没有账号时应当返回 nil")
	}
}

// TestPickAccountCurrentWithoutServed 一条使用记录都没有时也要给出个账号，
// 不能让菜单栏空着。
func TestPickAccountCurrentWithoutServed(t *testing.T) {
	now := time.Now()
	accounts := []accountQuota{
		weeklyAccount("a@x.com", 20, 0, now, time.Time{}),
		weeklyAccount("b@x.com", 50, 0, now, time.Time{}),
	}
	if idx := currentAccountIndex(accounts); idx != -1 {
		t.Errorf("没有使用记录时 currentAccountIndex 应当是 -1，得到 %d", idx)
	}
	got := pickAccount(accounts, currentChoice)
	if got == nil {
		t.Fatal("没有使用记录时不该返回 nil")
	}
	if got.Email != "a@x.com" {
		t.Errorf("应当退回列表第一个，得到 %s", got.Email)
	}
}
