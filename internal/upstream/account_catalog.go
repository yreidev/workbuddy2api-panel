// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// account_catalog.go 企业号各自的模型目录，以及按目录判断某个号能不能接某个模型。
//
// 域目录（storeRealmModels）每个域只拉一份，用的是个人号。企业号的名单因企业而异：
// 可能多出企业开通的模型（Claude、国际版模型等），也可能少几个个人号才有的模型。
// 上游 /v3/config 按请求里的账号身份下发名单，企业号不带企业身份时拿到的是个人名单
// （dsh-workbuddy-connect issue #123 用真实企业号实测：只带 X-User-Id 仍是个人名单，
// 只带 X-Enterprise-Id 被拒，两个都带才是企业名单，企业视图比个人视图多 3 个、少 2 个模型）。
//
// 所以每个国内版企业号单独拉一份目录存在这里，选号时（ServesModel，由 pool 注入调用）：
//   - 企业号的目录已拉到：不接别处目录里有、自己目录里没有的模型；
//   - 企业专属模型（域目录里没有、某个企业号目录里有）：不派给个人号，也不派给目录还没拉到的号；
//   - 其他情况放行，与引入按目录选号之前一致。
//
// 每个号各拉一份目录、按目录选号的做法参考 cli2api（caigee-cmd/cli2api 的 executor.itemHasModel）。
// 国际版企业号没有实测数据，不单独拉目录。
package upstream

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// accountCatalog 一个企业号最近一次成功拉到的目录。
type accountCatalog struct {
	realm   string
	infos   []ModelInfo
	set     map[string]bool
	fetched time.Time
}

// FetchAccountCatalog 拉一个国内版企业号自己的目录，只存进它自己的目录，不动域目录、倍率和档位缓存。
// 失败时保留上次成功的目录。
func (c *Client) FetchAccountCatalog(a *auth.Auth) ([]ModelInfo, error) {
	if a == nil || !a.IsEnterprise() || a.IsGlobal() {
		return nil, fmt.Errorf("只给国内版企业号单独拉目录")
	}
	out, err := c.probeCatalog(a)
	if err != nil {
		return nil, err
	}
	c.storeAccountCatalog(a, out)
	return out, nil
}

// storeAccountCatalog 记下企业号自己的目录；个人号、国际版账号不记。
func (c *Client) storeAccountCatalog(a *auth.Auth, infos []ModelInfo) {
	if a == nil || !a.IsEnterprise() || a.IsGlobal() || len(infos) == 0 {
		return
	}
	set := make(map[string]bool, len(infos))
	for _, mi := range infos {
		if mi.ID != "" {
			set[mi.ID] = true
		}
	}
	c.accountMu.Lock()
	defer c.accountMu.Unlock()
	if c.accountCatalogs == nil {
		c.accountCatalogs = make(map[string]accountCatalog)
	}
	c.accountCatalogs[a.UID] = accountCatalog{realm: a.Realm(), infos: infos, set: set, fetched: time.Now()}
}

// AccountCatalogFetched 返回该号目录最近一次拉到的时间；没有目录时 ok=false。
func (c *Client) AccountCatalogFetched(uid string) (time.Time, bool) {
	c.accountMu.RLock()
	defer c.accountMu.RUnlock()
	cat, ok := c.accountCatalogs[uid]
	return cat.fetched, ok
}

// PruneAccountCatalogs 删掉不在 keep 里的号的目录（账号移除、禁用或不再是企业号）。
func (c *Client) PruneAccountCatalogs(keep map[string]bool) {
	c.accountMu.Lock()
	defer c.accountMu.Unlock()
	for uid := range c.accountCatalogs {
		if !keep[uid] {
			delete(c.accountCatalogs, uid)
		}
	}
}

// ServesModel 按目录判断这个号能不能接这个模型（规则见文件头）。由 pool 在选号时调用。
func (c *Client) ServesModel(uid, realm string, enterprise bool, model string) bool {
	if c == nil || model == "" {
		return true
	}
	c.accountMu.RLock()
	cat, ok := c.accountCatalogs[uid]
	c.accountMu.RUnlock()
	if ok {
		// 自己目录里没有：只有域目录或别的企业号目录里确有这个模型时才不接；
		// 哪份目录都没有的模型（拼错、上游没列出的旧名）照常放行，让上游给出原本的报错。
		return cat.set[model] || !c.knownInRealm(realm, model)
	}
	if enterprise {
		return true // 企业号的目录还没拉到：先放行，拉到后按目录来
	}
	return !c.enterpriseOnly(realm, model)
}

// enterpriseOnly 该域目录拉到过且没有这个模型，而该域某个企业号的目录里有。
// 域目录从未拉到时不判定为企业专属：不知道个人号有没有，就不拦个人号。
func (c *Client) enterpriseOnly(realm, model string) bool {
	has, known := c.realmCatalogHas(realm, model)
	if !known || has {
		return false
	}
	return c.accountCatalogsHave(realm, model)
}

// knownInRealm 该域的域目录或某个企业号的目录里有没有这个模型。
func (c *Client) knownInRealm(realm, model string) bool {
	if has, _ := c.realmCatalogHas(realm, model); has {
		return true
	}
	return c.accountCatalogsHave(realm, model)
}

// accountCatalogsHave 该域有没有哪个企业号的目录里有这个模型。
func (c *Client) accountCatalogsHave(realm, model string) bool {
	c.accountMu.RLock()
	defer c.accountMu.RUnlock()
	for _, cat := range c.accountCatalogs {
		if realmKey(cat.realm) == realmKey(realm) && cat.set[model] {
			return true
		}
	}
	return false
}

// EnterpriseOnlyModels 该域的企业专属模型（域目录里没有、企业号目录里有），按 id 排序去重，
// 供 /v1/models 与面板模型页列出。域目录从未拉到时返回 nil。
func (c *Client) EnterpriseOnlyModels(realm string) []ModelInfo {
	c.effortsMu.RLock()
	realmSet, known := c.realmModels[realmKey(realm)]
	c.effortsMu.RUnlock()
	if !known {
		return nil
	}
	c.accountMu.RLock()
	defer c.accountMu.RUnlock()
	seen := map[string]bool{}
	var out []ModelInfo
	for _, cat := range c.accountCatalogs {
		if realmKey(cat.realm) != realmKey(realm) {
			continue
		}
		for _, mi := range cat.infos {
			if mi.ID == "" || realmSet[mi.ID] || seen[mi.ID] {
				continue
			}
			seen[mi.ID] = true
			out = append(out, mi)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// CheapestModel 测活用的模型：这个号能接的模型里积分倍率最低的一个。候选是这个域的目录加上这个号自己的
// 企业目录，按 ServesModel 过滤；倍率先看企业目录里的，再看域倍率表，不知道的排在知道的后面，同价按名字。
// skip 返回 true 的不算（models.hidden 里藏起来的）。目录都还没拉到时返回空串。
func (c *Client) CheapestModel(uid, realm string, enterprise bool, skip func(string) bool) string {
	if c == nil {
		return ""
	}
	rates := map[string]string{}
	c.effortsMu.RLock()
	for m := range c.realmModels[realmKey(realm)] {
		rates[m] = c.modelRates[realmKey(realm)][m]
	}
	c.effortsMu.RUnlock()
	c.accountMu.RLock()
	if cat, ok := c.accountCatalogs[uid]; ok {
		for _, mi := range cat.infos {
			if r := effectiveModelRate(mi); r != "" || rates[mi.ID] == "" {
				rates[mi.ID] = r
			}
		}
	}
	c.accountMu.RUnlock()

	best, bestRate, bestKnown := "", 0.0, false
	for m, raw := range rates {
		if m == "" || skip(m) || !c.ServesModel(uid, realm, enterprise, m) {
			continue
		}
		rate, err := strconv.ParseFloat(raw, 64)
		known := err == nil
		better := best == "" || (known && !bestKnown) ||
			(known == bestKnown && (known && rate < bestRate || (!known || rate == bestRate) && m < best))
		if better {
			best, bestRate, bestKnown = m, rate, known
		}
	}
	return best
}
