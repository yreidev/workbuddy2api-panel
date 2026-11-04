// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

// main.go workbuddy2api 入口：加载配置、构建 pool、起调度器与 HTTP 服务。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	wb2api "github.com/yreidev/workbuddy2api-panel"
	"github.com/yreidev/workbuddy2api-panel/internal/apikeys"
	"github.com/yreidev/workbuddy2api-panel/internal/auth"
	"github.com/yreidev/workbuddy2api-panel/internal/livecfg"
	"github.com/yreidev/workbuddy2api-panel/internal/logarchive"
	"github.com/yreidev/workbuddy2api-panel/internal/logfmt"
	"github.com/yreidev/workbuddy2api-panel/internal/panel"
	"github.com/yreidev/workbuddy2api-panel/internal/pool"
	"github.com/yreidev/workbuddy2api-panel/internal/proxies"
	"github.com/yreidev/workbuddy2api-panel/internal/redisstore"
	"github.com/yreidev/workbuddy2api-panel/internal/reqlog"
	"github.com/yreidev/workbuddy2api-panel/internal/scheduler"
	"github.com/yreidev/workbuddy2api-panel/internal/selfupdate"
	"github.com/yreidev/workbuddy2api-panel/internal/server"
	"github.com/yreidev/workbuddy2api-panel/internal/session"
	"github.com/yreidev/workbuddy2api-panel/internal/upstream"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

// appVersion 网关版本，按语义化版本独立编号（不带 v）：有使用者看得到的改动就在推送前升版本号，
// 并在 docs/CHANGELOG.md 顶部写好这一版的条目（测试会核对两者一致）；推送到 main 后 CI 自动打
// vX.Y.Z 标签、建 Release、给镜像打版本标签。规则见 docs/releasing.md。
const appVersion = "1.13.1"

// usagePathFor 由 state 文件路径推出用量文件路径：同目录、文件名 usage.json。
// 这样 config 里改 state_file 时用量数据跟着走，不需要额外配置项。
func usagePathFor(stateFile string) string { return stateSibling(stateFile, "usage.json") }

// stateSibling 返回与 state 文件同目录的指定文件名路径（相对路径场景回落当前目录）。
// usage.json（用量记录）与 output_probes.json（模型上限探测）共用本规则。
func stateSibling(stateFile, name string) string {
	dir := filepath.Dir(stateFile)
	if dir == "" || dir == "." {
		return name
	}
	return filepath.Join(dir, name)
}

// 测试可替换的进程级入口：默认值就是原来的调用，行为不变。
var (
	exitFunc      = os.Exit
	launchUpdater = selfupdate.Launch
	notifyContext = signal.NotifyContext
	newUpstream   = upstream.New
	reexec        = (*selfupdate.Updater).Reexec
)

// fatalf 打印后以 1 退出（等价 log.Fatalf；退出函数换成变量便于测试）。
func fatalf(format string, args ...any) {
	log.Printf(format, args...)
	exitFunc(1)
}

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径（默认当前目录 config.json；不存在时自动生成推荐配置）")
	showVersion := flag.Bool("version", false, "打印版本号后退出（一键更新用它试运行新程序）")
	flag.Parse()
	if *showVersion {
		fmt.Println(appVersion)
		exitFunc(0)
		return
	}
	cfg, err := loadOrInitConfig(*cfgPath)
	if err != nil {
		fatalf("load config: %v", err)
	}
	// 出站 DNS：没有 /etc/resolv.conf 的环境里 Go 的解析器会退回问本机 53 端口，查什么都失败。
	// 配了 dns_servers / WB2A_DNS 就让整个进程只问它们；发出任何请求之前换。
	if len(cfg.DNSServers) > 0 {
		upstream.UseDNSServers(cfg.DNSServers)
		log.Printf("DNS: 出站解析只问 %v（不走系统解析器）", cfg.DNSServers)
	}
	// 数据目录里有面板装好的新版就 exec 过去（不返回）：放在读取账号、恢复状态之前，什么都还没动。
	upd := launchUpdater(filepath.Dir(cfg.StateFile), appVersion)
	sigCtx, stop := notifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	restart, err := run(sigCtx, cfg, *cfgPath, upd, newUpstream(), nil)
	stop()
	if err != nil {
		fatalf("%v", err)
	}
	// 面板一键更新：run 优雅停机返回后原地换成自带程序（它再决定是否切到数据目录里的新版），
	// 放在 run 之外，让 run 里的 defer（池状态落盘、用量与请求日志收尾）先跑完。
	if restart {
		log.Printf("重启中……")
		if err := reexec(upd); err != nil {
			fatalf("重启失败：%v", err)
		}
	}
}

// loadOrInitConfig 读取配置；文件不存在时先落一份推荐配置（含随机 api_key）再读，
// 生成失败（目录只读等）退回纯默认 + env，不阻塞启动。错误交给 main 打印。
func loadOrInitConfig(path string) (*Config, error) {
	cfg, err := Load(path)
	if err != nil {
		// errors.Is 才能看穿 Load 里 fmt.Errorf("%w") 的包装；os.IsNotExist 不行。
		if errors.Is(err, fs.ErrNotExist) {
			// 首次运行：目录下没有配置 → 自动落一份推荐配置（含随机 api_key）再加载。
			// 双击 exe / 裸跑 docker 即开，无需先手工复制样例。
			if key, werr := WriteDefault(path); werr == nil {
				log.Printf("config %s 不存在，已生成推荐配置（api_key=%s，记录在该文件里，可自行修改）", path, key)
				cfg, err = Load(path)
			}
			if err != nil {
				// 生成失败（目录只读等）：退回纯默认 + env（旧行为兜底），不阻塞启动。
				log.Printf("config %s not found (auto-generate failed), using defaults+env: %v", path, err)
				cfg, err = Load("")
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

func run(ctx context.Context, cfg *Config, cfgPath string, upd *selfupdate.Updater, up *upstream.Client, ln net.Listener) (restart bool, err error) {
	auths, err := auth.LoadDir(cfg.AuthDir)
	if err != nil {
		return false, fmt.Errorf("load auths: %w", err)
	}
	log.Printf("loaded %d account(s) from %s", len(auths), cfg.AuthDir)

	// redisstore：未配置/连接失败 → Noop（纯内存模式，一切功能照常）。
	store := redisstore.New(cfg.Upstash.URL, cfg.Upstash.Token)

	p := pool.New(cfg.StateFile)
	// 停机序：先 pool.Close()（最后一次 Flush → SaveState 已提交到 store），
	// 再 store.Close() 排空在途异步写（最后一笔 Redis 镜像必须写完才关连接）。
	defer func() {
		p.Close()
		_ = store.Close()
	}()
	p.SetStore(store)
	p.RestoreFromSnapshot() // 择新恢复：Redis 快照比本地新才采用，否则本地优先
	p.SyncToDir(auths)      // 与 auths 目录对齐：新账号加入、已删除文件账号剔除（状态保留）

	// 熔断器 + 在途上限（含 global 分档）+ 连败降权 + 闲置补偿调优（从 config 注入，
	// 非正值回退默认）。
	p.SetBreaker(cfg.Pool.BreakerThreshold, cfg.BreakerCooldownDur, cfg.BreakerCooldownMaxD)
	p.SetMaxInFlight(cfg.Pool.MaxInFlight)
	p.SetMaxInFlightGlobal(cfg.Pool.MaxInFlightGlobal) // global 域 WAF 风控分档（P1-1）
	p.SetInFlightWait(cfg.InFlightWaitDur)             // 号都在途占满时排队等空位（默认 30s；0 不等）
	p.SetMaxRPM(cfg.Pool.MaxRPM)                       // 每号每分钟请求数上限（0 不限），超了同在途满
	p.SetMaxRPMGlobal(cfg.Pool.MaxRPMGlobal)
	p.SetDegrade(cfg.Pool.DegradeThreshold, cfg.DegradeCooldownDur, cfg.DegradeCooldownMaxD)
	p.SetSoftRateMax(cfg.SoftRateMaxDur)                 // 软冷却指数退避封顶（soft_rate_max，默认 2h）
	p.SetCostExploreInterval(cfg.CostExploreIntervalDur) // costTier 探索窗口（issue #136，默认 30m；0 关停）
	p.SetCreditFloor(cfg.Pool.CreditFloor)               // 积分保底（默认 0 = 关闭）
	p.SetRestrictionWave(cfg.Pool.RestrictionWaveThreshold, cfg.RestrictionWaveWindowDur, cfg.RestrictionWavePauseDur)
	p.SetWeights(cfg.Pool.IdleWeightPerHour, cfg.Pool.IdleWeightMax)
	p.SetPreferExpiring(cfg.Pool.PreferExpiring)

	// 会话粘性路由（可配关闭）。
	var sessRouter *session.Router
	redisMode := "noop"
	if _, ok := store.(redisstore.Noop); !ok {
		redisMode = "upstash"
	}
	if cfg.SessionSticky.Enabled {
		sessRouter = session.New(sessionConfig(cfg.SessionTTL, cfg.SessionGCInterval, store, p))
		sessRouter.LoadFromStore() // 启动时从 Redis 恢复粘性（读操作仅此处）
		sessRouter.StartGC()
		defer sessRouter.StopGC()
	}
	// 会话记录（出站会话 id、每段对话用过的号）：与 state 文件同目录，配置了 Redis 时再镜像一份，
	// 重启后同一段对话照旧回到原来的号、用原来的会话 id（issue #2）。defer 排在 store.Close 之后
	// 注册、先于它执行：最后一份快照提交到 Redis 后才排空关连接。
	sessPersist := session.NewPersist(stateSibling(cfg.StateFile, "sessions.json"), sessRouter, store)
	sessPersist.Load()
	sessPersist.Start()
	defer sessPersist.Stop()
	sessCount := func() int {
		if sessRouter != nil {
			return sessRouter.Count()
		}
		return 0
	}

	wireModelCatalog(p, up) // 位于 up 装配之后：倍率表与目录由探测下发，选号时读实时快照

	// 短 RPC 总时长上限（refresh/checkin/balance/FetchModels），语义不变。
	up.HTTP.Timeout = time.Duration(cfg.Upstream.TimeoutSeconds) * time.Second
	// 聊天 SSE 首字节前（响应头）上限：cfg 已 normalize（缺省回落 timeout_seconds）。
	up.SetHeaderTimeout(time.Duration(cfg.Upstream.HeaderTimeoutSeconds) * time.Second)
	// 聊天 SSE 流中空闲上限（S3 空闲监控读取）。
	up.IdleTimeout = time.Duration(cfg.Upstream.IdleTimeoutSeconds) * time.Second
	up.SanitizeFingerprints.Store(cfg.Features.SanitizeBlacklistFingerprints)
	// 出站 UA 与归属头（issue #42 + 上游同步）：
	// UserAgent 非空则完全覆盖；ClientVersion/CliVersion 缺省对齐官方形态；
	// ClientName 非空时 chat 路径注入 X-IDE-* 四头（用量归因对齐官方桌面端）。
	up.UserAgent = cfg.Upstream.UserAgent
	up.ClientVersion = cfg.Upstream.ClientVersion
	up.CliVersion = cfg.Upstream.CliVersion
	up.ClientName = cfg.Upstream.ClientName
	up.DeviceToken = cfg.Upstream.DeviceToken
	// 出站 TLS 指纹伪装（默认开）：关掉回落标准库 TLS。改这项要重启。
	upstream.SetTLSMimic(cfg.Upstream.TLSFingerprint)
	up.DeviceTokenFile = cfg.Upstream.DeviceTokenFile
	up.PassthroughIP = cfg.Upstream.PassthroughIP
	up.ModelOptimization.Store(cfg.Upstream.ModelOptimization)
	// global realm 路由（config global 段）：上游侧开关（第一道闸）+ base 覆盖；
	// auth 侧开关（auth.SetGlobalEnabled）是第二道闸，两者同 config global.enabled。
	up.GlobalEnabled = cfg.Global.Enabled
	up.ChatBaseGlobal = cfg.Global.ChatBase
	up.BillingBaseGlobal = cfg.Global.BillingBase
	auth.SetGlobalEnabled(cfg.Global.Enabled)
	// model.json 本地缓存接线（context_length/max_output_tokens 四级查找链第 3 级）：
	// 数据目录与 state.json 同风格（Docker volume 持久化路径）。首次缺失/损坏自动
	// 回落仓库内嵌种子；models.dev 按需拉取成功后原子写回。
	upstream.SetModelCatalogPath(stateSibling(cfg.StateFile, "model.json"))

	// 代理池（按号代理）：与 state 文件同目录。读不了就不启动——当成没有代理会让本该走代理的号从服务器
	// IP 直接出去。在调度器和面板发出第一个号的请求之前接好。
	proxyReg, err := proxies.Open(stateSibling(cfg.StateFile, "proxies.json"))
	if err != nil {
		return false, err
	}
	wireProxies(proxyReg, p, up, auths)
	if v := proxyReg.View(); len(v.Proxies) > 0 {
		log.Printf("[proxy] 代理池已载入：%d 个池、%d 个代理", len(v.Pools), len(v.Proxies))
	}

	sch := scheduler.New(scheduler.Config{
		Pool:           p,
		Upstream:       up,
		CheckinHours:   cfg.Schedule.CheckinHours,
		TravelHours:    cfg.Schedule.TravelHours,
		ActivityHours:  cfg.Schedule.ActivityHours,
		KeepaliveHours: cfg.Schedule.KeepaliveHours,
		BlackcatHours:  cfg.Schedule.BlackcatHours,
		GrowthHours:    cfg.Schedule.GrowthHours,
		// 快过期积分优先消耗：签到/余额刷新按此窗口分桶（issue:积分过期）。
		ExpiringSoonWindow: cfg.ExpiringSoonDur,
		CheckinDisabled:    !cfg.Schedule.CheckinEnabled,
		TravelDisabled:     !cfg.Schedule.TravelEnabled,
		ActivityDisabled:   !cfg.Schedule.ActivityEnabled,
		KeepaliveDisabled:  !cfg.Schedule.KeepaliveEnabled,
		BlackcatDisabled:   !cfg.Schedule.BlackcatEnabled,
		GrowthDisabled:     !cfg.Schedule.GrowthEnabled,
		// 保号类四任务是否覆盖禁用账号（缺省 false = 禁用即跳过，保持既有行为）。
		IncludeDisabledInTasks: cfg.Schedule.IncludeDisabledInTasks,
	})
	switch {
	case !cfg.Schedule.CheckinEnabled:
		log.Printf("签到已禁用（schedule.checkin_enabled=false）")
	default:
		log.Printf("签到已启用：%v 点（签到 + 余额查询解冻）", cfg.Schedule.CheckinHours)
	}
	switch {
	case !cfg.Schedule.TravelEnabled:
		log.Printf("猫猫旅行已禁用（schedule.travel_enabled=false）")
	default:
		log.Printf("猫猫旅行已启用：%v 点（独立排程：领养 / 派出 / 领奖）", cfg.Schedule.TravelHours)
	}
	switch {
	case !cfg.Schedule.ActivityEnabled:
		log.Printf("活跃上报已禁用（schedule.activity_enabled=false）")
	default:
		log.Printf("活跃上报已启用：%v 点（每日 1 次，点亮连登 + 解锁 first_buddy）", cfg.Schedule.ActivityHours)
	}
	if !cfg.Schedule.KeepaliveEnabled {
		log.Printf("token 保活已禁用（schedule.keepalive_enabled=false）")
	} else {
		log.Printf("token 保活已启用：%v 点", cfg.Schedule.KeepaliveHours)
	}
	switch {
	case !cfg.Schedule.BlackcatEnabled:
		log.Printf("夜猫子已禁用（schedule.blackcat_enabled=false）")
	default:
		log.Printf("夜猫子已启用：%v 点（23:00–08:00 窗口 glm-5.2 对话补足）", cfg.Schedule.BlackcatHours)
	}
	switch {
	case !cfg.Schedule.BalanceRefreshEnabled:
		log.Printf("余额后台刷新已禁用（schedule.balance_refresh_enabled=false）")
	case cfg.BalanceRefreshInterval > 0:
		log.Printf("余额后台刷新：每 %s（签到时点照常额外刷新）", cfg.BalanceRefreshInterval)
	}
	if !cfg.Schedule.LivenessEnabled {
		log.Printf("定时测活已禁用（schedule.liveness_enabled=false）")
	}
	switch cfg.Pool.CrossRealm {
	case livecfg.CrossRealmMerge:
		log.Printf("跨域负载均衡：合池（pool.cross_realm=merge），不带前缀的模型名在国内版 + 国际版合池选号")
	case livecfg.CrossRealmCNFirst:
		log.Printf("跨域负载均衡：国内版优先（pool.cross_realm=cn_first），国内版此刻没有能接的号时不带前缀的模型名才用国际版")
	}
	if cfg.Schedule.IncludeDisabledInTasks {
		log.Printf("保号任务覆盖禁用账号（schedule.include_disabled_in_tasks=true）：禁用号仍签到 / 活跃 / 保活 / 刷新余额，但不参与选号")
	}

	// 管理面板日志镜像：标准 log（stderr）与 chat 表格日志（stdout）双路复制进
	// 面板环形缓冲，供 /panel/api/logs 读取；控制台输出行为完全不变。
	// live 承载可热改字段（api_key/soft_rate/脱敏开关），面板保存配置时在线替换。
	live := livecfg.New(livecfg.Snapshot{
		APIKey:               cfg.APIKey,
		SoftCooldown:         cfg.SoftRateDur,
		SanitizeFingerprints: cfg.Features.SanitizeBlacklistFingerprints,
		RecordClientInfo:     cfg.Logging.RequestClientInfo,
		CrossRealm:           string(cfg.Pool.CrossRealm),
		UpdateCheck:          cfg.Panel.UpdateCheck,
		ModelRoutes:          cfg.ModelRoutes,
		HiddenModels:         livecfg.NormalizeHiddenModels(cfg.Models.Hidden),
	})
	// 用量记录器：与 state 文件同目录，随 state_file 配置一起搬移。
	// datapath 由 state 文件路径推出，避免再加一个配置项。
	usagePath := usagePathFor(cfg.StateFile)
	rec := usage.New(usagePath)
	rec.SetRetentionDays(cfg.Logging.UsageRetentionDays)
	rec.Start()
	defer rec.Stop()
	log.Printf("[usage] 逐请求用量记录已启用: %s (%s，%s)", usagePath, rec.Describe(), retentionText(cfg.Logging.UsageRetentionDays))

	// 客户端密钥（面板里建、发给别人调 /v1/* 的密钥）：与 state 文件同目录。读不了就不启动——
	// 当成没有密钥会让「建过密钥就必须带」的规则悄悄失效，接口变成谁都能调。
	keys, err := apikeys.Open(stateSibling(cfg.StateFile, "api_keys.json"))
	if err != nil {
		return false, err
	}
	keys.Start()
	defer keys.Stop()
	if n := len(keys.List(time.Now())); n > 0 {
		log.Printf("[apikeys] 已载入 %d 个客户端密钥：/v1/* 必须带有效密钥", n)
	}

	// 请求指标始终启用；JSONL 归档只写脱敏元数据，写盘失败不影响聊天请求。
	requestLog := reqlog.New(reqlog.Config{
		Dir:           stateSibling(cfg.StateFile, "request-logs"),
		Enabled:       cfg.Logging.RequestArchiveEnabled,
		RetentionDays: cfg.Logging.RequestRetentionDays,
		MaxBytes:      int64(cfg.Logging.RequestArchiveMaxMB) << 20,
	})
	defer requestLog.Close()
	rs := requestLog.ArchiveStats()
	if rs.Enabled {
		log.Printf("[reqlog] 请求指标已启用；JSONL 归档 %s（%s，上限 %d MiB）",
			rs.Dir, retentionText(cfg.Logging.RequestRetentionDays), cfg.Logging.RequestArchiveMaxMB)
	} else {
		log.Printf("[reqlog] 请求指标已启用；JSONL 归档已关闭")
	}

	// 运行日志的任务、系统两个频道落盘留存（data/runtime-logs/task-<日期>.jsonl、system-<日期>.jsonl），
	// 面板按时间范围查历史。没开留存也建：要按天数清掉以前留下的文件，运行中打开时接着写。
	runtimeLogDir := stateSibling(cfg.StateFile, "runtime-logs")
	taskLog := logarchive.New(logarchive.Config{
		Dir: runtimeLogDir, Prefix: "task", Enabled: cfg.Logging.TaskLogEnabled,
		RetentionDays: cfg.Logging.TaskLogRetentionDays, MaxBytes: int64(cfg.Logging.TaskLogMaxMB) << 20,
	})
	defer taskLog.Close()
	systemLog := logarchive.New(logarchive.Config{
		Dir: runtimeLogDir, Prefix: "system", Enabled: cfg.Logging.SystemLogEnabled,
		RetentionDays: cfg.Logging.SystemLogRetentionDays, MaxBytes: int64(cfg.Logging.SystemLogMaxMB) << 20,
	})
	defer systemLog.Close()
	log.Printf("[logs] 运行日志留存 %s：任务日志%s，系统日志%s", runtimeLogDir,
		runtimeLogText(cfg.Logging.TaskLogEnabled, cfg.Logging.TaskLogRetentionDays, cfg.Logging.TaskLogMaxMB),
		runtimeLogText(cfg.Logging.SystemLogEnabled, cfg.Logging.SystemLogRetentionDays, cfg.Logging.SystemLogMaxMB))
	// 积分获取流水（data/credit-logs/credit-<日期>.jsonl）：上游客户端每领到一笔积分记一行，面板「积分获取」
	// 按时间范围查。挂在 up 上，要在调度器和面板发出第一个请求之前设好。
	creditLog := logarchive.New(logarchive.Config{
		Dir: stateSibling(cfg.StateFile, "credit-logs"), Prefix: "credit", Enabled: cfg.Logging.CreditLogEnabled,
		RetentionDays: cfg.Logging.CreditLogRetentionDays, MaxBytes: int64(cfg.Logging.CreditLogMaxMB) << 20,
	})
	defer creditLog.Close()
	up.OnEarning = func(e upstream.Earning) { creditLog.Append(e.Time, e) }
	log.Printf("[logs] 积分获取流水 %s：%s", stateSibling(cfg.StateFile, "credit-logs"),
		runtimeLogText(cfg.Logging.CreditLogEnabled, cfg.Logging.CreditLogRetentionDays, cfg.Logging.CreditLogMaxMB))
	logs := logStores{requests: requestLog, task: taskLog, system: systemLog, credit: creditLog, usage: rec}

	pn := panel.New(panel.Config{
		Pool:        p,
		Usage:       rec,
		Keys:        keys,
		RequestLog:  requestLog,
		TaskLog:     taskLog,
		SystemLog:   systemLog,
		CreditLog:   creditLog,
		Upstream:    up,
		Scheduler:   sch,
		AuthDir:     cfg.AuthDir,
		APIKey:      cfg.APIKey,
		RedisMode:   redisMode,
		StickyCount: sessCount,
		Version:     appVersion,
		Changelog:   wb2api.Changelog,
		Updater:     upd,
		Live:        live,
		// 模型上限探测数据（scripts/probe_max_tokens.py --panel-out 写入）：
		// 与 state 文件同目录，缺省 data/output_probes.json。
		ProbeFile:  stateSibling(cfg.StateFile, "output_probes.json"),
		Proxies:    proxyReg,
		ConfigPath: cfgPath,
		LoadConfig: func() (any, error) {
			return Load(cfgPath)
		},
		SaveConfig: func(raw []byte) ([]string, error) {
			return saveConfig(raw, cfgPath, live, p, up, sch, logs)
		},
	})
	// 成长任务队列每日自动执行（与「执行全部待办」同管线）：Sequential 族零点解锁后
	// 无需手动扫描；hook 返回即启动（异步执行），已在跑时内部跳过。
	sch.SetGrowthHook(pn.RunGrowthQueueOnce)
	// 对话请求的表格流水行只打 stdout，不进面板的运行日志（面板看「请求记录」）。
	log.SetOutput(io.MultiWriter(os.Stderr, pn.Logs()))

	h := server.NewHandler(server.Config{
		Pool:         p,
		Upstream:     up,
		APIKey:       cfg.APIKey,
		Keys:         keys,
		Session:      sessRouter,
		StickyCount:  sessCount,
		ProxyName:    proxyReg.ProxyName,
		RedisMode:    redisMode,
		SoftCooldown: cfg.SoftRateDur,
		Panel:        pn,
		Live:         live,
		Usage:        rec,
		RequestLog:   requestLog,
		PromptMode:   cfg.Prompt.Mode,
		PromptText:   cfg.PromptText,
		// 来源记录开关经 livecfg 热生效；此处同时填静态字段，供 Live 为 nil 的
		// 裸用/测试路径拿到同一缺省值。
		RecordClientInfo: cfg.Logging.RequestClientInfo,
		// handler 侧第三道闸（global realm）：false（显式逃生门）时不列 global: 模型名。
		GlobalEnabled: cfg.Global.Enabled,
		Version:       appVersion,
	})
	// 测活走 handler 的对话链路（面板单测、全部测活、定时测活都经 scheduler 调它）。
	sch.SetLiveness(h.TestAccount)

	// 先占端口再起后台任务和停机协程：端口被占时直接返回，不先跑调度、不打停机日志。
	if ln == nil {
		ln, err = net.Listen("tcp", cfg.Listen)
		if err != nil {
			return false, fmt.Errorf("http: %w", err)
		}
	}

	// 面板一键更新 / 退回走和停止信号同一条优雅停机路径，停完由 main 原地重启。
	ctx, requestRestart := context.WithCancel(ctx)
	defer requestRestart()
	var restarting atomic.Bool
	upd.Restart = func() {
		restarting.Store(true)
		requestRestart()
	}
	go sch.Run(ctx)
	go proxyReg.Run(ctx)
	sch.StartBalanceRefresh(ctx, cfg.BalanceRefreshInterval)
	sch.StartLiveness(ctx, cfg.LivenessInterval)
	pn.StartUpdateCheck(ctx)

	// 两个域的模型目录（含积分倍率表）启动先拉一遍，之后定时补拉、刷新（见 refreshModelCatalogs）。
	// 异步执行：不阻塞监听启动。
	go refreshModelCatalogs(ctx, up, p)
	go refreshEnterpriseCatalogs(ctx, up, p)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: 30 * time.Second,
		// ReadTimeout 覆盖整个请求读取（含 body 上传）：防慢速 body 拖死连接。
		// 请求体已无网关侧上限（max_body_mb 移除）。缺省 300s（issue #100：旧固定
		// 60s 会掐掉大上下文/文件块经反代链的慢速上传，客户端收到
		// 400 "read body: ... i/o timeout"）；server.read_timeout="0" 显式关闭。
		// 改动需重启进程。
		ReadTimeout: cfg.ServerReadTimeoutDur,
		// IdleTimeout keep-alive 空闲连接回收：配合 chat 出站 ctx 传播防连接泄漏堆积。
		// 注意：SSE 流式响应期间连接非空闲，不受此项掐断；不设全局 WriteTimeout
		// （长流式生成合法时长可达数分钟，全局 WriteTimeout 会误杀在途 SSE）。
		IdleTimeout: 120 * time.Second,
	}
	grace, graceErr := shutdownGrace(os.Getenv("WB2A_SHUTDOWN_TIMEOUT"))
	if graceErr != nil {
		log.Printf("WB2A_SHUTDOWN_TIMEOUT 无效（%v），优雅停机沿用默认 %s", graceErr, grace)
	}
	upd.Grace = grace
	// 优雅停机：收到 SIGTERM/SIGINT 后 /readyz 立刻转 503（编排系统据此摘流量），
	// 先落盘，再 Shutdown——停止接新连接，等在途请求（含流式对话）结束，最多等 grace。
	shutdownDone := make(chan struct{})
	var listenFailed atomic.Bool
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		server.SetDraining()
		p.Flush() // 信号触发：先落盘再做优雅停机
		sessPersist.Flush()
		switch {
		case restarting.Load():
			log.Printf("准备重启：/readyz 已转 503，停止接新连接，最多等 %s 让在途请求结束", grace)
		case listenFailed.Load():
			log.Printf("监听出错，停止服务：/readyz 已转 503，最多等 %s 让在途请求结束", grace)
		default:
			log.Printf("收到停止信号：/readyz 已转 503，停止接新连接，最多等 %s 让在途请求结束", grace)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("等待 %s 后仍有请求未结束，强制退出：%v", grace, err)
		}
	}()

	// 面板装的新版稳定跑满 30 秒（或走完优雅停机）才算成功；没做到就退出的，连续 3 次后自带程序不再切过去。
	stable := time.AfterFunc(selfupdate.StableAfter, upd.MarkStable)
	defer stable.Stop()

	log.Printf("workbuddy2api listening on %s (api_key=%v)，管理面板 http://127.0.0.1%s/panel/", cfg.Listen, cfg.APIKey != "", panelListenPath(cfg.Listen))
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		// 监听出错也走一遍停机，等最后一次落盘和在途请求做完再返回：直接返回的话，
		// 停机协程会在进程退出、后面的 defer 收尾时还在写数据目录
		listenFailed.Store(true)
		requestRestart()
		<-shutdownDone
		return false, fmt.Errorf("http: %w", err)
	}
	// Shutdown 一开始 ListenAndServe 就会返回 ErrServerClosed，此时在途请求还没结束：
	// 必须等 Shutdown 返回再退出 main，否则进程立刻结束，等待时间形同虚设，
	// 后面的 defer（池状态最后一次落盘、用量与请求日志收尾）也会和在途请求抢跑。
	<-shutdownDone
	// 能走完优雅停机说明这一版能正常运行：没跑满 30 秒就被正常停掉（如重建容器）不算启动失败
	upd.MarkStable()
	if restarting.Load() {
		return true, nil
	}
	log.Printf("bye")
	return false, nil
}

// defaultShutdownGrace 优雅停机默认最长等待（未设置 WB2A_SHUTDOWN_TIMEOUT 时，与历史行为一致）。
const defaultShutdownGrace = 5 * time.Second

// shutdownGrace 解析 WB2A_SHUTDOWN_TIMEOUT（Go 时长，如 570s、10m）。未设置返回默认 5 秒；
// 格式不对或不是正数也回落默认，并返回错误供启动日志提示。
func shutdownGrace(v string) (time.Duration, error) {
	if v == "" {
		return defaultShutdownGrace, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultShutdownGrace, err
	}
	if d <= 0 {
		return defaultShutdownGrace, fmt.Errorf("%q 不是正数", v)
	}
	return d, nil
}

// 两个域的模型目录（域目录 + 积分倍率表）的刷新节奏：启动先拉一遍，之后每 catalogTick 看一次，没拉到过的域
// （启动时没有可用的号、上游出错）接着补拉，拉到过的超过 catalogMaxAge 重拉。
//
// 为什么要定时：目录只在这里和 /v1/models、面板模型页被访问时才拉，对话请求只在配了模型路由时顺带拉。
// 启动那一次没拉到、之后又没人调 /v1/models，就一直没有目录：开着跨域负载均衡时不带前缀的模型名全落到
// 国内版（没有国内版号就一直 503）；积分保底的目录倍率兜底也判不出收费，触底号会被当成「收费未知」放行
// 并打穿（实测：重启后 2 分钟，97 分的账号打收费模型归零）。tick 是变量，测试调短以验证第二轮。
var catalogTick = time.Minute

const catalogMaxAge = 30 * time.Minute

func refreshModelCatalogs(ctx context.Context, up *upstream.Client, p *pool.Pool) {
	t := time.NewTicker(catalogTick)
	defer t.Stop()
	st := newCatalogRefresh()
	for {
		st.once(ctx, up, p, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// catalogRefresh 各域上次拉成功的时刻（没有 = 还没拉到过），以及上次是不是拉失败了：失败日志只在第一次
// 失败、由好转坏时记，免得每分钟一条。
type catalogRefresh struct {
	last    map[string]time.Time
	failing map[string]bool
}

func newCatalogRefresh() *catalogRefresh {
	return &catalogRefresh{last: map[string]time.Time{}, failing: map[string]bool{}}
}

// once 拉一遍到点的域。该域此刻没有可用的号时跳过，不算失败，下一轮再看。
func (st *catalogRefresh) once(ctx context.Context, up *upstream.Client, p *pool.Pool, now time.Time) {
	// 不得拖住进程退出：ctx 取消（SIGINT/SIGTERM）时立刻放弃剩余域。
	if ctx.Err() == nil && st.due("cn", now) {
		// CN：有可用 CN 账号才拉（与面板 models 同口径，避免无谓上游调用）；优先用个人号。
		if uid := p.CatalogUIDForRealm("cn"); uid != "" {
			if a := p.AuthByUID(uid); a != nil {
				_, err := up.FetchModels(a)
				st.note("cn", now, err)
			}
		}
	}
	// global：独立目录端点（workbuddy.ai），倍率按 "global" 域键存储；只在国际版开着时拉。
	if up.GlobalEnabled && ctx.Err() == nil && st.due("global", now) {
		if uids := p.AvailableUIDsForRealm("global"); len(uids) > 0 {
			if a := p.AuthByUID(uids[0]); a != nil {
				// FetchGlobalModelInfos 无错误返回（内部负缓存自行节流），仅按结果条数判断是否拿到目录。
				var err error
				if infos := up.FetchGlobalModelInfos(a); len(infos) == 0 {
					err = errors.New("empty model list")
				}
				st.note("global", now, err)
			}
		}
	}
}

// due 该域到没到拉的时候：没拉到过，或者上次拉到已超过 catalogMaxAge。
func (st *catalogRefresh) due(realm string, now time.Time) bool {
	last, ok := st.last[realm]
	return !ok || now.Sub(last) >= catalogMaxAge
}

// note 记下这次拉的结果。失败时不动上次成功的时刻（上游那边沿用上次的目录），下一轮接着补拉。
func (st *catalogRefresh) note(realm string, now time.Time, err error) {
	if err != nil {
		if !st.failing[realm] {
			log.Printf("WARN: [upstream] model catalog (%s): %v，每 %s 重试", realm, err, catalogTick)
		}
		st.failing[realm] = true
		return
	}
	if _, ok := st.last[realm]; !ok || st.failing[realm] {
		log.Printf("[upstream] model catalog: %s ok", realm)
	}
	st.last[realm] = now
	st.failing[realm] = false
}

// 企业号目录的刷新节奏：每 enterpriseCatalogTick 检查一次，目录缺失或超过
// enterpriseCatalogMaxAge 的号重拉。新加的企业号最迟一个 tick 内拉到目录；拉到之前它照常接请求。
// tick 是变量，测试调短以验证第二轮刷新。
var enterpriseCatalogTick = 5 * time.Minute

const enterpriseCatalogMaxAge = 30 * time.Minute

// refreshEnterpriseCatalogs 定时给国内版企业号各拉一份自己的模型目录（启动时先跑一轮），
// 并清掉已移除、已禁用账号的目录。单号失败只记 WARN，保留它上次的目录；被代理池拦着的号不拉，也保留。
func refreshEnterpriseCatalogs(ctx context.Context, up *upstream.Client, p *pool.Pool) {
	t := time.NewTicker(enterpriseCatalogTick)
	defer t.Stop()
	for {
		refreshEnterpriseCatalogsOnce(ctx, up, p, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func refreshEnterpriseCatalogsOnce(ctx context.Context, up *upstream.Client, p *pool.Pool, now time.Time) {
	uids := p.EnterpriseUIDs("cn")
	keep := make(map[string]bool, len(uids))
	for _, uid := range uids {
		keep[uid] = true
		if ctx.Err() != nil {
			return
		}
		if at, ok := up.AccountCatalogFetched(uid); ok && now.Sub(at) < enterpriseCatalogMaxAge {
			continue
		}
		a := p.AuthByUID(uid)
		if a == nil {
			continue
		}
		if st, _ := p.Status(uid); st.ProxyBlocked != "" {
			continue // 代理池拦着（请求出不去）：留着上次的目录，放开后下一轮再拉
		}
		if infos, err := up.FetchAccountCatalog(a); err != nil {
			log.Printf("WARN: [upstream] enterprise catalog %s: %v", logfmt.Label(a.UID, a.Nickname), err)
		} else {
			log.Printf("[upstream] enterprise catalog %s: %d models", logfmt.Label(a.UID, a.Nickname), len(infos))
		}
	}
	up.PruneAccountCatalogs(keep)
}

// panelListenPath 从 listen 地址提取 ":port" 形式，用于启动日志拼面板 URL
// （":7863" 或 "0.0.0.0:7863" → ":7863"；异常输入原样返回）。
func panelListenPath(listen string) string {
	for i := len(listen) - 1; i >= 0; i-- {
		if listen[i] == ':' {
			return listen[i:]
		}
	}
	return listen
}

// renameConfig 是 saveConfig 落盘用的 rename；测试换成返回 EBUSY 的假函数，模拟单文件 bind mount。
var renameConfig = os.Rename

// saveConfig 面板保存配置：校验 → 落盘 → 热应用 → 返回需重启的字段列表。
//
// 热生效范围（设计取舍）：
//   - api_key / cooldown.soft_rate / features.sanitize_blacklist_fingerprints / pool.cross_realm / panel.update_check / model_routes / models.hidden → livecfg 快照
//   - features.sanitize_blacklist_fingerprints / upstream.model_optimization → upstream.Client 的 atomic 开关
//   - pool.* → pool.SetBreaker/SetMaxInFlight/SetMaxRPM/SetSoftRateMax/SetWeights/SetCostExploreInterval/SetPreferExpiring/SetCreditFloor/SetRestrictionWave
//   - schedule.* → scheduler.Reconfigure/SetBalanceInterval/SetExpiringSoonWindow
//   - logging.*（日志留存的开关、保留天数、容量上限）→ logStores.apply
//
// 需重启（涉及监听地址、HTTP client 超时、auth_dir 等装配期依赖）：
//   - listen / auth_dir / state_file / upstream.* / upstash.* / session_sticky.*（TTL 类）
//
// 落盘用"先写 tmp 再 rename"原子替换，且优先保留磁盘上的原始 JSON 结构（只改
// 面板表单覆盖到的键），避免把用户手写的注释性字段/未知键洗掉——这里直接整体
// 序列化校验后的配置，未知键在 json.Unmarshal 时已丢失，故先合并原始 map。
func saveConfig(raw []byte, path string, live *livecfg.Holder, p *pool.Pool, up *upstream.Client, sch *scheduler.Scheduler, logs logStores) ([]string, error) {
	// 1) 解析原始 JSON 为 map（保留用户手写的未知键），再叠加面板提交的键。
	oldRaw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read current config: %w", err)
	}
	var cur, incoming map[string]any
	if err := json.Unmarshal(oldRaw, &cur); err != nil {
		cur = map[string]any{}
	}
	if err := json.Unmarshal(raw, &incoming); err != nil {
		return nil, fmt.Errorf("parse submitted config: %w", err)
	}
	merged := mergeConfigMaps(cur, incoming)

	// 2) 校验（与启动同一套 Default+normalize），失败直接返回、不落盘。
	newCfg, err := ParseConfig(mergedJSON(merged))
	if err != nil {
		return nil, err
	}

	// 3) 落盘（原子替换）。
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}
	if err := renameConfig(tmp, path); err != nil {
		// A single-file Docker bind mount cannot be renamed over its mount
		// target (Linux returns EBUSY / "device or resource busy"). Keep the
		// atomic path for regular files, but update the mounted file in place
		// for this specific deployment shape.
		if !errors.Is(err, syscall.EBUSY) {
			return nil, fmt.Errorf("replace config: %w", err)
		}
		f, openErr := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
		if openErr != nil {
			_ = os.Remove(tmp)
			return nil, fmt.Errorf("replace config (bind mount fallback): %w", openErr)
		}
		_, writeErr := configFileWrite(f, out)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		closeErr := configFileClose(f)
		// 写失败时保留 tmp（挂载文件已被 O_TRUNC 破坏，tmp 里是完整新内容，
		// 可手工恢复）；写成功才清理。
		if writeErr != nil {
			return nil, fmt.Errorf("replace config (bind mount fallback, 完整新内容保留在 %s): %w", tmp, writeErr)
		}
		_ = os.Remove(tmp)
		if closeErr != nil {
			return nil, fmt.Errorf("replace config (bind mount fallback): %w", closeErr)
		}
	}

	// 4) 热应用：能立即生效的字段全部应用，并列出仍需重启的字段。
	live.Store(livecfg.Snapshot{
		APIKey:               newCfg.APIKey,
		SoftCooldown:         newCfg.SoftRateDur,
		SanitizeFingerprints: newCfg.Features.SanitizeBlacklistFingerprints,
		RecordClientInfo:     newCfg.Logging.RequestClientInfo,
		CrossRealm:           string(newCfg.Pool.CrossRealm),
		UpdateCheck:          newCfg.Panel.UpdateCheck,
		ModelRoutes:          newCfg.ModelRoutes,
		HiddenModels:         livecfg.NormalizeHiddenModels(newCfg.Models.Hidden),
	})
	up.SanitizeFingerprints.Store(newCfg.Features.SanitizeBlacklistFingerprints)
	up.ModelOptimization.Store(newCfg.Upstream.ModelOptimization)
	upstream.SetTLSMimic(newCfg.Upstream.TLSFingerprint) // TLS 指纹伪装开关（atomic，新建连接起用）
	p.SetBreaker(newCfg.Pool.BreakerThreshold, newCfg.BreakerCooldownDur, newCfg.BreakerCooldownMaxD)
	p.SetMaxInFlight(newCfg.Pool.MaxInFlight)
	p.SetMaxInFlightGlobal(newCfg.Pool.MaxInFlightGlobal)
	p.SetInFlightWait(newCfg.InFlightWaitDur)
	p.SetMaxRPM(newCfg.Pool.MaxRPM)
	p.SetMaxRPMGlobal(newCfg.Pool.MaxRPMGlobal)
	p.SetDegrade(newCfg.Pool.DegradeThreshold, newCfg.DegradeCooldownDur, newCfg.DegradeCooldownMaxD)
	p.SetSoftRateMax(newCfg.SoftRateMaxDur)
	p.SetCostExploreInterval(newCfg.CostExploreIntervalDur) // costTier 探索窗口热生效（0 关停）
	p.SetCreditFloor(newCfg.Pool.CreditFloor)               // 积分保底热生效（0 = 关闭）
	p.SetRestrictionWave(newCfg.Pool.RestrictionWaveThreshold, newCfg.RestrictionWaveWindowDur, newCfg.RestrictionWavePauseDur)
	p.SetWeights(newCfg.Pool.IdleWeightPerHour, newCfg.Pool.IdleWeightMax)
	p.SetPreferExpiring(newCfg.Pool.PreferExpiring)
	sch.SetExpiringSoonWindow(newCfg.ExpiringSoonDur)
	sch.Reconfigure(
		newCfg.Schedule.CheckinHours, newCfg.Schedule.TravelHours,
		newCfg.Schedule.ActivityHours, newCfg.Schedule.KeepaliveHours, newCfg.Schedule.BlackcatHours,
		newCfg.Schedule.GrowthHours,
		!newCfg.Schedule.CheckinEnabled, !newCfg.Schedule.TravelEnabled,
		!newCfg.Schedule.ActivityEnabled, !newCfg.Schedule.KeepaliveEnabled, !newCfg.Schedule.BlackcatEnabled,
		!newCfg.Schedule.GrowthEnabled)
	sch.SetBalanceInterval(newCfg.BalanceRefreshInterval)
	sch.SetLivenessInterval(newCfg.LivenessInterval)
	sch.SetIncludeDisabledInTasks(newCfg.Schedule.IncludeDisabledInTasks)
	logs.apply(newCfg)

	return restartRequiredFields(newCfg), nil
}

// logStores 日志留存要热改的几样：请求记录归档、任务日志、系统日志、积分获取流水、用量统计。零值什么都不改。
type logStores struct {
	requests *reqlog.Recorder
	task     *logarchive.Store
	system   *logarchive.Store
	credit   *logarchive.Store
	usage    *usage.Recorder
}

// apply 把配置里的开关、保留天数、容量上限交给各自的归档，马上按新规则清理一次。
func (l logStores) apply(c *Config) {
	l.requests.SetArchive(c.Logging.RequestArchiveEnabled, c.Logging.RequestRetentionDays, int64(c.Logging.RequestArchiveMaxMB)<<20)
	l.task.Reconfigure(c.Logging.TaskLogEnabled, c.Logging.TaskLogRetentionDays, int64(c.Logging.TaskLogMaxMB)<<20)
	l.system.Reconfigure(c.Logging.SystemLogEnabled, c.Logging.SystemLogRetentionDays, int64(c.Logging.SystemLogMaxMB)<<20)
	l.credit.Reconfigure(c.Logging.CreditLogEnabled, c.Logging.CreditLogRetentionDays, int64(c.Logging.CreditLogMaxMB)<<20)
	l.usage.SetRetentionDays(c.Logging.UsageRetentionDays)
}

// retentionText 保留天数的说法：「保留 7 天」或「永久保留」。
func retentionText(days int) string {
	if days <= 0 {
		return "永久保留"
	}
	return fmt.Sprintf("保留 %d 天", days)
}

// runtimeLogText 一类运行日志的留存设置：「已关闭」或「保留 30 天、上限 20 MiB」。
func runtimeLogText(enabled bool, days, maxMB int) string {
	if !enabled {
		return "已关闭"
	}
	return fmt.Sprintf("%s、上限 %d MiB", retentionText(days), maxMB)
}

// restartRequiredFields 返回本次改动中无法热生效、需要重启进程的字段名。
// 恒返回完整清单中的"与当前进程装配期依赖相关"的项——面板据此提示用户。
func restartRequiredFields(c *Config) []string {
	var out []string
	// 这些字段在进程内被监听地址/HTTP client/目录句柄等装配期对象捕获。
	if c.Listen != "" {
		out = append(out, "listen")
	}
	if c.AuthDir != "" {
		out = append(out, "auth_dir")
	}
	if c.StateFile != "" {
		out = append(out, "state_file")
	}
	out = append(out, "upstream.timeout_seconds", "upstream.header_timeout_seconds", "upstream.idle_timeout_seconds")
	// upstream.user_agent 在装配期被写进出站 client（main.go 的 up.UserAgent = ...），
	// 之后不再读取——不在 livecfg 热快照里，也无法热改。此前漏列，导致面板改完
	// 显示"已保存"却不提示需要重启，用户以为没生效（issue #102 附带发现 2）。
	out = append(out, "upstream.user_agent")
	if c.Upstash.URL != "" || c.Upstash.Token != "" {
		out = append(out, "upstash")
	}
	if len(c.DNSServers) > 0 {
		out = append(out, "dns_servers")
	}
	out = append(out, "session_sticky.ttl", "session_sticky.gc_interval")
	out = append(out, "server.read_timeout")
	return out
}

// mergeConfigMaps 把 incoming 深合并进 cur（原地），返回 cur。
// 对嵌套对象逐键覆盖而不是整体替换：面板表单只提交它管理的键，
// 未提交的兄弟键（含用户手写的未知键）保持原样。
func mergeConfigMaps(cur, incoming map[string]any) map[string]any {
	for k, v := range incoming {
		if inMap, ok := v.(map[string]any); ok {
			if curMap, ok := cur[k].(map[string]any); ok {
				cur[k] = mergeConfigMaps(curMap, inMap)
				continue
			}
		}
		cur[k] = v
	}
	return cur
}

// mergedJSON 把合并后的 map 序列化回 JSON（供 ParseConfig 校验）。
func mergedJSON(m map[string]any) []byte {
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}
