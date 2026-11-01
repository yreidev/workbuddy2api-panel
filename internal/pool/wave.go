// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 风控波熔断：短时间内同一个域有多个号被上游 11140「账号存在异常行为」限制时，
// 说明上游正在成批风控这个域（2026-10-23 国际版实测，workbuddy2api-hub #629），
// 此时继续轮换只会把剩下的号一个个送进风控。达到阈值后整域暂停一段时间，
// 被暂停的号记为 CoolPause：不参与全冷却兜底，也不随余额刷新解冻，到期自动恢复，
// 面板「解冻」可提前恢复单个号。参数默认照 hub 的做法：15 分钟内 2 个号 → 暂停 30 分钟。
//
// 装了出口函数（SetExitKey，代理池按号的代理算出口）时分两层：窗口内被限的号都在同一个出口，
// 只暂停这个域里这个出口下的号；分布在几个出口，才照旧暂停整个域。
package pool

import (
	"fmt"
	"log"
	"sort"
	"time"
)

const (
	defaultRestrictionWaveThreshold = 2
	defaultRestrictionWaveWindow    = 15 * time.Minute
	defaultRestrictionWavePause     = 30 * time.Minute
)

// restrictionHit 一次 11140 限制记录（窗口内按账号去重）。exit 是号当时的出口（没装出口函数为空）。
type restrictionHit struct {
	uid  string
	at   time.Time
	exit string
}

// SetExitKey 装上「号从哪个出口出去」的函数（代理池的 ExitKey）：key 相同的号算同一个出口，label 写进
// 日志和冷却原因。在 NoteRestriction 之前调用；不装照旧整域暂停。
func (p *Pool) SetExitKey(f func(uid string) (key, label string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.exitKey = f
}

// ExitOf 号 uid 的出口（SetExitKey 装上的函数算的）；没装出口函数时 key 为空，所有号算同一个出口。
func (p *Pool) ExitOf(uid string) (key, label string) {
	p.mu.RLock()
	f := p.exitKey
	p.mu.RUnlock()
	if f == nil {
		return "", ""
	}
	return f(uid)
}

// SameExit 和号 uid 同一个出口的号（含它自己），按 UID 排序；没装出口函数时是池里所有号。
func (p *Pool) SameExit(uid string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	exit := ""
	if p.exitKey != nil {
		exit, _ = p.exitKey(uid)
	}
	var out []string
	for u := range p.byUID {
		if p.exitKey != nil {
			if k, _ := p.exitKey(u); k != exit {
				continue
			}
		}
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// SetRestrictionWave 注入风控波熔断参数（main 从 config 解析后调用）。
// threshold 0 = 关闭（与 config 的 0 同义，并清空已有记录）；负值非法保留原值。
// window、pause 非正值保留原值（用默认）。
func (p *Pool) SetRestrictionWave(threshold int, window, pause time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if threshold >= 0 {
		p.waveThreshold = threshold
	}
	if window > 0 {
		p.waveWindow = window
	}
	if pause > 0 {
		p.wavePause = pause
	}
	if p.waveThreshold == 0 {
		p.waveHits = nil
	}
}

// NoteRestriction 记录账号被上游 11140 限制（调用方先 Disable 该号再调这里）。
// 窗口内同域不同账号的记录数达到阈值时，把该域其余未禁用的号暂停到 now+pause
// （已有更晚截止的冷却不缩短），清空该域记录，返回本次暂停的账号数；未触发返回 0。
func (p *Pool) NoteRestriction(uid string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waveThreshold <= 0 {
		return 0
	}
	e, ok := p.byUID[uid]
	if !ok {
		return 0
	}
	realm := e.a.Realm()
	now := time.Now()
	exit, label := "", ""
	if p.exitKey != nil {
		exit, label = p.exitKey(uid)
	}
	hits := make([]restrictionHit, 0, len(p.waveHits[realm])+1)
	for _, h := range p.waveHits[realm] {
		if h.uid != uid && now.Sub(h.at) < p.waveWindow {
			hits = append(hits, h)
		}
	}
	hits = append(hits, restrictionHit{uid: uid, at: now, exit: exit})
	if len(hits) < p.waveThreshold {
		if p.waveHits == nil {
			p.waveHits = map[string][]restrictionHit{}
		}
		p.waveHits[realm] = hits
		return 0
	}
	delete(p.waveHits, realm)

	until := now.Add(p.wavePause)
	sameExit := exit != ""
	for _, h := range hits {
		sameExit = sameExit && h.exit == exit
	}
	reason := fmt.Sprintf("restriction wave: %d %s accounts restricted by upstream (11140) within %s, realm paused until %s",
		len(hits), realm, p.waveWindow, until.Format("15:04:05"))
	if sameExit {
		reason = fmt.Sprintf("风控波：%g 分钟内 %d 个号在%s上被上游 11140 限制，这个出口的号暂停到 %s",
			p.waveWindow.Minutes(), len(hits), label, until.Format("15:04:05"))
	}
	paused := 0
	for _, x := range p.byUID {
		if x.disabled || x.a.Realm() != realm {
			continue
		}
		if sameExit {
			if k, _ := p.exitKey(x.a.UID); k != exit {
				continue // 只暂停同一个出口的号
			}
		}
		if now.Before(x.until) && !x.until.Before(until) {
			continue // 已有更晚截止的冷却（例如积分冷却到次日 04:00），不缩短
		}
		x.until = until
		x.coolKind = CoolPause
		x.reason = reason
		paused++
	}
	if paused > 0 {
		p.dirty.Store(true)
	}
	log.Printf("WARN: [pool] %s; %d accounts paused", reason, paused)
	return paused
}

// wavePausedLocked 报告账号是否正处风控波暂停期。软冷却等更短的冷却不得覆盖它：
// 暂停那一刻还在途的请求随后回 429，会把截止改短，让这个号提前回到选号。
// 调用方必须已持有 p.mu。
func (e *entry) wavePausedLocked(now time.Time) bool {
	return e.coolKind == CoolPause && now.Before(e.until)
}
