package pool

import (
	"sort"
	"strings"
	"time"
)

// ModelLockRow 单个 (域, 模型) 的锁池画像：这个模型在这个域还能不能选、被锁了几个号、
// 最早/全部解锁是什么时候。
//
// 与 ModelBlocked 的分工：ModelBlocked 回答「**这一个**模型此刻是不是全池被挡」
// （供请求失败时回 model_unavailable），本结构是它的**全清单**版本——把当前所有
// 有未过期模型级冷却的 (域, 模型) 一次性列出来，供 /status 与面板直接展示，
// 运维不必先失败一次才知道哪个模型不能用、还要等多久。
type ModelLockRow struct {
	// Model 裸模型名（不含 cn:/global: 前缀；域由 Realm 字段表达）。
	Model string `json:"model"`
	// Realm 账号域（cn | global）。同名模型在两个域各自独立计数。
	Realm string `json:"realm"`
	// Total 该域**参与选号**的账号数（禁用/暂停号不计，与 ModelBlocked 同口径），
	// 作为「可选 / 总数」的分母。
	Total int `json:"total"`
	// Servable 此刻真正能服务该模型的账号数（healthyForModel 且未在途占满）。
	Servable int `json:"servable"`
	// Locked 因该模型自身冷却被挡的账号数。
	Locked int `json:"locked"`
	// State 聚合状态：
	//   - locked  ：全部参与选号的账号都被该模型冷却挡住（换模型或等解锁才有用，
	//               与 ModelBlocked 的 Blocked 同口径）；
	//   - starved ：此刻没有号能服务，但不是模型冷却造成的（账号级冷却/熔断/在途占满）
	//               ——等一下就会好，与模型无关；
	//   - partial ：仍有号能服务该模型，只是部分号被锁。
	State string `json:"state"`
	// UnlockAt 最早解锁时刻：第一个被锁账号恢复的时刻（partial 下「何时值得重试」）。
	UnlockAt time.Time `json:"unlock_at"`
	// FullyUnlockAt 全部解锁时刻：最后一个被锁账号恢复的时刻。
	FullyUnlockAt time.Time `json:"fully_unlock_at"`
	// Reason 该模型被锁的原因（取最早解锁账号的上游原文）。
	Reason string `json:"reason,omitempty"`
}

// ModelLockView 汇总当前所有「有未过期模型级限流」的 (域, 模型)，按不可用优先排序。
//
// 为什么需要它：选号失败时上游只回 no_healthy_account（issue #102），ModelBlocked 虽
// 已把「号都在、只是都对这个模型关闭」从「池子真没号」里区分出来，但那是一次请求
// 失败后的单模型回答；运维仍看不到「此刻一共哪些模型不能用、分别还要锁多久」。
// 本视图把 (域, 模型) 维度聚合成一张清单补齐这一点。
//
// 口径与选号/ModelBlocked 严格一致，三处易错点：
//   - 只看**参与选号**的账号：disabled / paused 号跳过——它们的不可用与模型无关，
//     计进来会把「模型被锁」和「号被停了」混为一谈；
//   - 只看**真正拦路由**的冷却：AuditOnly 台账（无重置时间的 6004）不改变选号，
//     既不产生行也不计入 Locked；
//   - 已过期的冷却不算锁。
//
// 只读：持 RLock 遍历，不改任何状态。无锁时返回 nil（JSON 里是 null，面板按空态渲染）。
func (p *Pool) ModelLockView() []ModelLockRow {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()

	type agg struct {
		locked   int
		unlockAt time.Time
		fullyAt  time.Time
		reason   string
	}
	rows := make(map[string]*agg)
	routable := make(map[string]int) // realm → 参与选号的账号数

	for _, e := range p.byUID {
		realm := e.a.Realm()
		if e.disabled || e.paused {
			continue
		}
		routable[realm]++
		for model, mc := range e.modelCooldowns {
			if mc.AuditOnly || mc.Until.IsZero() || !now.Before(mc.Until) {
				continue
			}
			key := realm + "\x1f" + model
			a := rows[key]
			if a == nil {
				a = &agg{}
				rows[key] = a
			}
			a.locked++
			if a.unlockAt.IsZero() || mc.Until.Before(a.unlockAt) {
				a.unlockAt = mc.Until
				a.reason = mc.Reason
			}
			if a.fullyAt.IsZero() || mc.Until.After(a.fullyAt) {
				a.fullyAt = mc.Until
			}
		}
	}
	if len(rows) == 0 {
		return nil
	}

	out := make([]ModelLockRow, 0, len(rows))
	for key, a := range rows {
		sep := strings.IndexByte(key, '\x1f')
		realm, model := key[:sep], key[sep+1:]
		row := ModelLockRow{
			Model:         model,
			Realm:         realm,
			Total:         routable[realm],
			Locked:        a.locked,
			UnlockAt:      a.unlockAt,
			FullyUnlockAt: a.fullyAt,
			Reason:        a.reason,
		}
		for _, e := range p.byUID {
			if e.a.Realm() != realm {
				continue
			}
			if e.healthyForModel(now, model) && !p.inFlightFull(e) {
				row.Servable++
			}
		}
		switch {
		case row.Servable > 0:
			row.State = "partial"
		case row.Locked >= row.Total:
			// 参与选号的号全被这个模型挡住 —— 与 ModelBlocked 的 Blocked 同义。
			row.State = "locked"
		default:
			row.State = "starved"
		}
		out = append(out, row)
	}

	rank := func(state string) int {
		switch state {
		case "locked":
			return 0
		case "starved":
			return 1
		default:
			return 2
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i].State), rank(out[j].State); ri != rj {
			return ri < rj
		}
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		return out[i].Realm < out[j].Realm
	})
	return out
}
