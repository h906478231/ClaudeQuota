package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// view 是窗口显示的原生界面，主线程在需要一帧时调用它。
func (a *app) view(c *ui.Context) {
	t := c.Theme()
	accounts, failed := a.state()
	now := time.Now()

	ui.Column(c).Fill().Children(func() {
		a.header(c, t, accounts, now)
		ui.Divider(c)
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Fill().Padding(14).Gap(4).Children(func() {
				if failed != "" {
					a.failureBanner(c, t, failed)
				}
				if len(accounts) == 0 && failed == "" {
					a.empty(c, t)
				}
				// 本周用完的账号另起一栏，别和还能用的混在一起。
				split := firstWeeklyFull(accounts)
				for i := range accounts {
					if i == split {
						a.groupHeader(c, t, "本周额度已用尽", len(accounts)-split)
					}
					a.accountSection(c, t, accounts[i], now)
				}
			})
		})
		ui.Divider(c)
		a.footer(c, t, accounts)
	})
}

func (a *app) header(c *ui.Context, t *ui.Theme, accounts []accountQuota, now time.Time) {
	newest := time.Time{}
	for _, acc := range accounts {
		if acc.ReadAt.After(newest) {
			newest = acc.ReadAt
		}
	}

	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(14).Children(func() {
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, "Claude 套餐额度").FontSize(15).Bold()
			// 副标题整行灰色，不用徽章：标题下面只是一句说明。
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("%d 个账号", len(accounts))).FontSize(11).TextColor(t.TextMuted)
				ui.Text(c, "· 数据 "+ageText(newest, now)).FontSize(11).TextColor(t.TextMuted)
			})
		})
		if ui.Button(c, "刷新").Clicked() {
			go a.refresh()
		}
	})
}

// 紧凑版的行宽，加起来正好是面板内容宽度（360 - 两侧 14）。
const (
	labelWidth    float32 = 24
	barWidth      float32 = 166
	percentWidth  float32 = 40
	countdownWide float32 = 80
	// 进度条比默认的 8 点细一些，颜色留给右边的百分比去说话。
	meterHeight float32 = 5
)

// accountSection 画一个账号，压到最省地方：
// 一行账号信息，之后每个额度窗口各占一行。
func (a *app) accountSection(c *ui.Context, t *ui.Theme, acc accountQuota, now time.Time) {
	dim := acc.stale(now)

	section := ui.Column(c).Padding(0, 14).Gap(3)
	// 本周用尽的账号整块变淡：还在列表里，但一眼看出暂时用不上。
	if acc.isWeeklyFull() {
		section.Opacity(0.55)
	}
	section.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			emailColor := t.Text
			if dim {
				emailColor = t.TextMuted
			}
			// 邮箱太长时省略号截断，给右边的「同步」让位。
			ui.Text(c, acc.Email).FontSize(12).Bold().TextColor(emailColor).SingleLine().Shrink(1).MinWidth(0)
			if acc.Plan != "" {
				ui.Badge(c, acc.Plan)
			}
			if !acc.ServedAt.IsZero() && now.Sub(acc.ServedAt) < 30*time.Minute {
				// 唯一带颜色的徽章，其余账号信息一律灰色。
				ui.Badge(c, "● 正在使用").
					Background(t.Success.Alpha(0.14)).
					TextColor(t.Success)
			}
			ui.Spacer(c)
			if a.syncing(acc.Email) {
				ui.Spinner(c).Label("正在同步 " + acc.Email)
				ui.Text(c, "同步中").FontSize(10).TextColor(t.TextMuted)
				return
			}
			ui.Text(c, ageTextShort(acc.ReadAt, now)).FontSize(10).TextColor(t.TextMuted).Shrink(0)
			// 和顶部「刷新」不同：这个是真的让 magpie 去问一次这个账号。
			email := acc.Email
			if ui.Button(c, "同步").FontSize(10).Padding(1, 6).Shrink(0).
				Label("同步 " + email).
				Tooltip("让 magpie 立即重新读取这个账号的额度").
				Clicked() {
				go a.syncAccount(email)
			}
		})
		if msg := a.syncError(acc.Email); msg != "" {
			ui.Text(c, "同步失败："+msg).FontSize(10).TextColor(t.Warning)
		}

		for _, item := range acc.Windows {
			w := item
			label := shortLabel(w)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, label).Width(labelWidth).FontSize(10).TextColor(t.TextMuted)
				ui.Meter(c, w.Used, 0, 100, meterLevels).
					Width(barWidth).
					Height(meterHeight).
					Label(fmt.Sprintf("%s %s %.0f%%", acc.Email, w.title(), w.Used))
				ui.Text(c, fmt.Sprintf("%.0f%%", w.Used)).
					Width(percentWidth).TextAlign(ui.End).
					FontSize(13).Bold().TextColor(levelColor(t, w.Level))
				ui.Text(c, shortCountdown(countdown(w.ResetsAt, now))).
					Width(countdownWide).TextAlign(ui.End).
					FontSize(10).TextColor(t.TextMuted)
			})
		}
	})
	ui.Divider(c)
}

// groupHeader 是分组之间的分栏标题，把用尽的账号和还能用的分开。
func (a *app) groupHeader(c *ui.Context, t *ui.Theme, title string, count int) {
	ui.Column(c).Padding(8, 0).Gap(4).Children(func() {
		ui.Divider(c)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(2, 14).Children(func() {
			ui.Text(c, title).FontSize(11).Bold().TextColor(t.TextMuted)
			ui.Badge(c, fmt.Sprintf("%d 个", count))
		})
	})
}

// shortLabel 把窗口名压成 5h / 7d：密集列表里这样最省地方，
// 也免得和底部分段控件上的「5 小时」「每周」撞名字。
func shortLabel(w quotaWindow) string {
	if w.isSession() {
		return "5h"
	}
	return "7d"
}

// shortCountdown 去掉「后重置」，省下的位置留给账号列表。
func shortCountdown(text string) string {
	if text == "" {
		return "—"
	}
	return strings.TrimSuffix(text, "后重置")
}

func (a *app) failureBanner(c *ui.Context, t *ui.Theme, message string) {
	ui.Row(c).Gap(6).AlignItems(ui.Start).Padding(10, 14).Children(func() {
		ui.Text(c, "⚠").FontSize(11).TextColor(t.Warning)
		ui.Text(c, message).FontSize(11).TextColor(t.Warning).Grow(1)
	})
}

func (a *app) empty(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Padding(20, 14).Gap(6).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Label("正在读取")
			ui.Text(c, "正在读取 magpie 的额度数据…").FontSize(11).TextColor(t.TextMuted)
		})
	})
}

// footer 是三个可调项：菜单栏显示什么、盯哪个账号、多久刷新一次。
func (a *app) footer(c *ui.Context, t *ui.Theme, accounts []accountQuota) {
	ui.Column(c).Padding(11, 14).Gap(7).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "菜单栏显示").FontSize(11).TextColor(t.TextMuted)
			// 控件直接绑持久字段，Changed 报告这一帧有没有换选。
			if ui.Segmented(c, &a.labelModeValue, labelModes...).Changed() {
				a.setLabelMode(a.labelModeValue)
			}
		})

		// 两个下拉各占一行：并排会把标签挤成竖排单字。
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "监控账号").Width(52).FontSize(10).TextColor(t.TextMuted)
			a.accountSelect(c, t, accounts)
		})

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "刷新间隔").Width(52).FontSize(10).TextColor(t.TextMuted)
			labels := make([]string, len(intervalChoices))
			for i, choice := range intervalChoices {
				labels[i] = choice.Label
			}
			if ui.Select(c, &a.intervalValue, labels).Width(96).Changed() {
				for i, label := range labels {
					if label == a.intervalValue {
						a.setInterval(i)
						break
					}
				}
			}
			ui.Spacer(c)
			ui.Button(c, "退出").OnClick(mygo.App.Quit)
		})

		ui.Text(c, "「刷新」只重读本地文件，「同步」让 magpie 立即去问一次。").
			FontSize(9).TextColor(t.TextMuted)
	})
}

// accountSelect 是「监控账号」下拉。这里没用现成的 ui.Select，
// 因为它的选项只能是纯字符串，中间插不进一行分栏标题；
// 本周用尽的账号要单独一段，就得自己拼弹层。
// 外观照抄内置 Select 用的同一批主题量，免得两个下拉长得不一样。
func (a *app) accountSelect(c *ui.Context, t *ui.Theme, accounts []accountQuota) {
	// 账号列表可能刚变过，选中的名字不在选项里就先归位。
	options := a.accountOptions(accounts)
	if !slices.Contains(options, a.menuAccountValue) {
		a.menuAccountValue = a.accountChoice()
	}

	sel := ui.SelectBase(c, &a.menuAccountValue)
	// 不额外设可访问名：旁边的「监控账号」就是它的名字，设重了会读两遍。
	sel.Trigger.
		Grow(1).
		MinWidth(t.Space(35)).
		Padding(t.Space(1.5), t.Space(3.5)).
		Gap(t.Space(1.5)).
		Radius(t.Radius).
		Background(t.Surface).
		Border(1, t.Border).
		Justify(ui.SpaceBetween).
		Children(func() {
			ui.Text(c, a.menuAccountValue).SingleLine()
			ui.Text(c, "▾").TextColor(t.TextMuted)
		})

	split := firstWeeklyFull(accounts)
	if split < 0 {
		split = len(accounts)
	}

	// item 和内置 Select 的选项长得一样：高亮时用强调色，选中时用面板色。
	item := func(value string) {
		el := sel.Item(value).Padding(t.Space(1.5), t.Space(2.5)).Radius(t.Radius)
		switch {
		case el.Highlighted():
			el.Background(t.Accent).TextColor(t.AccentText)
		case value == a.menuAccountValue:
			el.Background(t.Surface)
		}
		el.Children(func() { ui.Text(c, value).SingleLine() })
	}

	sel.Popup(func(panel ui.Element) {
		panel.Margin(t.Space(1), 0, 0, 0).
			Padding(t.Space(1)).
			Radius(t.Radius+2).
			Background(t.Background).
			Border(1, t.Border)

		item(mostConstrainedLabel)
		item(currentLabel)
		for i := 0; i < split; i++ {
			item(accounts[i].Email)
		}
		if split < len(accounts) {
			// 这一行不是 Item，选不中，只当分栏标题。
			ui.Text(c, "本周已用尽").
				Padding(t.Space(1.5), t.Space(2.5)).
				FontSize(10).Bold().TextColor(t.TextMuted)
			for i := split; i < len(accounts); i++ {
				item(accounts[i].Email)
			}
		}
	})

	if sel.Trigger.Changed() {
		a.chooseAccount(a.menuAccountValue, accounts)
	}
}

// accountOptions 是下拉里的全部可选项，顺序和列表一致。
func (a *app) accountOptions(accounts []accountQuota) []string {
	options := make([]string, 0, len(accounts)+2)
	options = append(options, mostConstrainedLabel, currentLabel)
	for _, acc := range accounts {
		options = append(options, acc.Email)
	}
	return options
}

// accountChoice 是当前选择对应的下拉文字。
func (a *app) accountChoice() string {
	return a.choiceLabel(int(a.menuAccount.Load()))
}

// chooseAccount 把下拉里选中的文字翻回内部选择。
func (a *app) chooseAccount(choice string, accounts []accountQuota) {
	switch choice {
	case mostConstrainedLabel:
		a.setMenuAccount(mostConstrainedChoice)
		return
	case currentLabel:
		a.setMenuAccount(currentChoice)
		return
	}
	for i, acc := range accounts {
		if acc.Email == choice {
			a.setMenuAccount(i)
			return
		}
	}
}

func (a *app) intervalLabel() string {
	idx := int(a.intervalIdx.Load())
	if idx < 0 || idx >= len(intervalChoices) {
		idx = 1
	}
	return intervalChoices[idx].Label
}

func levelColor(t *ui.Theme, l level) ui.Color {
	switch l {
	case levelWarning:
		return t.Warning
	case levelCritical, levelExceeded:
		return t.Danger
	}
	return t.Success
}

var meterLevels = &ui.MeterLevels{Warning: 75, Critical: 90}
