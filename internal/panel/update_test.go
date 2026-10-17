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

	"github.com/linguo2625469/workbuddy2api-panel/internal/changelog"
	"github.com/linguo2625469/workbuddy2api-panel/internal/livecfg"
	"github.com/linguo2625469/workbuddy2api-panel/internal/selfupdate"
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

// 跑正式版的不提醒预发布版本；跑预发布版的照常提醒。
func TestCheckUpdatePrerelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("## [1.3.0-rc.1] - 2026-11-05\n\n- 试用\n\n" + remoteChangelog[len("# 更新日志\n\n"):]))
	}))
	defer srv.Close()
	for _, c := range []struct{ version, latest, updates string }{
		{"1.1.0", "1.2.0", "1.2.0"},
		{"1.2.0", "1.2.0", ""},
		{"1.2.1-rc.1", "1.3.0-rc.1", "1.3.0-rc.1"},
	} {
		p := New(Config{Version: c.version, UpdateURL: srv.URL, Live: livecfg.New(livecfg.Snapshot{UpdateCheck: true})})
		p.checkUpdate(context.Background())
		got := getVersion(t, p, "")
		if got.Latest != c.latest || releaseVersions(got.Updates) != c.updates || got.UpdateAvailable != (c.updates != "") {
			t.Fatalf("version %s: %+v", c.version, got)
		}
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
