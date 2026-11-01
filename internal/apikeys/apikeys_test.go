// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package apikeys

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// t0 北京时间 2026-10-20 12:00。
var t0 = time.Date(2026, 10, 20, 4, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "api_keys.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func mustCreate(t *testing.T, s *Store, in Input) Key {
	t.Helper()
	k, err := s.Create(in, t0)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func readFile(t *testing.T, path string) file {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestCreate 新建：整理输入、生成 id 和 sk- 原文、立即写盘（0600），能按原文查到。
func TestCreate(t *testing.T) {
	s, path := newStore(t)
	if s.Any() {
		t.Fatal("空库 Any 应为 false")
	}
	k := mustCreate(t, s, Input{Name: "  张三 ", Note: " 朋友 ", Enabled: true, Models: []string{" glm-* ", "", "GLM-*", "deepseek-v4.1-flash"}})
	if k.Name != "张三" || k.Note != "朋友" || !k.Enabled || len(k.ID) != 9 || !strings.HasPrefix(k.ID, "k") {
		t.Fatalf("k = %+v", k)
	}
	if !strings.HasPrefix(k.Secret, "sk-") || len(k.Secret) != 35 {
		t.Fatalf("secret = %q", k.Secret)
	}
	if strings.Join(k.Models, ",") != "glm-*,deepseek-v4.1-flash" {
		t.Fatalf("白名单应去空行、不区分大小写去重: %v", k.Models)
	}
	if !k.CreatedAt.Equal(t0) || k.Day != "2026-10-20" {
		t.Fatalf("created=%v day=%s", k.CreatedAt, k.Day)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("文件应已写入且为 0600: %v %v", st, err)
	}
	if f := readFile(t, path); f.Version != fileVersion || len(f.Keys) != 1 || f.Keys[0].Secret != k.Secret {
		t.Fatalf("file = %+v", f)
	}
	got, ok := s.Lookup(k.Secret)
	if !ok || got.ID != k.ID {
		t.Fatalf("Lookup = %+v %v", got, ok)
	}
	if _, ok := s.Lookup("sk-nope"); ok {
		t.Fatal("错的原文不该查到")
	}
	if _, ok := s.Lookup(""); ok {
		t.Fatal("空原文不该查到")
	}
	if !s.Any() {
		t.Fatal("建过密钥 Any 应为 true")
	}
	// 返回的是副本：改了不影响库里
	got.Models[0] = "改坏"
	if again, _ := s.Lookup(k.Secret); again.Models[0] != "glm-*" {
		t.Fatalf("库里的白名单被调用方改了: %v", again.Models)
	}
}

// TestCreateInvalid 输入不合法时报错、不写盘。
func TestCreateInvalid(t *testing.T) {
	s, path := newStore(t)
	mustCreate(t, s, Input{Name: "已有"})
	long := strings.Repeat("长", maxName+1)
	many := make([]string, maxModels+1)
	for i := range many {
		many[i] = "m" + strings.Repeat("x", i)
	}
	for name, tc := range map[string]struct {
		in   Input
		want string
	}{
		"空名字":    {Input{Name: "  "}, "不能为空"},
		"名字太长":   {Input{Name: long}, "最多"},
		"备注太长":   {Input{Name: "a", Note: strings.Repeat("字", maxNote+1)}, "备注"},
		"重名":     {Input{Name: "已有"}, "已经有"},
		"负数上限":   {Input{Name: "b", DailyLimit: Limits{Requests: -1}}, "负数"},
		"非数积分":   {Input{Name: "c", TotalLimit: Limits{Credits: math.NaN()}}, "负数"},
		"无穷积分":   {Input{Name: "d", TotalLimit: Limits{Credits: math.Inf(1)}}, "负数"},
		"带前缀":    {Input{Name: "e", Models: []string{"cn:glm-5.2"}}, "前缀"},
		"白名单项太长": {Input{Name: "f", Models: []string{strings.Repeat("m", maxPattern+1)}}, "最长"},
		"白名单太多":  {Input{Name: "g", Models: many}, "最多"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Create(tc.in, t0); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v，应含 %q", err, tc.want)
			}
		})
	}
	mustCreate(t, s, Input{Name: "Bob"})
	if _, err := s.Create(Input{Name: "bob"}, t0); err == nil {
		t.Fatal("名字不区分大小写也算重名")
	}
	if n := len(readFile(t, path).Keys); n != 2 {
		t.Fatalf("不合法的输入不该写盘：%d 个", n)
	}
}

// TestUpdateRegenerateDelete 改设置不动原文和用量；重新生成后旧原文失效；删除后查不到。
func TestUpdateRegenerateDelete(t *testing.T) {
	s, path := newStore(t)
	a := mustCreate(t, s, Input{Name: "a", Enabled: true})
	b := mustCreate(t, s, Input{Name: "b", Enabled: true})
	if d := s.Admit(a.ID, t0); d != nil {
		t.Fatal(d)
	}
	exp := t0.Add(48 * time.Hour)
	u, err := s.Update(a.ID, Input{Name: "a2", Enabled: false, ExpiresAt: exp, DailyLimit: Limits{Credits: 1.5}, Models: []string{"glm-*"}})
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "a2" || u.Enabled || !u.ExpiresAt.Equal(exp) || u.DailyLimit.Credits != 1.5 || u.Secret != a.Secret || u.Used.Requests != 1 {
		t.Fatalf("u = %+v", u)
	}
	if _, err := s.Update(a.ID, Input{Name: "B"}); err == nil {
		t.Fatal("改成别人的名字应报错")
	}
	if _, err := s.Update(a.ID, Input{Name: ""}); err == nil {
		t.Fatal("空名字应报错")
	}
	if _, err := s.Update("k_none", Input{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Update(a.ID, Input{Name: "A2", Enabled: true}); err != nil {
		t.Fatalf("改自己名字的大小写不算重名: %v", err)
	}

	r, err := s.Regenerate(a.ID)
	if err != nil || r.Secret == a.Secret || !strings.HasPrefix(r.Secret, "sk-") {
		t.Fatalf("r = %+v err=%v", r, err)
	}
	if _, ok := s.Lookup(a.Secret); ok {
		t.Fatal("旧原文应失效")
	}
	if got, ok := s.Lookup(r.Secret); !ok || got.ID != a.ID {
		t.Fatal("新原文应能查到")
	}
	if _, err := s.Regenerate("k_none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}

	if err := s.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(b.Secret); ok {
		t.Fatal("删掉的密钥不该查到")
	}
	if err := s.Delete(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := s.Get(b.ID, t0); ok {
		t.Fatal("删掉的密钥 Get 不到")
	}
	f := readFile(t, path)
	if len(f.Keys) != 1 || f.Keys[0].Secret != r.Secret || f.Keys[0].Name != "A2" {
		t.Fatalf("file = %+v", f.Keys)
	}

	// 重新打开读回来
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := s2.Lookup(r.Secret); !ok || got.Name != "A2" || got.Used.Requests != 1 {
		t.Fatalf("读回 = %+v %v", got, ok)
	}
}

// TestCheck 删了、停用、过期都不能用；到期时刻本身就算过期。
func TestCheck(t *testing.T) {
	s, _ := newStore(t)
	on := mustCreate(t, s, Input{Name: "on", Enabled: true, ExpiresAt: t0.Add(time.Hour)})
	off := mustCreate(t, s, Input{Name: "off"})
	if d := s.Check(on.ID, t0); d != nil {
		t.Fatalf("未过期应能用: %v", d)
	}
	for name, tc := range map[string]struct {
		id   string
		now  time.Time
		code string
		msg  string
	}{
		"删了": {"k_none", t0, "key_deleted", "删除"},
		"停用": {off.ID, t0, "key_disabled", "「off」已停用"},
		"到期": {on.ID, t0.Add(time.Hour), "key_expired", "北京时间 2026-10-20 13:00 到期"},
	} {
		t.Run(name, func(t *testing.T) {
			d := s.Check(tc.id, tc.now)
			if d == nil || d.Status != http.StatusUnauthorized || d.Code != tc.code || !strings.Contains(d.Error(), tc.msg) {
				t.Fatalf("d = %+v", d)
			}
			if d2 := s.Admit(tc.id, tc.now); d2 == nil || d2.Code != tc.code {
				t.Fatalf("Admit 也应拒绝: %+v", d2)
			}
		})
	}
	if (Key{}).Expired(t0) {
		t.Fatal("没设到期时间不会过期")
	}
}

// TestAdmitLimits 每天和总量的上限：请求数到了就拒（第 N 个请求放行），token、积分在已用量
// 达到上限后才拒；每天的到北京时间次日 00:00 重置并给出等待时间，总量的不给。
// TestRefund 退还放行时计的次数；过了日切只退累计；删掉的密钥、计数已是 0 时不出错。
func TestRefund(t *testing.T) {
	s, _ := newStore(t)
	k := mustCreate(t, s, Input{Name: "退还", Enabled: true, DailyLimit: Limits{Requests: 1}})
	if d := s.Admit(k.ID, t0); d != nil {
		t.Fatal(d)
	}
	s.Refund(k.ID, t0)
	if d := s.Admit(k.ID, t0); d != nil {
		t.Fatalf("退还后应能再放行一次: %v", d)
	}
	got, _ := s.Get(k.ID, t0)
	if got.Today.Requests != 1 || got.Used.Requests != 1 {
		t.Fatalf("got = %+v", got)
	}

	// 昨天放行、今天才失败：只退累计，今天的计数不动
	tomorrow := t0.Add(12 * time.Hour)
	if d := s.Admit(k.ID, tomorrow); d != nil {
		t.Fatal(d)
	}
	s.Refund(k.ID, t0)
	got, _ = s.Get(k.ID, tomorrow)
	if got.Today.Requests != 1 || got.Used.Requests != 1 {
		t.Fatalf("跨日退还 got = %+v", got)
	}

	// 计数已是 0 不会退成负数
	z := mustCreate(t, s, Input{Name: "零", Enabled: true})
	s.Refund(z.ID, t0)
	if got, _ := s.Get(z.ID, t0); got.Used.Requests != 0 || got.Today.Requests != 0 {
		t.Fatalf("got = %+v", got)
	}
	s.Refund("k_none", t0)
}

func TestAdmitLimits(t *testing.T) {
	s, _ := newStore(t)
	k := mustCreate(t, s, Input{Name: "限额", Enabled: true,
		DailyLimit: Limits{Requests: 2}, TotalLimit: Limits{Tokens: 1000, Credits: 5}})
	for i := 0; i < 2; i++ {
		if d := s.Admit(k.ID, t0); d != nil {
			t.Fatalf("第 %d 个请求应放行: %v", i+1, d)
		}
	}
	d := s.Admit(k.ID, t0)
	if d == nil || d.Status != http.StatusTooManyRequests || d.Code != "key_quota_exceeded" ||
		!strings.Contains(d.Message, "今天的用量已到上限（请求次数 2）") || d.RetryAfter != 12*time.Hour {
		t.Fatalf("d = %+v", d)
	}
	// 过了北京时间日切重置
	tomorrow := t0.Add(12 * time.Hour)
	if d := s.Admit(k.ID, tomorrow); d != nil {
		t.Fatalf("次日应重置: %v", d)
	}
	got, _ := s.Get(k.ID, tomorrow)
	if got.Today.Requests != 1 || got.Used.Requests != 3 || got.Day != "2026-10-21" || !got.LastUsedAt.Equal(tomorrow) {
		t.Fatalf("got = %+v", got)
	}

	// token 总量：用到 999 还放行，到 1000 拒
	s.Record(k.ID, tomorrow, 999, 0, false)
	if d := s.Admit(k.ID, tomorrow); d != nil {
		t.Fatalf("没到上限应放行: %v", d)
	}
	s.Record(k.ID, tomorrow, 1, 0, false)
	// 换一天查，免得先撞上每天 2 次的请求上限
	if d := s.Admit(k.ID, tomorrow.Add(24*time.Hour)); d == nil || !strings.Contains(d.Message, "总用量已到上限（token 1000）") || d.RetryAfter != 0 {
		t.Fatalf("d = %+v", d)
	}

	// 积分：上游没回积分的不计
	c := mustCreate(t, s, Input{Name: "积分", Enabled: true, DailyLimit: Limits{Credits: 2.5}})
	s.Record(c.ID, t0, 0, 9, false)
	if d := s.Admit(c.ID, t0); d != nil {
		t.Fatalf("没回积分不计: %v", d)
	}
	s.Record(c.ID, t0, 10, 2.5, true)
	if d := s.Admit(c.ID, t0); d == nil || !strings.Contains(d.Message, "（积分 2.5）") {
		t.Fatalf("d = %+v", d)
	}
	got, _ = s.Get(c.ID, t0)
	if got.Today.Credits != 2.5 || got.Used.Tokens != 10 || got.Today.Requests != 1 {
		t.Fatalf("got = %+v", got)
	}
	// 记账：删掉的密钥、没有用量的回复都不记
	s.Record("k_none", t0, 5, 1, true)
	s.Record(c.ID, t0, 0, 0, true)
	if again, _ := s.Get(c.ID, t0); again.Used != got.Used {
		t.Fatalf("不该记账: %+v", again.Used)
	}
}

// TestListRollsToday 过了日切还没用过的密钥，列表里今天的用量是 0，累计不变。
func TestListRollsToday(t *testing.T) {
	s, _ := newStore(t)
	k := mustCreate(t, s, Input{Name: "a", Enabled: true})
	_ = s.Admit(k.ID, t0)
	s.Record(k.ID, t0, 100, 1, true)
	l := s.List(t0.Add(24 * time.Hour))
	if len(l) != 1 || l[0].Today != (Usage{}) || l[0].Day != "2026-10-21" || l[0].Used != (Usage{Requests: 1, Tokens: 100, Credits: 1}) {
		t.Fatalf("l = %+v", l)
	}
	if l := s.List(t0); l[0].Today != l[0].Used {
		t.Fatalf("当天 Today 应等于累计: %+v", l[0])
	}
}

// TestAllowsModel 白名单为空不限；按 * 通配、不区分大小写匹配。
func TestAllowsModel(t *testing.T) {
	if !(Key{}).AllowsModel("anything") {
		t.Fatal("空白名单应不限")
	}
	k := Key{Models: []string{"glm-*", "deepseek-v4.1-flash"}}
	for model, want := range map[string]bool{"glm-5.2": true, "GLM-5.3-flash": true, "deepseek-v4.1-flash": true, "deepseek-v4-pro": false, "kimi": false} {
		if got := k.AllowsModel(model); got != want {
			t.Errorf("AllowsModel(%s) = %v", model, got)
		}
	}
}

// TestOpen 文件不存在从空开始；读不了、格式不对报错；残缺条目跳过；路径为空只存内存。
func TestOpen(t *testing.T) {
	dir := t.TempDir()
	if s, err := Open(filepath.Join(dir, "none.json")); err != nil || s.Any() {
		t.Fatalf("不存在: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte("{x"), 0o600)
	if _, err := Open(bad); err == nil || !strings.Contains(err.Error(), "解析") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "读取") {
		t.Fatalf("目录当文件读应报错: %v", err)
	}
	partial := filepath.Join(dir, "partial.json")
	_ = os.WriteFile(partial, []byte(`{"version":1,"keys":[null,{"id":"","secret":"x"},{"id":"k1","secret":""},{"id":"k2","name":"ok","secret":"sk-ok","enabled":true}]}`), 0o600)
	s, err := Open(partial)
	if err != nil {
		t.Fatal(err)
	}
	if l := s.List(t0); len(l) != 1 || l[0].ID != "k2" {
		t.Fatalf("l = %+v", l)
	}
	mem, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mem.Create(Input{Name: "m"}, t0); err != nil {
		t.Fatal(err)
	}
	mem.Flush()
	var nilStore *Store
	if nilStore.Any() {
		t.Fatal("nil 库 Any 应为 false")
	}
	if _, ok := nilStore.Lookup("sk-x"); ok {
		t.Fatal("nil 库查不到")
	}
}

// TestSaveFailureRollsBack 写盘失败时撤回这次增删改，内存和文件保持一致。
func TestSaveFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api_keys.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	k := mustCreate(t, s, Input{Name: "a", Enabled: true})
	// 把临时文件的位置占成目录，之后每次写盘都失败
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(Input{Name: "b"}, t0); err == nil {
		t.Fatal("写盘失败应报错")
	}
	if l := s.List(t0); len(l) != 1 {
		t.Fatalf("新建应撤回: %+v", l)
	}
	if _, err := s.Update(k.ID, Input{Name: "改名"}); err == nil {
		t.Fatal("写盘失败应报错")
	}
	if got, _ := s.Get(k.ID, t0); got.Name != "a" || !got.Enabled {
		t.Fatalf("修改应撤回: %+v", got)
	}
	if _, err := s.Regenerate(k.ID); err == nil {
		t.Fatal("写盘失败应报错")
	}
	if got, ok := s.Lookup(k.Secret); !ok || got.ID != k.ID {
		t.Fatal("重新生成应撤回，旧原文照样能用")
	}
	if err := s.Delete(k.ID); err == nil {
		t.Fatal("写盘失败应报错")
	}
	if _, ok := s.Lookup(k.Secret); !ok {
		t.Fatal("删除应撤回")
	}
	// 计数写不成留着 dirty，下次再写
	_ = s.Admit(k.ID, t0)
	s.Flush()
	if !s.dirty {
		t.Fatal("写盘失败应保留 dirty")
	}
	_ = os.Remove(path + ".tmp")
	s.Flush()
	if s.dirty || readFile(t, path).Keys[0].Used.Requests != 1 {
		t.Fatal("恢复后应写入计数")
	}

	// 建目录失败
	blocker := filepath.Join(dir, "blocker")
	_ = os.WriteFile(blocker, nil, 0o600)
	s2, _ := Open("")
	s2.path = filepath.Join(blocker, "sub", "api_keys.json")
	if _, err := s2.Create(Input{Name: "x"}, t0); err == nil {
		t.Fatal("建目录失败应报错")
	}
}

// TestRandFailure 随机源失败时新建、重新生成报错；id 连续撞号也报错。
func TestRandFailure(t *testing.T) {
	s, _ := newStore(t)
	k := mustCreate(t, s, Input{Name: "a"})
	old := randRead
	t.Cleanup(func() { randRead = old })
	randRead = func([]byte) (int, error) { return 0, errors.New("boom") }
	if _, err := s.Create(Input{Name: "b"}, t0); err == nil || !strings.Contains(err.Error(), "生成密钥 id") {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Regenerate(k.ID); err == nil || !strings.Contains(err.Error(), "生成密钥") {
		t.Fatalf("err = %v", err)
	}
	// id 生成成功、原文生成失败
	calls := 0
	randRead = func(b []byte) (int, error) {
		calls++
		if calls == 1 {
			return old(b)
		}
		return 0, errors.New("boom")
	}
	if _, err := s.Create(Input{Name: "c"}, t0); err == nil || !strings.Contains(err.Error(), "生成密钥:") {
		t.Fatalf("err = %v", err)
	}
	// 每次都生成同一个 id：撞上已有的
	randRead = func(b []byte) (int, error) {
		for i := range b {
			b[i] = 0
		}
		return len(b), nil
	}
	if _, err := s.Create(Input{Name: "d"}, t0); err != nil {
		t.Fatalf("第一次不撞: %v", err)
	}
	if _, err := s.Create(Input{Name: "e"}, t0); err == nil || !strings.Contains(err.Error(), "撞号") {
		t.Fatalf("err = %v", err)
	}
}

// TestStartStop 后台定时把用量计数写盘，停机再写一次。
func TestStartStop(t *testing.T) {
	old := flushInterval
	flushInterval = 5 * time.Millisecond
	t.Cleanup(func() { flushInterval = old })
	s, path := newStore(t)
	k := mustCreate(t, s, Input{Name: "a", Enabled: true})
	s.Start()
	_ = s.Admit(k.ID, t0)
	deadline := time.Now().Add(2 * time.Second)
	for readFile(t, path).Keys[0].Used.Requests != 1 {
		if time.Now().After(deadline) {
			t.Fatal("后台没写盘")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Record(k.ID, t0, 7, 0, false)
	s.Stop()
	s.Stop() // 重复调用不卡
	if readFile(t, path).Keys[0].Used.Tokens != 7 {
		t.Fatal("停机应写盘")
	}
}

// TestKeyGroups 限定的分组去空白、去重，返回副本；UsingGroup 按创建顺序列出限定了某个分组的密钥名。
func TestKeyGroups(t *testing.T) {
	s, _ := newStore(t)
	a := mustCreate(t, s, Input{Name: "甲", Enabled: true, Groups: []string{" g1 ", "", "g1", "g2"}})
	if strings.Join(a.Groups, ",") != "g1,g2" {
		t.Fatalf("groups = %v", a.Groups)
	}
	mustCreate(t, s, Input{Name: "乙", Enabled: true, Groups: []string{"g2"}})
	mustCreate(t, s, Input{Name: "丙", Enabled: true})
	if got := strings.Join(s.UsingGroup("g2"), ","); got != "甲,乙" {
		t.Fatalf("UsingGroup(g2) = %s", got)
	}
	if got := s.UsingGroup("g3"); len(got) != 0 {
		t.Fatalf("UsingGroup(g3) = %v", got)
	}
	got, _ := s.Lookup(a.Secret)
	got.Groups[0] = "改坏"
	if again, _ := s.Lookup(a.Secret); again.Groups[0] != "g1" {
		t.Fatalf("库里的分组被调用方改了: %v", again.Groups)
	}
}
