package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- 「正在使用」跟着 magpie 走 ----------

// inUseFixture 在临时目录里摆一套 magpie 文件和 Claude Code 的 .claude.json。
// weekly 是各账号的周用量；served 是各账号距今多久前经网关回答过请求
// （没有的就不写）；signedIn 是 Claude Code 登录的账号，空串表示没有 .claude.json。
func inUseFixture(t *testing.T, weekly map[string]float64, served map[string]time.Duration, logins, signedIn string) []accountQuota {
	t.Helper()
	now := time.Now()

	var quotas []string
	for email, used := range weekly {
		quotas = append(quotas, fmt.Sprintf(`"claude/%s": {"at": %q, "quota": {"provider": "claude", "plan": "max",
			"readAt": %q, "windows": [{"name": "7 days", "used": %g, "resetsAt": %q}]}}`,
			email, now.Format(time.RFC3339), now.Format(time.RFC3339), used, now.Add(48*time.Hour).Format(time.RFC3339)))
	}
	var servedLines []string
	for email, ago := range served {
		servedLines = append(servedLines, fmt.Sprintf(`"claude@%s": %q`, strings.ToLower(email), now.Add(-ago).Format(time.RFC3339)))
	}
	dir := writeFixture(t, "{"+strings.Join(quotas, ",")+"}", "{"+strings.Join(servedLines, ",")+"}")
	if logins != "" {
		if err := os.WriteFile(filepath.Join(dir, "logins.json"), []byte(logins), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	profile := filepath.Join(t.TempDir(), ".claude.json")
	if signedIn != "" {
		body := fmt.Sprintf(`{"numStartups": 3, "oauthAccount": {"emailAddress": %q, "organizationName": "x"}}`, signedIn)
		if err := os.WriteFile(profile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := claudeProfilePath
	claudeProfilePath = func() string { return profile }
	t.Cleanup(func() { claudeProfilePath = old })

	accounts, err := loadAccountsFrom(dir)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	return accounts
}

// inUse 返回被标成「正在使用」的账号；多于一个时直接判失败。
func inUse(t *testing.T, accounts []accountQuota) string {
	t.Helper()
	var marked []string
	for _, acc := range accounts {
		if acc.InUse {
			marked = append(marked, acc.Email)
		}
	}
	if len(marked) > 1 {
		t.Fatalf("同时有 %d 个账号标着正在使用：%v", len(marked), marked)
	}
	if len(marked) == 0 {
		return ""
	}
	if idx := currentAccountIndex(accounts); idx < 0 || accounts[idx].Email != marked[0] {
		t.Errorf("currentAccountIndex 没指向正在使用的 %s", marked[0])
	}
	return marked[0]
}

const threeLogins = `[
	{"agent": "claude", "user": "a@x.com", "on": true, "auth": {"secret": "不读"}},
	{"agent": "claude", "user": "b@x.com", "on": true},
	{"agent": "claude", "user": "c@x.com"}
]`

var threeWeekly = map[string]float64{"a@x.com": 30, "b@x.com": 40, "c@x.com": 50}

// TestInUseDropsPreviousAccount 复现用户报的问题：用过一个账号后，
// magpie 换到下一个，旧账号还一直挂着「正在使用」。
func TestInUseDropsPreviousAccount(t *testing.T) {
	// a 二十分钟前还在回答请求，之后 magpie 换到 b。
	// 旧逻辑是「半小时内用过就算在用」，a 和 b 会同时亮。
	accounts := inUseFixture(t, threeWeekly,
		map[string]time.Duration{"a@x.com": 20 * time.Minute, "b@x.com": 2 * time.Minute},
		threeLogins, "b@x.com")
	if got := inUse(t, accounts); got != "b@x.com" {
		t.Errorf("正在使用 = %q, 期望 magpie 现在用的 b@x.com", got)
	}
}

// TestInUseFollowsSignIn 网关一小时内没人用时，跟着 Claude Code 登录的账号走，
// 不再认很久以前用过的那个。
func TestInUseFollowsSignIn(t *testing.T) {
	accounts := inUseFixture(t, threeWeekly,
		map[string]time.Duration{"a@x.com": 3 * time.Hour},
		threeLogins, "B@X.com")
	if got := inUse(t, accounts); got != "b@x.com" {
		t.Errorf("正在使用 = %q, 期望登录着的 b@x.com", got)
	}
}

// TestInUseRecentServedWins 一小时内回答过请求的账号优先，和 magpie 的菜单栏一样：
// 网关已经转去别的账号时，登录信息可能还没跟上。
func TestInUseRecentServedWins(t *testing.T) {
	accounts := inUseFixture(t, threeWeekly,
		map[string]time.Duration{"c@x.com": 10 * time.Minute},
		threeLogins, "a@x.com")
	if got := inUse(t, accounts); got != "c@x.com" {
		t.Errorf("正在使用 = %q, 期望刚回答过请求的 c@x.com", got)
	}
}

func TestInUseSkipsPausedSignIn(t *testing.T) {
	logins := `[
		{"agent": "claude", "user": "a@x.com", "paused": true},
		{"agent": "claude", "user": "b@x.com"},
		{"agent": "claude", "user": "c@x.com", "on": true}
	]`
	accounts := inUseFixture(t, threeWeekly, nil, logins, "a@x.com")
	if got := inUse(t, accounts); got != "c@x.com" {
		t.Errorf("正在使用 = %q, 期望接班的 c@x.com（a 被暂停了）", got)
	}
}

func TestInUseSpentSignIn(t *testing.T) {
	weekly := map[string]float64{"a@x.com": 100, "b@x.com": 100, "c@x.com": 20}
	logins := `[
		{"agent": "claude", "user": "a@x.com"},
		{"agent": "claude", "user": "b@x.com", "on": true},
		{"agent": "claude", "user": "c@x.com", "on": true}
	]`
	accounts := inUseFixture(t, weekly, nil, logins, "a@x.com")
	if got := inUse(t, accounts); got != "c@x.com" {
		t.Errorf("正在使用 = %q, 期望还有余量的接班账号 c@x.com", got)
	}

	// 没有哪个接班的还有余量时，就留在登录着的那个上。
	weekly["c@x.com"] = 100
	accounts = inUseFixture(t, weekly, nil, logins, "a@x.com")
	if got := inUse(t, accounts); got != "a@x.com" {
		t.Errorf("正在使用 = %q, 期望留在登录着的 a@x.com", got)
	}
}

// TestInUseHeldFallback 读不到 .claude.json 时，用 magpie 记下的登录账号。
func TestInUseHeldFallback(t *testing.T) {
	logins := `[
		{"agent": "claude", "user": "a@x.com", "on": true},
		{"agent": "claude", "user": "b@x.com", "held": true}
	]`
	accounts := inUseFixture(t, threeWeekly, nil, logins, "")
	if got := inUse(t, accounts); got != "b@x.com" {
		t.Errorf("正在使用 = %q, 期望 magpie 记下的 b@x.com", got)
	}
}

// TestInUseTeamAccount 团队账号在 magpie 里叫「邮箱 · 组织」。
func TestInUseTeamAccount(t *testing.T) {
	weekly := map[string]float64{"a@x.com": 10, "me@corp.com · Corp": 20}
	logins := `[
		{"agent": "claude", "user": "a@x.com", "on": true},
		{"agent": "claude", "user": "me@corp.com · Corp"}
	]`
	accounts := inUseFixture(t, weekly, nil, logins, "me@corp.com")
	if got := inUse(t, accounts); got != "me@corp.com · Corp" {
		t.Errorf("正在使用 = %q, 期望团队账号", got)
	}
}

// TestInUseUnknown magpie 什么也说不出来时，谁都不标。
func TestInUseUnknown(t *testing.T) {
	accounts := inUseFixture(t, threeWeekly, nil, "", "")
	if got := inUse(t, accounts); got != "" {
		t.Errorf("没有任何线索时不该标正在使用，得到 %q", got)
	}
}

// TestInUseOldServed 只有很久以前的使用记录、又读不到登录信息时，
// 和 magpie 一样退回最后用过的那个。
func TestInUseOldServed(t *testing.T) {
	accounts := inUseFixture(t, threeWeekly,
		map[string]time.Duration{"a@x.com": 5 * time.Hour, "c@x.com": 3 * time.Hour}, "", "")
	if got := inUse(t, accounts); got != "c@x.com" {
		t.Errorf("正在使用 = %q, 期望最后用过的 c@x.com", got)
	}
}
