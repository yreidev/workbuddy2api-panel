// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// global 模型目录探测：纯动态产出模型名及其窗口 / 能力元数据（v3-config-merge）。
//
// 个人号只拉 /v3/config（桌面端内置 CLI 的 UA，与官方桌面端同一份目录）；企业号并发再探
// 企业模型目录（官方内置 CLI 的 ModelsProductProvider，fetchEnterpriseModels 补缺），
// 并集 = v3 条目为主、企业端点补 v3 缺失的 id。
// 倍率字段（credits）虽随目录下发，但只透出展示，不注入 costTier、不参与选号。
//
// 纯动态：不再回落任何静态名单——拉不出目录即意味着该域上游不可用，
// 假名单只会让客户端选到 11102 的模型（产品决策：无兜底）。
package upstream

import (
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/auth"
)

// GlobalModelNames 国际版（global realm）历史静态名单（PLAN §7.2 附录 21 名）。
// 纯动态化后**不再作为模型目录的基底/兜底**：/v1/models 只透出上游实际下发的模型。
// 保留仅作历史对照（global e2e 观测日志差集参照）。
var GlobalModelNames = []string{
	"default-model",
	"fast-model",
	"balanced-model",
	"primary-model",
	"hy4-preview",
	"gpt-5.6-sol",
	"gpt-5.6-terra",
	"deep-model",
	"deepseek-v4.1-flash",
	"gpt-6-astra",
	"hy4-preview-f",
	"hy3",
	"glm-5.2",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4",
	"gpt-5.3-codex",
	"gemini-3.5-flash",
	"glm-5.3",
	"kimi-k3",
	"kimi-k2.6",
}

// fetchGlobalModelsCache 探测结果缓存（语义参照 CN 侧 handler.dynamicModelsCache：1h TTL +
// 5min 失败负缓存）。按 Client 实例持有（effortsMu 同模式），测试新建 Client 即隔离。
// Mutex 内嵌，与 modelList 无并发读路径竞争（唯一读写点本文件内）。
type fetchGlobalModelsCache struct {
	sync.Mutex
	names    []string    // 成功缓存：并集模型名（已去重）；nil = 未探测/失败
	infos    []ModelInfo // 成功缓存：对象形态的全字段条目（窄表/失败形态为 nil）
	fetched  time.Time
	lastFail time.Time
}

// globalModelsTTL / globalModelsFailCooldown 探测缓存时长：成功 1h，失败 5min 负缓存。
const (
	globalModelsTTL          = time.Hour
	globalModelsFailCooldown = 5 * time.Minute
)

// FetchGlobalModels 探测 global 账号的模型名目录并返回**模型名列表**（无元数据）。
//
// 纯动态：成功返回并集结果（去重），缓存 1h；失败（两路全非 2xx / 解析失败 /
// 空列表）记 5min 负缓存，返回 nil（无静态回落）。缓存/负缓存命中：直接返回，零上游调用。
//
// 调用方负责：仅在有 global 账号时调用（无则不探测）；GlobalEnabled 关闭时（逃生门）
// 不得调用——本方法由 globalOn(a) 内部兜底，若账号因开关回落 cn 则返回 nil。
func (c *Client) FetchGlobalModels(a *auth.Auth) []string {
	names, _ := c.fetchGlobalModelsOnce(a)
	return names
}

// FetchGlobalModelInfos 探测 global 账号的模型目录并返回全字段 ModelInfo 列表。
// 与 FetchGlobalModels 共享同一次探测与缓存（names + infos 一体落缓存）：
// 对象形态 200 → 全字段条目；窄表形态 / 探测失败 / 负缓存 / 非 global 路由账号
// → nil（调用方按 ID 名单输出裸条目，不编造字段）。
// 账号因 GlobalEnabled 开关回落 cn 时不探测（globalOn 兜底，零上游调用）。
func (c *Client) FetchGlobalModelInfos(a *auth.Auth) []ModelInfo {
	_, infos := c.fetchGlobalModelsOnce(a)
	return infos
}

// fetchGlobalModelsOnce 单次探测决策（缓存命中/负缓存/触发探测），返回 (names, infos)。
// 纯动态：成功 = 并集结果去重；一切失败 = nil（不回落静态）。
// infos 仅对象形态成功探测时非 nil。
func (c *Client) fetchGlobalModelsOnce(a *auth.Auth) (names []string, infos []ModelInfo) {
	if !c.globalOn(a) {
		// 逃生门兜底：账号不路由 global 上游 → 不探测（零上游调用）。
		return nil, nil
	}

	c.globalModels.Lock()
	if len(c.globalModels.names) > 0 && time.Since(c.globalModels.fetched) < globalModelsTTL {
		names, infos := c.globalModels.names, c.globalModels.infos
		c.globalModels.Unlock()
		return names, infos
	}
	if !c.globalModels.lastFail.IsZero() && time.Since(c.globalModels.lastFail) < globalModelsFailCooldown {
		// 负缓存冷却期内：避免反复打上游，直接按失败处理（无静态回落）。
		c.globalModels.Unlock()
		return nil, nil
	}
	c.globalModels.Unlock()

	names, infos, efforts, defaults, err := c.probeGlobalModels(a)
	if err != nil || len(names) == 0 {
		// 探测失败：负缓存 + 返回 nil（effort 桶不写，prepareBody 走 globalEffortMap 静态兜底）。
		// 面板只能显示「上游未返回可用模型」，原因记在运行日志里。
		if err == nil {
			err = fmt.Errorf("目录是空的")
		}
		log.Printf("WARN: [upstream] global models: 拉国际版模型目录失败，%v 内不再重试：%v", globalModelsFailCooldown, err)
		c.globalModels.Lock()
		c.globalModels.lastFail = time.Now()
		c.globalModels.names = nil
		c.globalModels.infos = nil
		c.globalModels.Unlock()
		return nil, nil
	}
	// global 域 effort 能力：探测下发的 supportedEfforts/defaultEffort 权威写入 global 桶
	// （raw remote，不并入静态表——静态兜底在 prepareBody 的 globalEffortMap 与
	// /v1/models 的 EffortListing 里按需 fallback）。空探测不写（防清既有桶）。
	if len(efforts) > 0 || len(defaults) > 0 {
		c.storeEfforts("global", efforts, defaults)
	}
	c.storeModelRates("global", infos)

	// 成功：names/infos 均落缓存（探测时已按 id 去重）；倍率等选号敏感字段只透出展示，
	// 不注入 costTier。
	// 名单是多路探测的并集（比带倍率的 infos 全），跨域选号按它判断 global 有没有这个模型。
	c.storeRealmModels("global", names)

	c.globalModels.Lock()
	c.globalModels.names = names
	c.globalModels.infos = infos
	c.globalModels.fetched = time.Now()
	c.globalModels.lastFail = time.Time{}
	c.globalModels.Unlock()
	return names, infos
}

// probeGlobalModels 发起一次 global 模型目录探测：/v3/config（桌面端内置 CLI 的 UA）；企业号
// 并发再探企业模型目录（官方内置 CLI 的 ModelsProductProvider，补缺）后并集合并。返回模型名
// 列表（已合并、按 id 去重）、全字段 ModelInfo 及
// effort 能力桶（supportedEfforts/defaultEffort，可为空）。
//
// 以前还并发换 IDE UA、旧 CLI UA 拉 /v3/config，并探个人号的企业端点，各路独有的模型取并集。
// 同一个号用几种客户端身份拉目录是官方用户不会有的特征；10-19 生产实测那些独有模型对个人
// 免费号多半已下线（11102）或不开放，桌面端目录才是官方用户实际能选的。不在目录里的模型照样
// 能直接请求（加 `global:` 前缀可钉到国际版）。
// 企业号两路互不拖累：全部失败才返回错误，单路失败降级为另一路结果 + warn 日志。
func (c *Client) probeGlobalModels(a *auth.Auth) (names []string, infos []ModelInfo, efforts map[string][]string, defaults map[string]string, err error) {
	v3Ch := make(chan probeResult, 1)
	go func() {
		// chatBase 已按 realm 切 global base。
		byID, perr := c.fetchV3ConfigModelMap(a)
		if perr != nil {
			v3Ch <- probeResult{err: perr}
			return
		}
		outInfos := make([]ModelInfo, 0, len(byID))
		for _, mi := range byID {
			if nonChatModel(mi.ID, mi.MaxTokens, mi.Tags) {
				continue
			}
			outInfos = append(outInfos, mi)
		}
		// map 迭代序随机，排序保输出稳定；下游 extractEfforts 按 infos 的顺序产出名单。
		sort.Slice(outInfos, func(i, j int) bool { return outInfos[i].ID < outInfos[j].ID })
		v3Ch <- probeResult{infos: outInfos}
	}()
	enterpriseCh := make(chan probeResult, 1)
	if a.IsEnterprise() {
		go func() {
			// 企业模型目录：官方内置 CLI 的企业模型接口，按 realm 自动落到国际版 base。
			infos, perr := c.fetchEnterpriseModels(a)
			enterpriseCh <- probeResult{infos: infos, err: perr}
		}()
	} else {
		enterpriseCh <- probeResult{}
	}
	v3 := <-v3Ch
	enterprise := <-enterpriseCh
	if !a.IsEnterprise() {
		if v3.err != nil {
			return nil, nil, nil, nil, v3.err
		}
		names, infos, efforts, defaults = extractEfforts(v3.infos)
		return names, infos, efforts, defaults, nil
	}

	if v3.err != nil && enterprise.err != nil {
		// 两路全失败 → 负缓存语义（等价原家族端点全非 2xx）。
		return nil, nil, nil, nil, v3.err
	}
	if v3.err != nil {
		// /v3 失败降级：不拖累企业端点结果（降级仅企业端点 + warn）。
		log.Printf("WARN: [upstream] global models: v3/config probe failed (degraded to enterprise endpoint): %v", v3.err)
		names, infos, efforts, defaults = extractEfforts(enterprise.infos)
		return names, infos, efforts, defaults, nil
	}
	if enterprise.err != nil {
		log.Printf("WARN: [upstream] global models: enterprise endpoint failed (v3/config only): %v", enterprise.err)
		names, infos, efforts, defaults = extractEfforts(v3.infos)
		return names, infos, efforts, defaults, nil
	}
	// 两路皆成功：v3 为主、企业端点补缺合并（含 effort 桶合并，v3 权威）。
	v3Names, v3Infos, v3Efforts, v3Defaults := extractEfforts(v3.infos)
	entNames, entInfos, entEfforts, entDefaults := extractEfforts(enterprise.infos)
	names, infos = mergeGlobalCatalog(v3Names, v3Infos, entNames, entInfos)
	efforts = mergeEffortBuckets(v3Efforts, entEfforts)
	defaults = mergeEffortDefaults(v3Defaults, entDefaults)
	return names, infos, efforts, defaults, nil
}

// probeResult 单路模型目录探测结果（/v3/config，或企业模型目录）。
// infos 为对象形态条目；err 非 nil 表示该路失败。
type probeResult struct {
	infos []ModelInfo
	err   error
}

// extractEfforts 从条目列表抽取 effort 能力桶（supportedEfforts 数组优先；
// 缺数组但 defaultEffort 单档非空也入 defaults 桶）并顺带返回有序 names。
// 同一 id 只留第一条（与国内版 mergeModelInfos 同口径）：企业端点单独兜底时它的条目原样进
// 面板和 /v1/models，重复 id 会让面板模型表少行、一筛选就卡死（issue #6）。
func extractEfforts(infos []ModelInfo) (names []string, out []ModelInfo, efforts map[string][]string, defaults map[string]string) {
	names = make([]string, 0, len(infos))
	seen := make(map[string]bool, len(infos))
	for _, mi := range infos {
		if mi.ID == "" || seen[mi.ID] {
			continue
		}
		seen[mi.ID] = true
		names = append(names, mi.ID)
		out = append(out, mi)
		if len(mi.Efforts) > 0 {
			if efforts == nil {
				efforts = make(map[string][]string)
			}
			efforts[mi.ID] = mi.Efforts
		}
		if mi.DefaultEffort != "" {
			if defaults == nil {
				defaults = make(map[string]string)
			}
			defaults[mi.ID] = mi.DefaultEffort
		}
	}
	return names, out, efforts, defaults
}

// mergeGlobalCatalog 两路合并（v3 主、企业补缺）：names 按 id 去重（v3 原序在前、
// 企业端点补充项在其原序后追加——稳定输出）；infos 同步合并（v3 条目字段权威，
// 企业端点条目只在 id 缺失时进并集）。
// 窄表形态（infos nil）时保持 nil——无对象字段不编造。
func mergeGlobalCatalog(primaryNames []string, primaryInfos []ModelInfo, secondaryNames []string, secondaryInfos []ModelInfo) (names []string, infos []ModelInfo) {
	if len(secondaryNames) == 0 {
		return primaryNames, primaryInfos
	}
	seen := make(map[string]bool, len(primaryNames)+len(secondaryNames))
	out := make([]string, 0, len(primaryNames)+len(secondaryNames))
	for _, id := range primaryNames {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	var outInfos []ModelInfo
	if primaryInfos != nil {
		outInfos = make([]ModelInfo, 0, len(primaryInfos)+len(secondaryInfos))
		outInfos = append(outInfos, primaryInfos...)
	}
	for _, id := range secondaryNames {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		// 窄表企业响应（secondaryInfos nil / 超出条目数）时该 id 无对象字段，
		// infos 保持原样（调用方按 id 名单输出裸条目，不编造字段）。
		for _, mi := range secondaryInfos {
			if mi.ID == id {
				outInfos = append(outInfos, mi)
				break
			}
		}
	}
	return out, outInfos
}

// mergeEffortBuckets 合并两路 effort 桶：主路（v3）权威，企业端点只补主路缺失的模型档位。
func mergeEffortBuckets(primary, secondary map[string][]string) map[string][]string {
	if len(secondary) == 0 {
		return primary
	}
	out := primary
	if out == nil {
		out = make(map[string][]string, len(secondary))
	}
	for id, v := range secondary {
		if _, ok := out[id]; !ok {
			out[id] = v
		}
	}
	return out
}

// mergeEffortDefaults 合并两路 defaultEffort：主路（v3）权威，企业端点只补缺失。
func mergeEffortDefaults(primary, secondary map[string]string) map[string]string {
	if len(secondary) == 0 {
		return primary
	}
	out := primary
	if out == nil {
		out = make(map[string]string, len(secondary))
	}
	for id, v := range secondary {
		if _, ok := out[id]; !ok {
			out[id] = v
		}
	}
	return out
}
