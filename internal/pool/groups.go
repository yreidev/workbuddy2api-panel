// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// 账号分组与优先级（issue #4）。
//
// 分组：面板里建、改名、删；一个号可以进多个组。客户端密钥可以限定只用哪些组的号（apikeys.Key.Groups），
// 分组本身不影响选号。优先级：每个号一个数，越小越先用，默认 0。选号时只在优先级最高、此刻能接这个请求的
// 那一层号里挑，这一层都接不了（冷却、禁用、暂停、模型冷却、积分保底、在途或每分钟请求数满了）才轮到下一层；
// 先分优先级层，再在层内按成本分层。参考 sub2api 的账号分组与 priority（数字小的先用）。
package pool

import (
	"crypto/rand"
	"errors"
	"slices"
	"strings"
	"time"
)

// MaxPriority 优先级的上限，下限是 0（默认）。
const MaxPriority = 99

// Group 一个账号分组。
type Group struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// GroupView 面板列表里的分组：带组里有几个号。
type GroupView struct {
	Group
	Accounts int `json:"accounts"`
}

var (
	// ErrGroupNotFound 没有这个 id 的分组。
	ErrGroupNotFound = errors.New("分组不存在")
	// ErrGroupNameTaken 已经有同名（不区分大小写）的分组。
	ErrGroupNameTaken = errors.New("已经有同名的分组")
)

// Groups 全部分组，按建的先后。
func (p *Pool) Groups() []GroupView {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]GroupView, 0, len(p.groups))
	for _, g := range p.groups {
		n := 0
		for _, e := range p.byUID {
			if slices.Contains(e.groups, g.ID) {
				n++
			}
		}
		out = append(out, GroupView{Group: g, Accounts: n})
	}
	return out
}

// HasGroup 有没有这个 id 的分组。
func (p *Pool) HasGroup(id string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.groupIndexLocked(id) >= 0
}

// CreateGroup 建一个分组。name、note 由调用方整理、校验过。
func (p *Pool) CreateGroup(name, note string) (Group, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.nameTakenLocked(name, "") {
		return Group{}, ErrGroupNameTaken
	}
	g := Group{ID: "g" + strings.ToLower(rand.Text()[:10]), Name: name, Note: note, CreatedAt: time.Now()}
	p.groups = append(p.groups, g)
	p.dirty.Store(true)
	return g, nil
}

// UpdateGroup 改分组的名字和备注；号和密钥按 id 认分组，改名不影响它们。
func (p *Pool) UpdateGroup(id, name, note string) (Group, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.groupIndexLocked(id)
	if i < 0 {
		return Group{}, ErrGroupNotFound
	}
	if p.nameTakenLocked(name, id) {
		return Group{}, ErrGroupNameTaken
	}
	p.groups[i].Name, p.groups[i].Note = name, note
	p.dirty.Store(true)
	return p.groups[i], nil
}

// DeleteGroup 删分组，组里的号退出这个组。还有密钥在用这个组时由调用方先拦下（删了密钥就变成不限）。
func (p *Pool) DeleteGroup(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.groupIndexLocked(id)
	if i < 0 {
		return ErrGroupNotFound
	}
	p.groups = slices.Delete(p.groups, i, i+1)
	for _, e := range p.byUID {
		e.groups = slices.DeleteFunc(e.groups, func(g string) bool { return g == id })
	}
	p.dirty.Store(true)
	return nil
}

// SetAccountGroups 用 ids 替换号所在的分组（ids 由调用方去重过）。号不存在返回 false；有不存在的分组返回 ErrGroupNotFound。
func (p *Pool) SetAccountGroups(uid string, ids []string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return false, nil
	}
	for _, id := range ids {
		if p.groupIndexLocked(id) < 0 {
			return true, ErrGroupNotFound
		}
	}
	e.groups = slices.Clone(ids)
	p.dirty.Store(true)
	return true, nil
}

// JoinGroup 号加进分组（已经在里面不动），leave 为 true 时是退出。号不存在返回 false；分组不存在返回 ErrGroupNotFound。
func (p *Pool) JoinGroup(uid, id string, leave bool) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.groupIndexLocked(id) < 0 {
		return false, ErrGroupNotFound
	}
	e, ok := p.byUID[uid]
	if !ok {
		return false, nil
	}
	switch in := slices.Contains(e.groups, id); {
	case leave && in:
		e.groups = slices.DeleteFunc(e.groups, func(g string) bool { return g == id })
	case !leave && !in:
		e.groups = append(e.groups, id)
	default:
		return true, nil
	}
	p.dirty.Store(true)
	return true, nil
}

// SetPriority 改号的优先级（0～MaxPriority，由调用方校验）。号不存在返回 false。
func (p *Pool) SetPriority(uid string, n int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.byUID[uid]
	if !ok {
		return false
	}
	if e.priority != n {
		e.priority = n
		p.dirty.Store(true)
	}
	return true
}

// UIDsOutsideGroups 不在这些分组里的号，客户端密钥绑了分组时选号跳过它们（当作这次已经试过）。
func (p *Pool) UIDsOutsideGroups(ids []string) map[string]bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := map[string]bool{}
	for uid, e := range p.byUID {
		if !slices.ContainsFunc(e.groups, func(g string) bool { return slices.Contains(ids, g) }) {
			out[uid] = true
		}
	}
	return out
}

// topPriorityLocked 只留优先级最高（数字最小）的那一层号。哪一层最高只看此刻真能接这个请求的号：积分保底拦着的
// 不算，免得一层号全触底时还把会话分到这一层；都被拦着时原样返回。调用方持 p.mu。
func (p *Pool) topPriorityLocked(es []*entry, model, realm string, now time.Time) []*entry {
	best, found := 0, false
	for _, e := range es {
		if (!found || e.priority < best) && !p.floorBlockedForRealmModel(e, model, realm, now) {
			best, found = e.priority, true
		}
	}
	if !found {
		return es
	}
	out := es[:0]
	for _, e := range es {
		if e.priority == best {
			out = append(out, e)
		}
	}
	return out
}

func (p *Pool) groupIndexLocked(id string) int {
	return slices.IndexFunc(p.groups, func(g Group) bool { return g.ID == id })
}

func (p *Pool) nameTakenLocked(name, exceptID string) bool {
	return slices.ContainsFunc(p.groups, func(g Group) bool { return g.ID != exceptID && strings.EqualFold(g.Name, name) })
}
