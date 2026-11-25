// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/changelog"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/selfupdate"
)

const localReleases = `## [1.1.0] - 2026-10-25

### 修复

- 修了个问题

## [1.0.0] - 2026-10-17

### 新增

- 首版
`

const (
	localChangelog  = "# 更新日志\n\n" + localReleases
	remoteChangelog = "# 更新日志\n\n## [1.2.0] - 2026-11-01\n\n### 新增\n\n- 远端新功能\n\n" + localReleases
)

func getVersion(t *testing.T, p *Panel, query string) versionResp {
	t.Helper()
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/panel/api/version"+query, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("version status=%d body=%s", rr.Code, rr.Body.String())
	}
	var out versionResp
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func releaseVersions(rels []changelog.Release) string {
	vs := make([]string, len(rels))
	for i, r := range rels {
		vs[i] = r.Version
	}
	return strings.Join(vs, ",")
}

func TestVersionEndpoint(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(remoteChangelog))
	}))
	defer srv.Close()
	live := livecfg.New(livecfg.Snapshot{UpdateCheck: true})
	p := New(Config{Version: "1.1.0", Changelog: localChangelog, UpdateURL: srv.URL, Live: live})

	// 还没读到远端：只有本机版本
	got := getVersion(t, p, "")
	if got.Version != "1.1.0" || !got.UpdateCheck || got.UpdateAvailable || got.Latest != "" || got.CheckedAt != nil {
		t.Fatalf("before check = %+v", got)
	}

	p.checkUpdate(context.Background())
	if hits.Load() != 1 {
		t.Fatalf("remote hits=%d want 1", hits.Load())
	}
	got = getVersion(t, p, "?since=1.0.0")
	if got.Latest != "1.2.0" || !got.UpdateAvailable || releaseVersions(got.Updates) != "1.2.0" || got.CheckedAt == nil || got.CheckError != "" {
		t.Fatalf("after check = %+v", got)
	}
	if got.ReleaseURL != releaseTagURL+"1.2.0" {
		t.Fatalf("release_url=%q", got.ReleaseURL)
	}
	// 从 1.0.0 升到 1.1.0：「本次更新」取内嵌日志里的 1.1.0
	if releaseVersions(got.WhatsNew) != "1.1.0" || got.WhatsNew[0].Groups[0].Items[0] != "修了个问题" {
		t.Fatalf("whats_new = %+v", got.WhatsNew)
	}
	// 没有记录（since=none）：只给本机这一版，不带更早的 1.0.0
	if w := getVersion(t, p, "?since=none").WhatsNew; releaseVersions(w) != "1.1.0" {
		t.Fatalf("since=none whats_new = %+v", w)
	}
	// 没升级、降级、since 不合法：都没有「本次更新」
	for _, q := range []string{"", "?since=1.1.0", "?since=1.2.0", "?since=garbage"} {
		if w := getVersion(t, p, q).WhatsNew; len(w) != 0 {
			t.Fatalf("%q whats_new = %+v, want none", q, w)
		}
	}

	// 关掉开关：不再给出远端信息
	live.Store(livecfg.Snapshot{UpdateCheck: false})
	got = getVersion(t, p, "")
	if got.UpdateCheck || got.UpdateAvailable || got.Latest != "" || len(got.Updates) != 0 || got.ReleaseURL != "" {
		t.Fatalf("disabled = %+v", got)
	}
}

func TestCheckUpdateUpToDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(remoteChangelog))
	}))
	defer srv.Close()
	p := New(Config{Version: "1.2.0", UpdateURL: srv.URL, Live: livecfg.New(livecfg.Snapshot{UpdateCheck: true})})
	p.checkUpdate(context.Background())
	got := getVersion(t, p, "")
	if got.Latest != "1.2.0" || got.UpdateAvailable || len(got.Updates) != 0 || got.ReleaseURL != "" {
		t.Fatalf("up to date = %+v", got)
	}
}

// 更新通道过滤：stable 只认正式版；prerelease 认正式版与预发布版，且热切换立即生效并同步限制一键更新。
func TestCheckUpdatePrerelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("## [1.3.0-rc.1] - 2026-11-05\n\n- 试用\n\n" + remoteChangelog[len("# 更新日志\n\n"):]))
	}))
	defer srv.Close()
	for _, c := range []struct{ version, channel, wantChannel, latest, updates string }{
		{"1.1.0", "", livecfg.UpdateChannelStable, "1.2.0", "1.2.0"},
		{"1.2.0", livecfg.UpdateChannelStable, livecfg.UpdateChannelStable, "1.2.0", ""},
		{"1.2.1-rc.1", livecfg.UpdateChannelStable, livecfg.UpdateChannelStable, "1.2.0", ""},
		{"1.1.0", livecfg.UpdateChannelPrerelease, livecfg.UpdateChannelPrerelease, "1.3.0-rc.1", "1.3.0-rc.1,1.2.0"},
		{"1.2.0", livecfg.UpdateChannelPrerelease, livecfg.UpdateChannelPrerelease, "1.3.0-rc.1", "1.3.0-rc.1"},
		{"1.2.1-rc.1", livecfg.UpdateChannelPrerelease, livecfg.UpdateChannelPrerelease, "1.3.0-rc.1", "1.3.0-rc.1"},
	} {
		p := New(Config{Version: c.version, UpdateURL: srv.URL, Live: livecfg.New(livecfg.Snapshot{UpdateCheck: true, UpdateChannel: c.channel})})
		p.checkUpdate(context.Background())
		got := getVersion(t, p, "")
		if got.UpdateChannel != c.wantChannel || got.Latest != c.latest || releaseVersions(got.Updates) != c.updates || got.UpdateAvailable != (c.updates != "") {
			t.Fatalf("version %s channel %q: %+v", c.version, c.channel, got)
		}
	}

	// 热切换通道：无需重拉远端日志即生效，且 POST /panel/api/update 按当前通道放行或拦截预发布版本。
	upd := selfupdate.Launch(t.TempDir(), "1.2.0")
	release := httptest.NewServer(http.NotFoundHandler())
	defer release.Close()
	upd.DownloadBase = release.URL + "/"
	upd.Restart = func() { t.Error("下载失败时不应该重启") }

	live := livecfg.New(livecfg.Snapshot{UpdateCheck: true, UpdateChannel: livecfg.UpdateChannelStable})
	p := New(Config{Version: "1.2.0", UpdateURL: srv.URL, Live: live, Updater: upd})
	p.checkUpdate(context.Background())
	if got := getVersion(t, p, ""); got.UpdateChannel != livecfg.UpdateChannelStable || got.UpdateAvailable || got.Latest != "1.2.0" {
		t.Fatalf("stable before switch = %+v", got)
	}
	if rr := postJSON(t, p, "/panel/api/update", `{"version":"1.3.0-rc.1"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("stable channel should reject prerelease update, status=%d", rr.Code)
	}

	live.Store(livecfg.Snapshot{UpdateCheck: true, UpdateChannel: livecfg.UpdateChannelPrerelease})
	if got := getVersion(t, p, ""); got.UpdateChannel != livecfg.UpdateChannelPrerelease || !got.UpdateAvailable || got.Latest != "1.3.0-rc.1" {
		t.Fatalf("prerelease after hot switch = %+v", got)
	}
	if rr := postJSON(t, p, "/panel/api/update", `{"version":"1.3.0-rc.1"}`); rr.Code != http.StatusAccepted {
		t.Fatalf("prerelease channel should accept prerelease update, status=%d body=%s", rr.Code, rr.Body.String())
	}
	// 等后台下载协程结束，避免与 t.TempDir() 清理竞态。
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s := getVersion(t, p, "").SelfUpdate; s.Phase == selfupdate.PhaseFailed && s.Target == "1.3.0-rc.1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("prerelease self_update did not finish: %+v", getVersion(t, p, "").SelfUpdate)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 读失败只记下原因，保留上一次成功的结果。
func TestCheckUpdateFailureKeepsLastResult(t *testing.T) {
	var body atomic.Value
	body.Store(remoteChangelog)
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()
	p := New(Config{Version: "1.1.0", UpdateURL: srv.URL, Live: livecfg.New(livecfg.Snapshot{UpdateCheck: true})})
	p.checkUpdate(context.Background())

	status.Store(http.StatusNotFound)
	p.checkUpdate(context.Background())
	got := getVersion(t, p, "")
	if got.Latest != "1.2.0" || !got.UpdateAvailable || got.CheckError != "HTTP 404" {
		t.Fatalf("after 404 = %+v", got)
	}

	status.Store(http.StatusOK)
	body.Store("<html>不是更新日志</html>")
	p.checkUpdate(context.Background())
	if got = getVersion(t, p, ""); got.CheckError == "" || got.Latest != "1.2.0" {
		t.Fatalf("after garbage = %+v", got)
	}

	body.Store(strings.Repeat("x", updateMaxBytes+1))
	p.checkUpdate(context.Background())
	if got = getVersion(t, p, ""); !strings.Contains(got.CheckError, "1 MiB") {
		t.Fatalf("after oversize = %+v", got)
	}
}

func postJSON(t *testing.T, p *Panel, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rr
}

func TestVersionCheckNow(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(remoteChangelog))
	}))
	defer srv.Close()
	live := livecfg.New(livecfg.Snapshot{UpdateCheck: false})
	p := New(Config{Version: "1.1.0", UpdateURL: srv.URL, Live: live})
	if rr := postJSON(t, p, "/panel/api/version/check", ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("disabled check status=%d", rr.Code)
	}
	live.Store(livecfg.Snapshot{UpdateCheck: true})
	rr := postJSON(t, p, "/panel/api/version/check", "")
	var got versionResp
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || rr.Code != http.StatusOK || got.Latest != "1.2.0" || !got.UpdateAvailable {
		t.Fatalf("check status=%d body=%s", rr.Code, rr.Body.String())
	}
	// 30 秒内再点：用上次的结果，不再请求远端
	postJSON(t, p, "/panel/api/version/check", "")
	if hits.Load() != 1 {
		t.Fatalf("remote hits=%d want 1", hits.Load())
	}
}

func TestSelfUpdateEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(remoteChangelog))
	}))
	defer srv.Close()

	// 没有接入一键更新：version 不带 self_update，操作接口 501
	p := New(Config{Version: "1.1.0", UpdateURL: srv.URL, Live: livecfg.New(livecfg.Snapshot{UpdateCheck: true})})
	if got := getVersion(t, p, ""); got.SelfUpdate != nil {
		t.Fatalf("self_update = %+v, want none", got.SelfUpdate)
	}
	if rr := postJSON(t, p, "/panel/api/update", `{"version":"1.2.0"}`); rr.Code != http.StatusNotImplemented {
		t.Fatalf("update without updater status=%d", rr.Code)
	}

	upd := selfupdate.Launch(t.TempDir(), "1.1.0")
	release := httptest.NewServer(http.NotFoundHandler()) // Release 里还没有安装包
	defer release.Close()
	upd.DownloadBase = release.URL + "/"
	upd.Restart = func() { t.Error("下载失败时不应该重启") }
	live := livecfg.New(livecfg.Snapshot{UpdateCheck: true})
	p = New(Config{Version: "1.1.0", UpdateURL: srv.URL, Live: live, Updater: upd})
	got := getVersion(t, p, "")
	if got.SelfUpdate == nil || got.SelfUpdate.Base != "1.1.0" || got.SelfUpdate.Override || got.SelfUpdate.Phase != selfupdate.PhaseIdle {
		t.Fatalf("self_update = %+v", got.SelfUpdate)
	}

	// 还没读到远端：没有可以更新到的版本
	if rr := postJSON(t, p, "/panel/api/update", `{"version":"1.2.0"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("update before check status=%d", rr.Code)
	}
	p.checkUpdate(context.Background())
	for _, body := range []string{`{}`, `{"version":"1.1.0"}`, `{"version":"1.3.0"}`, `not json`} {
		if rr := postJSON(t, p, "/panel/api/update", body); rr.Code != http.StatusBadRequest {
			t.Fatalf("update %s status=%d", body, rr.Code)
		}
	}
	live.Store(livecfg.Snapshot{UpdateCheck: false})
	if rr := postJSON(t, p, "/panel/api/update", `{"version":"1.2.0"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("update with check disabled status=%d", rr.Code)
	}
	live.Store(livecfg.Snapshot{UpdateCheck: true})
	if rr := postJSON(t, p, "/panel/api/update", `{"version":"1.2.0"}`); rr.Code != http.StatusAccepted {
		t.Fatalf("update status=%d body=%s", rr.Code, rr.Body.String())
	}
	// 后台下载拿到 404：失败原因出现在 self_update.error
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := getVersion(t, p, "").SelfUpdate
		if s.Phase == selfupdate.PhaseFailed && strings.Contains(s.Error, "CI 可能还在构建") && s.Target == "1.2.0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("self_update = %+v", s)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 没有面板装的版本：无从退回
	if rr := postJSON(t, p, "/panel/api/update/rollback", ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("rollback status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestVersionChannelEndpointAndWhatsNewFilter(t *testing.T) {
	fullChangelog := `# 更新日志

## [1.2.0] - 2026-11-10

### 新增

- 正式版汇总

## [1.2.0-rc.2] - 2026-11-08

### 修复

- RC2 修复

## [1.2.0-rc.1] - 2026-11-05

### 新增

- RC1 试用

## [1.1.0] - 2026-10-25

### 修复

- 旧正式版
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("## [1.3.0-rc.1] - 2026-11-12\n\n- 新预发布\n\n" + fullChangelog[len("# 更新日志\n\n"):]))
	}))
	defer srv.Close()

	// 1) WhatsNew：正式版 → 正式版跳过中间 RC，不重复展示；预发布参与的升级照旧展示中间版本。
	pStable := New(Config{Version: "1.2.0", Changelog: fullChangelog, UpdateURL: srv.URL, Live: livecfg.New(livecfg.Snapshot{UpdateCheck: true})})
	if got := releaseVersions(getVersion(t, pStable, "?since=1.1.0").WhatsNew); got != "1.2.0" {
		t.Fatalf("stable→stable whats_new=%q want 1.2.0", got)
	}
	if got := releaseVersions(getVersion(t, pStable, "?since=1.2.0-rc.1").WhatsNew); got != "1.2.0,1.2.0-rc.2" {
		t.Fatalf("rc→stable whats_new=%q want 1.2.0,1.2.0-rc.2", got)
	}
	pPre := New(Config{Version: "1.2.0-rc.2", Changelog: fullChangelog, UpdateURL: srv.URL, Live: livecfg.New(livecfg.Snapshot{UpdateCheck: true})})
	if got := releaseVersions(getVersion(t, pPre, "?since=1.1.0").WhatsNew); got != "1.2.0-rc.2,1.2.0-rc.1" {
		t.Fatalf("stable→rc whats_new=%q want 1.2.0-rc.2,1.2.0-rc.1", got)
	}

	// 2) POST /panel/api/version/channel：未接入 501；非法参数 400；切换成功落盘并即时返回最新 versionResp。
	if rr := postJSON(t, pStable, "/panel/api/version/channel", `{"channel":"prerelease"}`); rr.Code != http.StatusNotImplemented {
		t.Fatalf("without saver status=%d want 501", rr.Code)
	}

	live := livecfg.New(livecfg.Snapshot{UpdateCheck: true, UpdateChannel: livecfg.UpdateChannelStable})
	var saved string
	p := New(Config{
		Version:   "1.2.0",
		Changelog: fullChangelog,
		UpdateURL: srv.URL,
		Live:      live,
		SaveUpdateChannel: func(ch string) error {
			saved = ch
			snap := live.Load()
			snap.UpdateChannel = ch
			live.Store(snap)
			return nil
		},
	})
	p.checkUpdate(context.Background())

	for _, body := range []string{`not json`, `{}`, `{"channel":"beta"}`} {
		if rr := postJSON(t, p, "/panel/api/version/channel", body); rr.Code != http.StatusBadRequest {
			t.Fatalf("bad body %s status=%d want 400", body, rr.Code)
		}
	}
	rr := postJSON(t, p, "/panel/api/version/channel", `{"channel":"prerelease"}`)
	if rr.Code != http.StatusOK || saved != livecfg.UpdateChannelPrerelease {
		t.Fatalf("switch status=%d saved=%q body=%s", rr.Code, saved, rr.Body.String())
	}
	var got versionResp
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UpdateChannel != livecfg.UpdateChannelPrerelease || !got.UpdateAvailable || got.Latest != "1.3.0-rc.1" {
		t.Fatalf("after channel switch = %+v", got)
	}

	// 持久化失败返回 500 Internal Server Error（而非 400）。
	pFail := New(Config{
		Version: "1.2.0",
		Live:    live,
		SaveUpdateChannel: func(string) error {
			return http.ErrHandlerTimeout
		},
	})
	if rr := postJSON(t, pFail, "/panel/api/version/channel", `{"channel":"stable"}`); rr.Code != http.StatusInternalServerError {
		t.Fatalf("saver error status=%d want 500", rr.Code)
	}
}
