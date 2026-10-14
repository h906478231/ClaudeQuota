package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// magpie 自己按账号维护着额度快照。我们只读它写下的文件：
// 不去碰 OAuth token，也不去挤那个会限流的接口。
func magpieDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "magpie")
}

// ---------- magpie 的文件格式 ----------

type rawWindow struct {
	Name     string  `json:"name"`
	Used     float64 `json:"used"`
	ResetsAt string  `json:"resetsAt"`
}

type rawEntry struct {
	At    string `json:"at"`
	Quota struct {
		Provider string      `json:"provider"`
		Plan     string      `json:"plan"`
		User     string      `json:"user"`
		ReadAt   string      `json:"readAt"`
		Windows  []rawWindow `json:"windows"`
	} `json:"quota"`
}

// ---------- 界面用的模型 ----------

type level int

const (
	levelOK level = iota
	levelWarning
	levelCritical
	levelExceeded
)

func resolveLevel(percent float64) level {
	switch {
	case percent >= 100:
		return levelExceeded
	case percent >= 90:
		return levelCritical
	case percent >= 75:
		return levelWarning
	}
	return levelOK
}

func (l level) label() string {
	switch l {
	case levelWarning:
		return "接近上限"
	case levelCritical:
		return "即将用尽"
	case levelExceeded:
		return "已用尽"
	}
	return "正常"
}

type quotaWindow struct {
	Name     string
	Used     float64
	Level    level
	ResetsAt time.Time
}

func (w quotaWindow) title() string {
	lower := strings.ToLower(w.Name)
	switch {
	case strings.Contains(lower, "5") || strings.Contains(lower, "hour"):
		return "5 小时窗口"
	case strings.Contains(lower, "7") || strings.Contains(lower, "day") || strings.Contains(lower, "week"):
		return "每周额度"
	}
	return w.Name
}

func (w quotaWindow) isSession() bool {
	lower := strings.ToLower(w.Name)
	return strings.Contains(lower, "5") || strings.Contains(lower, "hour")
}

func (w quotaWindow) isWeekly() bool {
	return !w.isSession()
}

// accountQuota 是一个账号当前的额度，界面照着它画。
type accountQuota struct {
	Email     string
	Plan      string
	Windows   []quotaWindow
	ReadAt    time.Time
	ServedAt  time.Time
	HadReadAt bool
	// InUse 是 magpie 认定的「正在使用」的那一个，同一时刻最多一个，见 markInUse。
	InUse bool
}

func (a accountQuota) session() *quotaWindow {
	for i := range a.Windows {
		if a.Windows[i].isSession() {
			return &a.Windows[i]
		}
	}
	return nil
}

// currentAccountIndex 是 magpie 认定正在使用的那个账号的下标，
// magpie 说不出来时返回 -1。
func currentAccountIndex(accounts []accountQuota) int {
	for i, acc := range accounts {
		if acc.InUse {
			return i
		}
	}
	return -1
}

func (a accountQuota) weekly() *quotaWindow {
	for i := range a.Windows {
		if a.Windows[i].isWeekly() {
			return &a.Windows[i]
		}
	}
	return nil
}

func (a accountQuota) mostUsed() *quotaWindow {
	var best *quotaWindow
	for i := range a.Windows {
		if best == nil || a.Windows[i].Used > best.Used {
			best = &a.Windows[i]
		}
	}
	return best
}

// stale 表示 magpie 很久没读到过这个账号了 —— 账号没在用，或者已登出。
func (a accountQuota) stale(now time.Time) bool {
	return a.ReadAt.IsZero() || now.Sub(a.ReadAt) > 6*time.Hour
}

// isWeeklyFull 表示这个账号本周的额度已经用完。
func (a accountQuota) isWeeklyFull() bool {
	w := a.weekly()
	return w != nil && w.Used >= 100
}

// weeklyRemaining 是本周还剩多少。没有周窗口的算作「不知道」，
// 排到最后，别混进有明确剩余量的账号之间。
func (a accountQuota) weeklyRemaining() float64 {
	w := a.weekly()
	if w == nil {
		return math.Inf(1)
	}
	return 100 - w.Used
}

// weeklyReset 是本周额度恢复的时刻，没记录就是零值。
func (a accountQuota) weeklyReset() time.Time {
	if w := a.weekly(); w != nil {
		return w.ResetsAt
	}
	return time.Time{}
}

// resetBefore 让有恢复时间的排在没记录的前面；两边都有就按先后。
func resetBefore(left, right time.Time) bool {
	if left.IsZero() != right.IsZero() {
		return !left.IsZero()
	}
	return left.Before(right)
}

// orderAccounts 就地排成界面要的顺序：
// 还能用的账号在前，其中正在使用的排第一，其余按周额度剩余升序
// （快用满的靠前）；本周已经用完的整组沉到最后。
func orderAccounts(accounts []accountQuota) {
	sort.SliceStable(accounts, func(i, j int) bool {
		left, right := accounts[i], accounts[j]
		if left.isWeeklyFull() != right.isWeeklyFull() {
			return !left.isWeeklyFull()
		}
		if left.isWeeklyFull() {
			// 都用尽了，剩余量没意义，看谁先恢复。
			return resetBefore(left.weeklyReset(), right.weeklyReset())
		}
		if left.weeklyRemaining() != right.weeklyRemaining() {
			return left.weeklyRemaining() < right.weeklyRemaining()
		}
		// 剩余一样时，读得新的靠前。
		return left.ReadAt.After(right.ReadAt)
	})

	// 正在使用的那一个提到最前面。
	if best := currentAccountIndex(accounts); best > 0 {
		current := accounts[best]
		copy(accounts[1:best+1], accounts[0:best])
		accounts[0] = current
	}
}

// firstWeeklyFull 是本周用完的那一组从第几个开始，没有就是 -1。
// 界面对着它插一条分栏标题。
func firstWeeklyFull(accounts []accountQuota) int {
	for i, acc := range accounts {
		if acc.isWeeklyFull() {
			return i
		}
	}
	return -1
}

// ---------- 读取 ----------

func loadAccounts() ([]accountQuota, error) {
	return loadAccountsFrom(magpieDir())
}

// loadAccountsFrom 从一个 magpie 配置目录读额度，方便测试用临时目录喂数据。
func loadAccountsFrom(dir string) ([]accountQuota, error) {
	path := filepath.Join(dir, "quotas.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读不到 magpie 的额度文件（%s）", path)
	}

	var raw map[string]rawEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("magpie 额度文件解析失败：%w", err)
	}

	served := loadServed(dir)
	logins := readLogins(dir, "claude")
	// 登记表读不到时留空，那就不过滤 —— 宁可多显示，也别把整份数据清空。
	live := liveSet(logins)

	var out []accountQuota
	for key, entry := range raw {
		if entry.Quota.Provider != "" && entry.Quota.Provider != "claude" {
			continue
		}
		email, ok := strings.CutPrefix(key, "claude/")
		if !ok {
			continue
		}
		// magpie 删号后额度快照还留在文件里，只认登记表里仍在的账号。
		if len(live) > 0 && !live[email] {
			continue
		}
		// 键里的邮箱最可靠，quota.user 缺失时才有用。
		if email == "" {
			email = entry.Quota.User
		}

		acc := accountQuota{
			Email:    email,
			Plan:     entry.Quota.Plan,
			ServedAt: served[strings.ToLower(email)],
		}
		for _, w := range entry.Quota.Windows {
			acc.Windows = append(acc.Windows, quotaWindow{
				Name:     w.Name,
				Used:     w.Used,
				Level:    resolveLevel(w.Used),
				ResetsAt: parseTime(w.ResetsAt),
			})
		}

		// readAt 是真正读到额度的时间，at 是写入时间，后者兜底。
		for _, candidate := range []string{entry.Quota.ReadAt, entry.At} {
			if t := parseTime(candidate); !t.IsZero() {
				acc.ReadAt = t
				acc.HadReadAt = true
				break
			}
		}
		if len(acc.Windows) > 0 {
			out = append(out, acc)
		}
	}

	// 最近读到过的排前面，久未更新的自然沉到底部；同则按邮箱稳定排序。
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ReadAt.Equal(out[j].ReadAt) {
			return out[i].ReadAt.After(out[j].ReadAt)
		}
		return out[i].Email < out[j].Email
	})
	markInUse(out, logins, signedInClaude(), time.Now())
	return out, nil
}

// loginRecord 是 magpie 账号登记表（logins.json）里一条账号用得上的部分。
// 那张表里还存着各账号的凭据，我们一概不读。
type loginRecord struct {
	Agent  string `json:"agent"`
	User   string `json:"user"`
	On     bool   `json:"on"`     // 轮到它接班：正在用的那个用完后请求转给它
	Paused bool   `json:"paused"` // 用户在 magpie 里暂停了正在登录的这个
	Held   bool   `json:"held"`   // magpie 上次看时 Claude Code 登录的就是它
	Lapsed string `json:"lapsed"` // 登录失效了，得重新登录才能用
}

// readLogins 读 magpie 的账号登记表，按文件里的顺序返回某个 agent 名下的账号。
// 读不到表时返回 nil。
func readLogins(dir, agent string) []loginRecord {
	data, err := os.ReadFile(filepath.Join(dir, "logins.json"))
	if err != nil {
		return nil
	}
	var records []loginRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil
	}
	var out []loginRecord
	for _, record := range records {
		if record.Agent == agent && record.User != "" {
			out = append(out, record)
		}
	}
	return out
}

// loadLogins 返回某个 agent 名下还存在的账号。
//
// 这张表才是「活着」的账号集合：magpie 删号时不会顺手清掉 quotas.json
// 里的额度快照，那些记录会一直躺着，照单全收就会把早删掉的账号也显示出来。
// 读不到表时返回空集，调用方据此放弃过滤，宁可多显示也别整个空掉。
func loadLogins(dir, agent string) map[string]bool {
	return liveSet(readLogins(dir, agent))
}

func liveSet(records []loginRecord) map[string]bool {
	if records == nil {
		return nil
	}
	out := map[string]bool{}
	for _, record := range records {
		out[record.User] = true
	}
	return out
}

// ---------- 正在使用的账号 ----------
//
// 跟 magpie 自己菜单栏上「正在使用的账号」用同一套规则（magpie 的
// internal/gui/trayusage.go 里的 trayInUseCard 和 provider.InUseLogin）：
//
//  1. 一小时内经网关回答过请求的账号里，最近的那一个；
//  2. 否则是 Claude Code 当前登录的账号 —— 被暂停了就换下一个接班的，
//     额度已用尽而另有接班账号还有余量时也换过去；
//  3. 否则是最后回答过请求的那个，不管多久以前。
//
// 同一时刻只有一个。以前是「半小时内用过的都算在用」，magpie 换号后旧号
// 还会挂着「正在使用」，好几个账号同时亮。

// servedFresh 是 magpie 认「刚用过」的时限，和它的 trayServedFresh 一致。
const servedFresh = time.Hour

// spentShare 是 magpie 认为一个账号额度已用尽的比例（它的 SpentShare）。
const spentShare = 98

// markInUse 把 accounts 里正在使用的那一个标上 InUse，其余清掉。
// logins 是 magpie 登记表里的 Claude 账号，signedIn 是 Claude Code 当前登录的账号。
func markInUse(accounts []accountQuota, logins []loginRecord, signedIn string, now time.Time) {
	for i := range accounts {
		accounts[i].InUse = false
	}
	if idx := inUseIndex(accounts, logins, signedIn, now); idx >= 0 {
		accounts[idx].InUse = true
	}
}

func inUseIndex(accounts []accountQuota, logins []loginRecord, signedIn string, now time.Time) int {
	if len(accounts) == 0 {
		return -1
	}
	if len(accounts) == 1 {
		return 0
	}
	last := -1
	for i, acc := range accounts {
		if !acc.ServedAt.IsZero() && (last < 0 || acc.ServedAt.After(accounts[last].ServedAt)) {
			last = i
		}
	}
	if last >= 0 && now.Sub(accounts[last].ServedAt) < servedFresh {
		return last
	}
	if user := inUseLogin(logins, signedIn, accountRoom(accounts, now)); user != "" {
		for i, acc := range accounts {
			if sameUser(acc.Email, user) {
				return i
			}
		}
	}
	return last
}

// accountRoom 说某个账号的额度是否已知、是否已用尽。
// 读不到的账号算「不知道」，绝不当作用尽。
func accountRoom(accounts []accountQuota, now time.Time) func(user string) (known, spent bool) {
	return func(user string) (bool, bool) {
		for _, acc := range accounts {
			if !sameUser(acc.Email, user) {
				continue
			}
			for _, w := range acc.Windows {
				// 已经过了重置时间的窗口在 magpie 那边是空的。
				if w.Used >= spentShare && (w.ResetsAt.IsZero() || w.ResetsAt.After(now)) {
					return true, true
				}
			}
			return true, false
		}
		return false, false
	}
}

// inUseLogin 是 magpie 网关最先把请求发给的那个账号（它的 InUseLogin）：
// Claude Code 登录着的那个，除非它被暂停，或者额度用尽而另有接班的还有余量；
// 否则是第一个接班的。登记表里没有时返回空串。
func inUseLogin(logins []loginRecord, signedIn string, room func(user string) (known, spent bool)) string {
	if signedIn == "" {
		// 读不到 Claude Code 的登录信息时，用 magpie 上次看到的那个。
		for _, l := range logins {
			if l.Held {
				signedIn = l.User
				break
			}
		}
	}
	active := func(l loginRecord) bool { return signedIn != "" && sameUser(l.User, signedIn) }
	othersOn := func(user string) bool {
		for _, l := range logins {
			if l.On && !sameUser(l.User, user) {
				return true
			}
		}
		return false
	}

	for _, l := range logins {
		if !active(l) {
			continue
		}
		// 暂停只在另有账号接班时才生效，不然请求照旧发给它。
		if l.Paused && othersOn(l.User) {
			break
		}
		if known, spent := room(l.User); known && spent {
			for _, next := range logins {
				if !next.On || next.Lapsed != "" || active(next) {
					continue
				}
				if known, spent := room(next.User); known && !spent {
					return next.User
				}
			}
		}
		return l.User
	}
	for _, l := range logins {
		if (l.On || active(l)) && !(active(l) && l.Paused) {
			return l.User
		}
	}
	return ""
}

// sameUser 比较两个账号名：不分大小写；团队账号在 magpie 里叫
// 「邮箱 · 组织」，只比邮箱那一段。
func sameUser(a, b string) bool {
	a, _, _ = strings.Cut(a, " · ")
	b, _, _ = strings.Cut(b, " · ")
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// claudeProfilePath 是 Claude Code 存登录账号的文件，和 magpie 找的是同一个。
// 测试里换掉。
var claudeProfilePath = func() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

// signedInClaude 是 Claude Code 当前登录的账号邮箱，读不到时返回空串。
// 只取 oauthAccount 里的邮箱，不碰凭据。
func signedInClaude() string {
	path := claudeProfilePath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var profile struct {
		OAuthAccount struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(data, &profile) != nil {
		return ""
	}
	return strings.TrimSpace(profile.OAuthAccount.EmailAddress)
}

// loadServed 读 magpie 记录的各账号最近经网关回答请求的时刻，键是小写邮箱。
func loadServed(dir string) map[string]time.Time {
	out := map[string]time.Time{}
	data, err := os.ReadFile(filepath.Join(dir, "served.json"))
	if err != nil {
		return out
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	for key, value := range raw {
		email, ok := strings.CutPrefix(key, "claude@")
		if !ok {
			continue
		}
		// magpie 记的键是小写的，查的时候也按小写查。
		if t := parseTime(value); !t.IsZero() {
			out[strings.ToLower(email)] = t
		}
	}
	return out
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// countdown 把重置时间说成人话。
func countdown(to, now time.Time) string {
	if to.IsZero() {
		return ""
	}
	d := to.Sub(now)
	if d <= 0 {
		return "即将重置"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	switch {
	case days > 0:
		return fmt.Sprintf("%d 天 %d 小时后重置", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d 小时 %d 分后重置", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%d 分 %d 秒后重置", minutes, seconds)
	}
	return fmt.Sprintf("%d 秒后重置", seconds)
}

// ageText 说明这份数字是多久以前读到的。
func ageText(t, now time.Time) string {
	if t.IsZero() {
		return "从未读到"
	}
	switch elapsed := int(now.Sub(t).Seconds()); {
	case elapsed < 0:
		return "刚刚读取"
	case elapsed < 60:
		return fmt.Sprintf("%d 秒前读取", elapsed)
	case elapsed < 3600:
		return fmt.Sprintf("%d 分钟前读取", elapsed/60)
	case elapsed < 86400:
		return fmt.Sprintf("%d 小时前读取", elapsed/3600)
	default:
		return fmt.Sprintf("%d 天前读取", elapsed/86400)
	}
}

// ageTextShort 是账号行上用的短版本：邮箱很长，多四个字就会折行。
func ageTextShort(t, now time.Time) string {
	switch text := ageText(t, now); text {
	case "从未读到", "刚刚读取":
		return text
	default:
		return strings.TrimSuffix(text, "读取")
	}
}
