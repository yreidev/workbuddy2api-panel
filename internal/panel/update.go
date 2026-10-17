package panel

// 更新检查：后台定时从 GitHub 读取 main 分支上的 docs/CHANGELOG.md，和本机版本比较，面板据此
// 提醒「有新版本」并列出新版本的条目；升级后的「本次更新」用内嵌的同一份文件取条目，不联网。
// 浏览器受 CSP 限制只能请求本服务，所以由网关代为读取。每次只发一个 GET，不带本机任何信息；
// 读不到就记一行日志，等下一轮。开关是 panel.update_check（经 livecfg 热生效）。
//
// 一键更新（internal/selfupdate）跟着同一个开关：有新版本时面板可以让网关下载、校验并原地重启到新版，
// 也可以退回程序自带的版本。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/changelog"
	"github.com/linguo2625469/workbuddy2api-panel/internal/selfupdate"
)

const (
	defaultUpdateURL = "https://raw.githubusercontent.com/yreidev/workbuddy2api-panel/main/docs/CHANGELOG.md"
	releaseTagURL    = "https://github.com/yreidev/workbuddy2api-panel/releases/tag/v"
	updateInterval   = 6 * time.Hour
	updateTimeout    = 15 * time.Second
	updateMaxBytes   = 1 << 20
	// 面板「立即检查」距上次尝试不到这么久时直接用上次的结果，免得连点打到 GitHub 限流
	manualCheckGap = 30 * time.Second
)

// updateState 最近一次检查的结果。
type updateState struct {
	mu        sync.Mutex
	remote    []changelog.Release // 远端更新日志（从新到旧）
	checkedAt time.Time           // 最近一次成功
	attempted time.Time           // 最近一次尝试（成功或失败）
	err       string              // 最近一次尝试失败的原因；成功后清空
	notified  string              // 已在日志里提示过的最新版本，避免每轮重复
}

// updateCheckEnabled 当前是否开着更新检查（没有 Live 的测试面板视为关闭）。
func (p *Panel) updateCheckEnabled() bool {
	return p.cfg.Live != nil && p.cfg.Live.Load().UpdateCheck
}

// StartUpdateCheck 启动后台更新检查，ctx 结束时退出。每分钟看一次开关，开着且距上次尝试满 6 小时
// 就读一次：启动后约一分钟读第一次，面板里刚打开开关也在一分钟内读到。
func (p *Panel) StartUpdateCheck(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			p.upd.mu.Lock()
			due := time.Since(p.upd.attempted) >= updateInterval
			p.upd.mu.Unlock()
			if due && p.updateCheckEnabled() {
				p.checkUpdate(ctx)
			}
		}
	}()
}

// checkUpdate 读一次远端更新日志并记下结果。
func (p *Panel) checkUpdate(ctx context.Context) {
	p.upd.mu.Lock()
	p.upd.attempted = time.Now()
	p.upd.mu.Unlock()

	rels, err := p.fetchChangelog(ctx)
	announce := ""
	p.upd.mu.Lock()
	if err != nil {
		p.upd.err = err.Error()
	} else {
		p.upd.remote, p.upd.checkedAt, p.upd.err = rels, time.Now(), ""
		if newer := changelog.Newer(p.relevant(rels), p.cfg.Version); len(newer) > 0 && newer[0].Version != p.upd.notified {
			p.upd.notified = newer[0].Version
			announce = newer[0].Version
		}
	}
	p.upd.mu.Unlock()

	if err != nil {
		log.Printf("[update] 检查更新失败：%v（%d 小时后再试）", err, int(updateInterval.Hours()))
	} else if announce != "" {
		log.Printf("[update] 有新版本 v%s（当前 v%s），面板里可以查看更新内容", announce, p.cfg.Version)
	}
}

// relevant 跑正式版的只提醒正式版；自己跑着预发布版的，预发布版本也提醒。
func (p *Panel) relevant(rels []changelog.Release) []changelog.Release {
	if changelog.Prerelease(p.cfg.Version) {
		return rels
	}
	return changelog.Stable(rels)
}

// fetchChangelog GET 远端更新日志并解析。
func (p *Panel) fetchChangelog(ctx context.Context) ([]changelog.Release, error) {
	url := p.cfg.UpdateURL
	if url == "" {
		url = defaultUpdateURL
	}
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, updateMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > updateMaxBytes {
		return nil, errors.New("更新日志超过 1 MiB")
	}
	rels := changelog.Parse(string(body))
	if len(rels) == 0 {
		return nil, errors.New("更新日志里没有可识别的版本")
	}
	return rels, nil
}

// versionResp GET /panel/api/version 的返回。
type versionResp struct {
	Version     string `json:"version"`
	UpdateCheck bool   `json:"update_check"`
	// 以下几项只在更新检查开着时给出
	Latest          string              `json:"latest,omitempty"` // 远端最新版本；还没读到过时为空
	UpdateAvailable bool                `json:"update_available"`
	Updates         []changelog.Release `json:"updates"` // 比本机新的版本，从新到旧
	ReleaseURL      string              `json:"release_url,omitempty"`
	CheckedAt       *time.Time          `json:"checked_at,omitempty"`
	CheckError      string              `json:"check_error,omitempty"`
	// WhatsNew 从 since 升级到本机版本所包含的版本（取内嵌的更新日志）；since=none 时只有本机这一版；
	// 没给 since 或没升级时为空
	WhatsNew []changelog.Release `json:"whats_new"`
	// SelfUpdate 一键更新的状态；没有接入时不给出
	SelfUpdate *selfupdate.Status `json:"self_update,omitempty"`
}

// version 版本与更新信息：面板的更新提醒和「本次更新」弹窗都读这里。
func (p *Panel) version(w http.ResponseWriter, r *http.Request) {
	out := versionResp{Version: p.cfg.Version, UpdateCheck: p.updateCheckEnabled()}
	switch since := r.URL.Query().Get("since"); {
	case changelog.Valid(since):
		out.WhatsNew = changelog.Between(p.releases, since, p.cfg.Version)
	case since == "none":
		// 这个浏览器没看过任何版本（新装，或从还没有更新提醒的旧版升上来）：只给本机这一版，不翻出全部历史
		for _, rel := range p.releases {
			if changelog.Compare(rel.Version, p.cfg.Version) == 0 {
				out.WhatsNew = []changelog.Release{rel}
				break
			}
		}
	}
	if out.UpdateCheck {
		p.upd.mu.Lock()
		remote, checkedAt, checkErr := p.upd.remote, p.upd.checkedAt, p.upd.err
		p.upd.mu.Unlock()
		remote = p.relevant(remote)
		if len(remote) > 0 {
			out.Latest = remote[0].Version
			out.Updates = changelog.Newer(remote, p.cfg.Version)
		}
		if out.UpdateAvailable = len(out.Updates) > 0; out.UpdateAvailable {
			out.ReleaseURL = releaseTagURL + out.Latest
		}
		if !checkedAt.IsZero() {
			out.CheckedAt = &checkedAt
		}
		out.CheckError = checkErr
	}
	if p.cfg.Updater != nil {
		s := p.cfg.Updater.Status()
		out.SelfUpdate = &s
	}
	writeJSON(w, http.StatusOK, out)
}

// versionCheck 面板「立即检查」：马上读一次远端更新日志，返回和 version 一样的结果。
func (p *Panel) versionCheck(w http.ResponseWriter, r *http.Request) {
	if !p.updateCheckEnabled() {
		writeErr(w, http.StatusBadRequest, "更新检查已关闭，请先在配置页打开「检查更新」")
		return
	}
	p.upd.mu.Lock()
	recent := time.Since(p.upd.attempted) < manualCheckGap
	p.upd.mu.Unlock()
	if !recent {
		p.checkUpdate(r.Context())
	}
	p.version(w, r)
}

// selfUpdate 一键更新到 body.version：必须是更新检查读到的、比本机新的版本。下载、校验在后台进行，
// 进度看 version 接口的 self_update；全部通过后网关优雅停机并原地重启。
func (p *Panel) selfUpdate(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Updater == nil {
		writeErr(w, http.StatusNotImplemented, "没有接入一键更新")
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version == "" {
		writeErr(w, http.StatusBadRequest, "缺少 version")
		return
	}
	if !p.updateCheckEnabled() {
		writeErr(w, http.StatusBadRequest, "更新检查已关闭，请先在配置页打开「检查更新」")
		return
	}
	p.upd.mu.Lock()
	newer := changelog.Newer(p.relevant(p.upd.remote), p.cfg.Version)
	p.upd.mu.Unlock()
	if !slices.ContainsFunc(newer, func(rel changelog.Release) bool { return rel.Version == body.Version }) {
		writeErr(w, http.StatusBadRequest, "v"+body.Version+" 不是可以更新到的版本")
		return
	}
	if err := p.cfg.Updater.Start(body.Version); err != nil {
		writeErr(w, updaterErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// selfUpdateRollback 删掉面板装的版本：正跑着它就重启回程序自带的版本（restarting=true）。
func (p *Panel) selfUpdateRollback(w http.ResponseWriter, r *http.Request) {
	if p.cfg.Updater == nil {
		writeErr(w, http.StatusNotImplemented, "没有接入一键更新")
		return
	}
	restarting, err := p.cfg.Updater.Rollback()
	if err != nil {
		writeErr(w, updaterErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarting": restarting})
}

func updaterErrStatus(err error) int {
	if errors.Is(err, selfupdate.ErrBusy) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}
