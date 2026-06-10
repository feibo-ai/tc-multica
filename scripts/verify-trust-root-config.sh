#!/usr/bin/env bash
# verify-trust-root-config.sh — mini-ADR v4 invariant #10 / §1.5
#
# 断言信任根的 GitHub 服务端配置存在且未被削弱(把散文/口头缓解变可验证门)。
# 防「实现期遗漏 / 事后被人削弱 ruleset」=「安全感而非安全性」。
# CI / 运维定期跑;任一断言失败即 exit 1。需 gh CLI + 对该 repo 的 read 权。
#
# 用法:  scripts/verify-trust-root-config.sh [owner/repo]
# 默认:  feibo-ai/tc-multica
set -euo pipefail

REPO="${1:-feibo-ai/tc-multica}"
fail=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad() { printf '  \033[31m✗\033[0m %s\n' "$*"; fail=1; }
note(){ printf '    %s\n' "$*"; }

echo "== 信任根配置断言: $REPO (mini-ADR v4 invariant #10) =="

rulesets="$(gh api "repos/$REPO/rulesets" 2>/dev/null || echo '[]')"

# ── main 分支 ruleset:active · ≥2 评审 · dismiss-stale · 无 bypass · 禁 force-push ──
mainid="$(echo "$rulesets" | jq -r '.[]|select(.target=="branch" and .enforcement=="active")|.id' | head -1)"
if [ -z "$mainid" ] || [ "$mainid" = "null" ]; then
  bad "无 active branch ruleset(main 未受保护 —— 直推/未评审 commit 可进 main,签名信任根失效)"
else
  d="$(gh api "repos/$REPO/rulesets/$mainid")"
  reviews="$(echo "$d" | jq -r '[.rules[]|select(.type=="pull_request")|.parameters.required_approving_review_count]|first // 0')"
  bypass="$(echo "$d" | jq '.bypass_actors | length')"
  [ "${reviews:-0}" -ge 2 ] && ok "main 需 ≥2 评审 (=$reviews)" || bad "main 评审数 <2 (=$reviews) —— invariant #7 要求 ≥2"
  [ "$bypass" -eq 0 ] && ok "main bypass 为空(admin 不绕)" || bad "main 有 $bypass 个 bypass actor(应为空,否则可绕评审)"
  echo "$d" | jq -e '.rules[]|select(.type=="pull_request")|select(.parameters.dismiss_stale_reviews_on_push==true)' >/dev/null \
    && ok "main dismiss-stale-reviews 开(force-push 后旧 approval 失效)" || bad "main 未开 dismiss-stale(陈旧 approval 可被复用,RB-8)"
  echo "$d" | jq -e '.rules[]|select(.type=="non_fast_forward")' >/dev/null \
    && ok "main 禁 force-push(non-fast-forward)" || bad "main 可 force-push(历史可改写)"
  echo "$d" | jq -e '.rules[]|select(.type=="deletion")' >/dev/null \
    && ok "main 禁删除" || bad "main 可被删除"
fi

# ── v* tag ruleset:active · 禁删除 · 禁改写 ──
tagid="$(echo "$rulesets" | jq -r '.[]|select(.target=="tag" and .enforcement=="active")|.id' | head -1)"
if [ -z "$tagid" ] || [ "$tagid" = "null" ]; then
  bad "无 active tag ruleset(release tag 可被删除/改写)"
else
  d="$(gh api "repos/$REPO/rulesets/$tagid")"
  echo "$d" | jq -e '.rules[]|select(.type=="deletion")' >/dev/null \
    && ok "v* tag 禁删除(release tag 不可移除)" || bad "v* tag 可删除"
  echo "$d" | jq -e '.rules[]|select(.type=="non_fast_forward")' >/dev/null \
    && ok "v* tag 禁改写(发版指向不可篡改)" || bad "v* tag 可改写"
fi

# ── CODEOWNERS:保护信任根制造者(.github/release.yml/update.go/install.sh)──
if gh api "repos/$REPO/contents/.github/CODEOWNERS" >/dev/null 2>&1; then
  ok ".github/CODEOWNERS 存在"
  # 占位 handle 检测:若仍含模板占位,提醒未激活
  body="$(gh api "repos/$REPO/contents/.github/CODEOWNERS" --jq '.content' 2>/dev/null | base64 -d 2>/dev/null || echo '')"
  if echo "$body" | grep -q '@feibo-ai/security-owners'; then
    note "提醒:CODEOWNERS 仍是模板占位 @feibo-ai/security-owners —— DRI 须填真实安全 owner"
    note "并在 main ruleset 开 require_code_owner_review,否则 gate-maker 保护未生效"
  fi
else
  bad ".github/CODEOWNERS 缺失(改 release.yml/update.go 无 owner 评审,可绕签名门)"
fi

echo ""
if [ "$fail" -ne 0 ]; then
  echo "== FAIL:信任根配置有缺口(见上 ✗)—— mini-ADR v4 §1.5 未满足 =="
  exit 1
fi
echo "== OK:信任根配置断言全过 =="
