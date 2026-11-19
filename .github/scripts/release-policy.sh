#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
# SPDX-License-Identifier: AGPL-3.0-only
# 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

# 镜像、版本 Release 和滚动下载每次执行前都从远端重新判断，包括只重跑失败任务时。
set -euo pipefail
version=$1
versioned=false
rolling=false
prerelease=false
if [[ "$version" == *-* ]]; then prerelease=true; fi

if [[ "$GITHUB_EVENT_NAME" == push && "$GITHUB_REF" == refs/heads/main ]]; then
  git fetch --quiet --force --prune origin '+refs/heads/main:refs/remotes/origin/main' '+refs/tags/*:refs/tags/*'
  # 强推后已脱离 main 的提交只构建。已由其他提交占用的版本标签保持原指向。
  if git merge-base --is-ancestor "$GITHUB_SHA" refs/remotes/origin/main; then
    tagged=$(git rev-parse -q --verify "refs/tags/v${version}^{commit}" || true)
    if [[ -z "$tagged" || "$tagged" == "$GITHUB_SHA" ]]; then versioned=true; fi
    if [[ "$prerelease" == false ]]; then
      latest=$(git rev-parse -q --verify 'refs/tags/latest^{commit}' || true)
      tip=$(git rev-parse refs/remotes/origin/main)
      # 排队期间 main 可以先进入 RC；正常正式构建仍可推进滚动版本。
      # 旧提交的重跑只补版本产物，已经发布过的较新滚动版本也不能倒退。
      if [[ "$GITHUB_SHA" == "$tip" ]] || {
        [[ "${GITHUB_RUN_ATTEMPT:-1}" == 1 ]] &&
          { [[ -z "$latest" ]] || git merge-base --is-ancestor "$latest" "$GITHUB_SHA"; }
      }; then rolling=true; fi
    fi
  fi
fi
printf 'versioned=%s\nrolling=%s\nprerelease=%s\n' "$versioned" "$rolling" "$prerelease" >> "$GITHUB_OUTPUT"
