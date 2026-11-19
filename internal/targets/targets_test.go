// SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
// SPDX-License-Identifier: AGPL-3.0-only
// 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

package targets

import "testing"

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		model, target, native string
		explicit              bool
	}{
		{"cn:model", "workbuddy:cn", "model", true}, {"global:model", "workbuddy:global", "model", true},
		{"traework:cn:model", "traework:cn", "model", true}, {"traecode:cn:model", "traecode:cn", "model", true},
		{"qoder:global:vendor:model", "qoder:global", "vendor:model", true}, {"native:model", "", "native:model", false},
	} {
		target, native, explicit := Parse(tc.model)
		if target != tc.target || native != tc.native || explicit != tc.explicit {
			t.Fatalf("%s: %s %s %v", tc.model, target, native, explicit)
		}
	}
}
