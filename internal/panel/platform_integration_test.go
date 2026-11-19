// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package panel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yreidev/workbuddy2api-panel/internal/providers"
	"github.com/yreidev/workbuddy2api-panel/internal/usage"
)

func TestPanelModelsWithOnlyProvidersAndPartialFailure(t *testing.T) {
	requests := 0
	manager, err := providers.Open("", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "center.qoder.sh" && r.URL.Path == "/algo/api/v3/service/region/endpoints" {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"inferNodes":["https://api2.qoder.sh"]}`))}, nil
		}
		requests++
		if r.URL.Path != "/algo/api/v2/model/list" {
			t.Fatal("模型页不应触发推理", r.URL.Path)
		}
		if r.URL.Host == "api2.qoder.sh" {
			return nil, fmt.Errorf("fixture catalog unavailable")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"body":[{"key":"same","display_name":"测试模型","max_input_tokens":1234},{"key":"same"}]}`))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"qoder:cn", "qoder:global"} {
		if _, err := manager.Import(context.Background(), providers.AccountInput{Target: target, Credential: providers.Credential{UID: "fixture", AccessToken: "fixture-token", UserType: "teams"}}); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := newPanelFor(t, Config{Providers: manager})
	for i := 0; i < 2; i++ {
		rec := get(p, http.MethodGet, "/panel/api/models")
		autoWantCode(t, rec, 200)
		data := autoDecode(t, rec)
		models := data["models"].([]any)
		if len(models) != 1 {
			t.Fatal("未去重或目录丢失", models)
		}
		entry := models[0].(map[string]any)
		if entry["id"] != "qoder:cn:same" || entry["context_length"] != float64(1234) || entry["credits"] != nil || len(data["warnings"].([]any)) != 1 {
			t.Fatal("多平台目录或未知价格错误", data)
		}
	}
	if requests != 4 {
		t.Fatal("重新获取没有强制刷新", requests)
	}
}

func TestPlatformBatchExportAuthAndTimeUsage(t *testing.T) {
	manager, _ := providers.Open("", nil)
	id, err := manager.Import(context.Background(), providers.AccountInput{Target: "qoder:cn", Name: "测试账号", Credential: providers.Credential{UID: "fixture", AccessToken: "secret-fixture", UserType: "teams"}})
	if err != nil {
		t.Fatal(err)
	}
	recorder := usage.New("")
	for _, target := range []string{"qoder:cn", "qoder:global", "cn"} {
		recorder.Add(time.Now(), target, id, "model", usage.Delta{TokensUnknown: true, Key: "client"}, true)
	}
	p, _ := newPanelFor(t, Config{Providers: manager, Usage: recorder})
	body := fmt.Sprintf(`{"ids":[%q,"missing",%q],"action":"pause"}`, id, id)
	rec := postJSON(t, p, "/panel/api/providers/batch", body)
	autoWantCode(t, rec, 200)
	data := autoDecode(t, rec)
	if data["ok"] != float64(1) || len(data["failed"].([]any)) != 1 || !manager.List()[0].Paused {
		t.Fatal("批量失败未逐号报告", data)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/providers/batch", fmt.Sprintf(`{"ids":[%q],"action":"join_group","group":"missing"}`, id)), 400)
	called := 0
	p.SetProviderLiveness(func(context.Context, string, string) (providers.LivenessResponse, error) {
		called++
		return providers.LivenessResponse{Result: providers.LivenessResult{OK: true}}, nil
	})
	rec = postJSON(t, p, "/panel/api/providers/batch", fmt.Sprintf(`{"ids":[%q],"action":"liveness"}`, id))
	if called != 0 || autoDecode(t, rec)["ok"] != float64(0) {
		t.Fatal("没有明确模型时误测")
	}
	if err := manager.Update(id, providers.AccountInput{Name: "测试账号", LivenessModel: "qoder:cn:model"}, false); err != nil {
		t.Fatal(err)
	}
	autoWantCode(t, postJSON(t, p, "/panel/api/providers/batch", fmt.Sprintf(`{"ids":[%q],"action":"liveness"}`, id)), 200)
	if called != 1 {
		t.Fatal("批量测活未接入")
	}
	rec = postJSON(t, p, "/panel/api/providers/export", fmt.Sprintf(`{"ids":[%q]}`, id))
	autoWantCode(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "secret-fixture") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("导出内容或缓存策略错误")
	}
	for _, target := range []string{"qoder:cn", "workbuddy:cn"} {
		rec = get(p, http.MethodGet, "/panel/api/usage?hours=0&target="+target+"&key=client")
		autoWantCode(t, rec, 200)
		if autoDecode(t, rec)["totals"].(map[string]any)["requests"] != float64(1) {
			t.Fatal("用量未按上游过滤")
		}
	}
	autoWantCode(t, get(p, http.MethodGet, "/panel/api/usage?target=unknown"), 400)
	p.cfg.APIKey = "admin-fixture"
	for _, path := range []string{"batch", "export"} {
		autoWantCode(t, postJSON(t, p, "/panel/api/providers/"+path, body), 401)
	}
}
