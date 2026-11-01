// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package proxies

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// t0 测试的起始时间。
var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// testReg 带假时钟、假探测的代理池。
type testReg struct {
	*Registry
	dir string

	// fmu 管假时钟和假探测的状态（和内嵌的 Registry.mu 分开）。
	fmu   sync.Mutex
	clock time.Time
	gates map[string]string
	exits map[string]*Exit // 代理地址 → 查到的出口（"" = 没绑代理的号）
	// fail 测连通时这些地址不通。
	fail map[string]bool
	// tested 测连通测过的（地址 + 入口）。
	tested []string
}

func newReg(t *testing.T) *testReg {
	t.Helper()
	dir := t.TempDir()
	r, err := Open(filepath.Join(dir, "proxies.json"))
	if err != nil {
		t.Fatal(err)
	}
	tr := &testReg{Registry: r, dir: dir, clock: t0, gates: map[string]string{}, exits: map[string]*Exit{}, fail: map[string]bool{}}
	r.now = tr.now
	r.probe = tr.probeFn
	r.SetGateHook(func(uid, reason string) {
		tr.fmu.Lock()
		defer tr.fmu.Unlock()
		tr.gates[uid] = reason
	})
	r.SetProbes(tr.test, map[string]string{"cn": "cn.example", "global": "global.example"}, "")
	return tr
}

func (tr *testReg) now() time.Time {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	return tr.clock
}

func (tr *testReg) advance(d time.Duration) {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	tr.clock = tr.clock.Add(d)
}

func (tr *testReg) gate(uid string) string {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	return tr.gates[uid]
}

func (tr *testReg) probeFn(_ context.Context, rawURL string, _ []string) *Exit {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	if e, ok := tr.exits[rawURL]; ok {
		c := *e
		return &c
	}
	return &Exit{Error: "查不到"}
}

func (tr *testReg) test(_ context.Context, rawURL, host string) (time.Duration, error) {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	tr.tested = append(tr.tested, rawURL+"@"+host)
	if tr.fail[rawURL] {
		return 0, errTest
	}
	return 120 * time.Millisecond, nil
}

func (tr *testReg) setFail(rawURL string, on bool) {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	tr.fail[rawURL] = on
}

func (tr *testReg) setExit(rawURL, ip string) {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	tr.exits[rawURL] = &Exit{IP: ip, Country: "中国", Region: "香港", ISP: "某运营商"}
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errTest = testErr("连不上代理")

// takeTested 取出测过的「地址@入口」（排好序）并清空。
func (tr *testReg) takeTested() []string {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	out := slices.Clone(tr.tested)
	slices.Sort(out)
	tr.tested = nil
	return out
}

func (tr *testReg) testedN() int {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	return len(tr.tested)
}

func (tr *testReg) delExit(rawURL string) {
	tr.fmu.Lock()
	defer tr.fmu.Unlock()
	delete(tr.exits, rawURL)
}

// waitFor 等 ok 成立，最多 5 秒。
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("等不到：%s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// acct 造一个号（凭证文件放在临时目录里，绑定改了会写回去）。
func (tr *testReg) acct(t *testing.T, uid, realm string) *auth.Auth {
	t.Helper()
	a := &auth.Auth{UID: uid, AccessToken: "at-" + uid, FilePath: filepath.Join(tr.dir, "workbuddy-"+uid+".json")}
	if _, err := auth.BackfillRealmFor(a, realm); err != nil {
		t.Fatal(err)
	}
	return a
}

// pool 建一个池，返回 ID。
func (tr *testReg) pool(t *testing.T, in PoolInput) string {
	t.Helper()
	id, err := tr.CreatePool(in)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// add 往池里导入代理，返回新代理的 ID。
func (tr *testReg) add(t *testing.T, pool string, lines ...string) []string {
	t.Helper()
	text := ""
	for _, l := range lines {
		text += l + "\n"
	}
	res, err := tr.Import(text, "http", pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != len(lines) {
		t.Fatalf("导入 %d 行只加了 %d 个: %+v", len(lines), len(res.Added), res.Lines)
	}
	return res.Added
}

// captureLog 收集 log 输出。
func captureLog(t *testing.T) *lockedBuf {
	t.Helper()
	b := &lockedBuf{}
	old := log.Writer()
	log.SetOutput(b)
	t.Cleanup(func() { log.SetOutput(old) })
	return b
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
