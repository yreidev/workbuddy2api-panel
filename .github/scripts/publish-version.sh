#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 yreidev 与贡献者（见 NOTICE）
# SPDX-License-Identifier: AGPL-3.0-only
# 复制或改写本文件须保留以上声明，并按 NOTICE 的署名条款注明出处：https://github.com/yreidev/workbuddy2api-panel

# 输入为构建任务生成的 dist/ 和 notes.md。由 main 发布队列调用。
set -euo pipefail
tag="v$1"
flags=(--target "$GITHUB_SHA" --title "$tag" --notes-file notes.md --prerelease="$2" --latest="$3")
owner=${GITHUB_REPOSITORY%/*}
repo=${GITHUB_REPOSITORY#*/}
# GraphQL 可查到上传中断后尚未打标签的草稿。null 表示不存在，查询失败则中止。
release=$(gh api graphql -F owner="$owner" -F repo="$repo" -F tag="$tag" -f query='
  query($owner: String!, $repo: String!, $tag: String!) {
    repository(owner: $owner, name: $repo) {
      release(tagName: $tag) { isDraft }
    }
  }' --jq '.data.repository.release | if . == null then "missing" elif .isDraft then "draft" else "published" end')

case "$release" in
  missing)
    gh release create "$tag" dist/* "${flags[@]}"
    ;;
  draft)
    target=$(gh release view "$tag" --json targetCommitish --jq .targetCommitish)
    if [[ "$target" != "$GITHUB_SHA" ]]; then
      echo "::error::$tag 的草稿指向 $target，请复核对应的发布提交"
      exit 1
    fi
    gh release upload "$tag" dist/* --clobber
    gh release edit "$tag" "${flags[@]}" --draft=false
    ;;
  published)
    # 完整的编号包已经交付。缺少文件时整组补传，避免 checksums 与包来自两次不同的构建。
    assets=$(gh release view "$tag" --json assets --jq '.assets[] | select(.state == "uploaded" and .size > 0) | .name')
    for path in dist/*; do
      if ! grep -Fxq "${path##*/}" <<< "$assets"; then
        gh release upload "$tag" dist/* --clobber
        break
      fi
    done
    ;;
  *)
    echo "::error::查询 $tag 得到未知状态 $release"
    exit 1
    ;;
esac
