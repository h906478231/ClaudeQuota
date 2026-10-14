package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// fakeMagpie 把 runMagpie 换成假的，记下每次传进来的参数。
func fakeMagpie(t *testing.T, reply string, err error) *[][]string {
	t.Helper()
	var mu sync.Mutex
	calls := &[][]string{}
	old := runMagpie
	runMagpie = func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		*calls = append(*calls, args)
		mu.Unlock()
		return []byte(reply), err
	}
	t.Cleanup(func() { runMagpie = old })
	return calls
}

func fakeAccountsApp() *app {
	a := newApp()
	a.accounts = []accountQuota{{
		Email:   "one@example.com",
		Plan:    "max",
		ReadAt:  time.Now().Add(-10 * time.Minute),
		Windows: []quotaWindow{{Name: "five_hour", Used: 40}, {Name: "seven_day", Used: 20}},
	}}
	return a
}

// 同步要真的让 magpie 去读这一个账号，而不是别的命令或别的账号。
func TestSyncAccountAsksMagpieForThatAccount(t *testing.T) {
	calls := fakeMagpie(t, `[{"provider":"claude","user":"one@example.com"}]`, nil)
	a := fakeAccountsApp()

	a.syncAccount("one@example.com")

	if len(*calls) != 1 {
		t.Fatalf("magpie 被调用 %d 次，期望 1 次", len(*calls))
	}
	if got := strings.Join((*calls)[0], " "); got != "quota refresh claude one@example.com --json" {
		t.Errorf("magpie 参数 = %q", got)
	}
	if a.syncing("one@example.com") {
		t.Error("同步结束后还标着同步中")
	}
	if msg := a.syncError("one@example.com"); msg != "" {
		t.Errorf("成功的同步不该留下错误：%s", msg)
	}
}

// magpie 读失败时，原因要留在这个账号上；下一次成功就清掉。
func TestSyncAccountKeepsCardError(t *testing.T) {
	fakeMagpie(t, `[{"provider":"claude","user":"one@example.com","error":"signed out"}]`, nil)
	a := fakeAccountsApp()
	a.syncAccount("one@example.com")
	if got := a.syncError("one@example.com"); got != "signed out" {
		t.Fatalf("同步错误 = %q, 期望 signed out", got)
	}

	fakeMagpie(t, `[{"provider":"claude","user":"one@example.com"}]`, nil)
	a.syncAccount("one@example.com")
	if got := a.syncError("one@example.com"); got != "" {
		t.Errorf("成功之后错误还在：%q", got)
	}
}

// 面板上每个账号都有自己的「同步」按钮，点了就去同步那个账号。
func TestPanelSyncButton(t *testing.T) {
	calls := fakeMagpie(t, `[]`, nil)
	a := fakeAccountsApp()
	tt := ui.NewTester(a.view, 360, 520)

	if err := tt.Click("同步 one@example.com"); err != nil {
		t.Fatalf("点不到账号的「同步」：%v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (len(*calls) == 0 || a.syncing("one@example.com")) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(*calls) != 1 || (*calls)[0][3] != "one@example.com" {
		t.Fatalf("点「同步」后 magpie 调用 = %v", *calls)
	}
}

// 真的去 exec：PATH 上找到的 magpie 要被跑起来，失败时把它最后一行报错带回来。
func TestRunMagpieExec(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$4\" = bad@example.com ]; then echo 'no card of claude · bad@example.com' >&2; exit 1; fi\necho '[{\"user\":\"'\"$4\"'\"}]'\n"
	if err := os.WriteFile(filepath.Join(dir, "magpie"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	if err := requestQuota(context.Background(), "one@example.com"); err != nil {
		t.Errorf("正常账号同步失败：%v", err)
	}
	err := requestQuota(context.Background(), "bad@example.com")
	if err == nil || !strings.Contains(err.Error(), "no card of claude") {
		t.Errorf("失败原因没带回来：%v", err)
	}
}
