package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 单账号同步：让 magpie 真的去读一次这个账号的额度，而不是只重读它的文件。
//
// 走的是 magpie Usage 页上每张卡的刷新按钮那条路（网关的
// POST /v1/magpie/quotas/refresh，新版也有命令行 `magpie quota refresh`）：
// 在那个账号自己的配置目录里跑 Claude Code 的 /usage，读到的结果写回 quotas.json。我们只是替用户敲这条命令，凭据仍由 magpie 和
// Claude Code 自己管，这边一个 token 都不碰。
// magpie 对同一张卡 30 秒内不会重复去问，连点也不会多打扰。

// syncTimeout 比 magpie 自己的 45 秒多留一点，让它先把超时报出来。
const syncTimeout = 60 * time.Second

// magpieCandidates 是 magpie 命令行可能在的位置。
// 从 Finder 或登录项启动的 app 拿不到 shell 的 PATH，所以安装脚本放的
// ~/.local/bin、Homebrew 和 app 包里的可执行文件都要自己找一遍。
func magpieCandidates() []string {
	var paths []string
	if p, err := exec.LookPath("magpie"); err == nil {
		paths = append(paths, p)
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		paths = append(paths, filepath.Join(home, ".local", "bin", "magpie"))
	}
	paths = append(paths, "/opt/homebrew/bin/magpie", "/usr/local/bin/magpie")
	for _, apps := range []string{"/Applications", filepath.Join(home, "Applications")} {
		for _, name := range []string{"magpie.app", "Magpie.app"} {
			paths = append(paths, filepath.Join(apps, name, "Contents", "MacOS", "magpie"))
		}
	}
	return paths
}

func findMagpie() (string, error) {
	for _, p := range magpieCandidates() {
		if info, err := os.Stat(p); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", errors.New("找不到 magpie 命令行，没法发起同步")
}

// runMagpie 跑一条 magpie 命令，返回标准输出。测试里换成假的。
var runMagpie = func(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := findMagpie()
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("magpie 太久没回应，已放弃")
		}
		if msg := lastLine(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// requestQuota 让 magpie 重新读 email 这个账号，读不到时返回原因。
// 先走 magpie 网关（magpie 应用开着就有，旧版也支持），
// 网关不在或太旧时再用命令行（magpie 新版才有 quota refresh）。
func requestQuota(ctx context.Context, email string) error {
	err := requestViaGateway(ctx, email)
	if !errors.Is(err, errNoGateway) {
		return err
	}
	return requestViaCLI(ctx, email)
}

// errNoGateway 表示网关没开或不认识这条路径，该换命令行试。
var errNoGateway = errors.New("magpie 网关不可用")

// quotaCard 是 magpie 回给我们的一张额度卡里用得上的部分。
type quotaCard struct {
	Provider string `json:"provider"`
	User     string `json:"user"`
	Error    string `json:"error"`
}

// cardError 找出这个账号那张卡上的错误。
func cardError(cards []quotaCard, email string) error {
	for _, card := range cards {
		if card.Provider != "" && card.Provider != "claude" {
			continue
		}
		if card.Error != "" && (card.User == "" || strings.EqualFold(card.User, email)) {
			return errors.New(card.Error)
		}
	}
	return nil
}

// gatewayURL 是 magpie 网关的地址。端口在 magpie 设置里可以改，
// 没改过就是默认的 3425；MAGPIE_ADDR 优先。测试里换掉。
var gatewayURL = func() string {
	if addr := os.Getenv("MAGPIE_ADDR"); addr != "" {
		if !strings.Contains(addr, "://") {
			addr = "http://" + addr
		}
		return strings.TrimSuffix(addr, "/")
	}
	port := 3425
	if data, err := os.ReadFile(filepath.Join(magpieDir(), "settings.json")); err == nil {
		var s struct {
			Port int `json:"port"`
		}
		if json.Unmarshal(data, &s) == nil && s.Port > 0 {
			port = s.Port
		}
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// requestViaGateway 就是 magpie Usage 页卡片上那个刷新按钮发的请求：
// 本机访问不需要密钥。
func requestViaGateway(ctx context.Context, email string) error {
	base := gatewayURL()
	if base == "" {
		return errNoGateway
	}
	q := url.Values{"provider": {"claude"}, "user": {email}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/magpie/quotas/refresh?"+q.Encode(), nil)
	if err != nil {
		return errNoGateway
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("magpie 太久没回应，已放弃")
		}
		return errNoGateway
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return errNoGateway // 这版 magpie 还没有这条路径
	case resp.StatusCode != http.StatusOK:
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
			return errors.New(e.Error.Message)
		}
		return fmt.Errorf("magpie 网关返回 %d", resp.StatusCode)
	}
	var list struct {
		Data []quotaCard `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("看不懂 magpie 的回复：%w", err)
	}
	return cardError(list.Data, email)
}

func requestViaCLI(ctx context.Context, email string) error {
	out, err := runMagpie(ctx, "quota", "refresh", "claude", email, "--json")
	if err != nil {
		// 旧版 magpie 把 refresh 当成了服务商名字。
		if strings.Contains(err.Error(), `no provider "refresh"`) {
			return errors.New("这版 magpie 不支持单账号刷新：请打开 magpie 应用，或把 magpie 更新到最新版")
		}
		return err
	}
	var cards []quotaCard
	if err := json.Unmarshal(out, &cards); err != nil {
		return fmt.Errorf("看不懂 magpie 的回复：%w", err)
	}
	return cardError(cards, email)
}

// syncing 说这个账号是不是正在同步。
func (a *app) syncing(email string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.syncBusy[email]
}

// syncError 是这个账号上一次同步失败的原因，成功过就是空的。
func (a *app) syncError(email string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.syncErrs[email]
}

// syncAccount 真的去问一次这个账号的额度，再把 magpie 的文件重读进来。
// 同一个账号还在同步时再点不会重复发起。
func (a *app) syncAccount(email string) {
	a.mu.Lock()
	if a.syncBusy[email] {
		a.mu.Unlock()
		return
	}
	if a.syncBusy == nil {
		a.syncBusy = map[string]bool{}
	}
	a.syncBusy[email] = true
	a.mu.Unlock()
	a.redraw()

	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	err := requestQuota(ctx, email)
	cancel()

	a.mu.Lock()
	delete(a.syncBusy, email)
	if a.syncErrs == nil {
		a.syncErrs = map[string]string{}
	}
	if err != nil {
		a.syncErrs[email] = err.Error()
	} else {
		delete(a.syncErrs, email)
	}
	a.mu.Unlock()

	// 失败也重读一遍：magpie 可能留下了更新过的快照。
	a.refresh()
}
