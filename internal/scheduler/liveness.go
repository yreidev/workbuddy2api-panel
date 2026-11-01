// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 测活排程：定时测活（每 schedule.liveness_minutes 分钟测一遍整个号在冷却、熔断中的号，测通就提前恢复，
// 类似 new-api 的被动恢复模式）、面板的「全部测活」、多选后的批量测活和单号测活。测活本身在 server.Handler.TestAccount，
// 由 main 接上（SetLiveness）。

package scheduler

import (
	"context"
	"errors"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/pool"
)

// LivenessFunc 测活一个号（server.Handler.TestAccount）：model 为空时挑这个号能接的最便宜的模型，
// 返回结果和上游回复的文字。号不存在、挑不出模型返回 error，上游的失败写在结果里。
type LivenessFunc func(ctx context.Context, uid, model string, scheduled bool) (pool.LivenessResult, string, error)

// livenessAllConcurrency 全部测活同时测几个号。定时测活一个一个来，要测的号本来就少。
const livenessAllConcurrency = 2

var (
	// ErrLivenessBusy 已有一批测活在跑（全部测活或定时测活）。
	ErrLivenessBusy = errors.New("已有一批测活在跑，跑完再试")
	// ErrLivenessUnavailable 测活没接上（main 没调 SetLiveness，只在测试和裸用时出现）。
	ErrLivenessUnavailable = errors.New("liveness not available")
)

// SetLiveness 接上测活（server 建得比 scheduler 晚，事后接线）。
func (s *Scheduler) SetLiveness(fn LivenessFunc) {
	s.livenessMu.Lock()
	s.livenessFn = fn
	s.livenessMu.Unlock()
}

func (s *Scheduler) liveness() LivenessFunc {
	s.livenessMu.Lock()
	defer s.livenessMu.Unlock()
	return s.livenessFn
}

// TestAccount 面板单测一个号；不受「同时只跑一批」限制。
func (s *Scheduler) TestAccount(ctx context.Context, uid, model string) (pool.LivenessResult, string, error) {
	fn := s.liveness()
	if fn == nil {
		return pool.LivenessResult{}, "", ErrLivenessUnavailable
	}
	return fn(ctx, uid, model, false)
}

// StartLivenessAll 面板「全部测活」：后台测所有没被人工禁用、没被代理池拦着的号（暂停选号、网关自动禁用的也测），
// 同时测 livenessAllConcurrency 个，结果记在各号的「上次测活」里。返回要测的号数。
func (s *Scheduler) StartLivenessAll() (int, error) {
	return s.startLivenessBatch("全部测活", func(st pool.Status) bool {
		return !st.Disabled || st.DisabledReason != pool.ManualDisableReason // 人工禁用的不测
	})
}

// StartLivenessFor 面板多选后的「测活」：后台测选中的号，人工禁用的也测（是使用者自己挑的），代理池拦着的
// 跳过，不在池里的 uid 不管。并发和结果记法同全部测活，和它、定时测活共用「同时只跑一批」。返回要测的号数。
func (s *Scheduler) StartLivenessFor(uids []string) (int, error) {
	want := make(map[string]bool, len(uids))
	for _, uid := range uids {
		want[uid] = true
	}
	return s.startLivenessBatch("批量测活", func(st pool.Status) bool { return want[st.UID] })
}

// startLivenessBatch 在后台测 pick 挑中、没被代理池拦着的号（代理池拦着的请求出不去，测不了）。
func (s *Scheduler) startLivenessBatch(kind string, pick func(pool.Status) bool) (int, error) {
	fn := s.liveness()
	if fn == nil {
		return 0, ErrLivenessUnavailable
	}
	if !s.livenessRunning.CompareAndSwap(false, true) {
		return 0, ErrLivenessBusy
	}
	var uids []string
	for _, st := range s.cfg.Pool.List() {
		if st.ProxyBlocked == "" && pick(st) {
			uids = append(uids, st.UID)
		}
	}
	go func() {
		defer s.livenessRunning.Store(false)
		runLiveness(fn, uids, livenessAllConcurrency, kind, false)
	}()
	return len(uids), nil
}

// RunLivenessNow 定时测活一轮：测 pool.LivenessDue 挑出的号，一个一个来。上一批还没跑完就跳过这一轮。
func (s *Scheduler) RunLivenessNow() {
	fn := s.liveness()
	if fn == nil || !s.livenessRunning.CompareAndSwap(false, true) {
		return
	}
	defer s.livenessRunning.Store(false)
	if uids := s.cfg.Pool.LivenessDue(time.Now()); len(uids) > 0 {
		runLiveness(fn, uids, 1, "定时测活", true)
	}
}

// runLiveness 按并发上限测完 uids，前后各记一行任务日志（kind 写是哪种测活）。
func runLiveness(fn LivenessFunc, uids []string, conc int, kind string, scheduled bool) {
	log.Printf("liveness %s开始：%d 个号", kind, len(uids))
	var ok, bad atomic.Int32
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, uid := range uids {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res, _, err := fn(context.Background(), uid, "", scheduled)
			switch {
			case err != nil:
				log.Printf("liveness WARN %v", err)
				bad.Add(1)
			case res.OK:
				ok.Add(1)
			default:
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	log.Printf("liveness %s结束：%d 个通，%d 个不通", kind, ok.Load(), bad.Load())
}

// StartLiveness 启动定时测活循环：每 interval 跑一轮 RunLivenessNow。interval<=0 先不跑，
// 等 SetLivenessInterval 打开（面板改配置）。ctx 取消即退出。
func (s *Scheduler) StartLiveness(ctx context.Context, interval time.Duration) {
	s.livenessInterval.Store(int64(max(interval, 0)))
	go func() {
		var logged time.Duration = -1
		for {
			cur := time.Duration(s.livenessInterval.Load())
			if cur != logged {
				if cur > 0 {
					log.Printf("scheduler: 定时测活每 %s（只测冷却、熔断中的号）", cur)
				} else if logged > 0 {
					log.Printf("scheduler: 定时测活已关闭")
				}
				logged = cur
			}
			var tick <-chan time.Time
			var timer *time.Timer
			if cur > 0 {
				timer = time.NewTimer(cur)
				tick = timer.C
			}
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case <-s.rearmLiveness:
				if timer != nil {
					timer.Stop() // 间隔已变：按新值重算
				}
			case <-tick:
				s.RunLivenessNow()
			}
		}
	}()
}

// SetLivenessInterval 热改定时测活间隔；<=0 关闭（面板关掉开关时）。
func (s *Scheduler) SetLivenessInterval(d time.Duration) {
	s.livenessInterval.Store(int64(max(d, 0)))
	poke(s.rearmLiveness)
}
