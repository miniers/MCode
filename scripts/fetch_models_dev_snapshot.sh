#!/usr/bin/env bash
# 刷新随应用内置的 models.dev 能力快照（离线基线数据）。
#
#   scripts/fetch_models_dev_snapshot.sh
#
# 写入 internal/modelsdev/snapshot.json，由 go:embed 嵌入二进制。
# 只保留 mcode 支持的 provider（需与 internal/modelsdev.Providers 一致）与能力字段，
# 含价格（cost）与推理/输入模态等能力，体积约 30 KB。
# 断网时保留已有快照；仅在没有任何有效快照时才失败。
set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEST="$REPO_ROOT/internal/modelsdev/snapshot.json"
URL="${MODELS_DEV_URL:-https://models.dev/api.json}"
IFS=' ' read -r -a PROVIDERS <<<"${MODELS_DEV_PROVIDERS:-anthropic openai google cline-pass}"
TMP="$(mktemp "${TMPDIR:-/tmp}/models-dev-XXXXXX.json")"
OUT="$(mktemp "${TMPDIR:-/tmp}/models-dev-trim-XXXXXX.json")"
trap 'rm -f "$TMP" "$OUT"' EXIT

warn() { echo "fetch_models_dev_snapshot: $1" >&2; }

# 占位快照（"{}"）不算有效快照。
have_snapshot() { [ -f "$DEST" ] && [ "$(wc -c <"$DEST")" -gt 10 ]; }

keep_or_fail() {
    if have_snapshot; then
        warn "$1；保留现有快照 ${DEST#"$REPO_ROOT"/}"
        exit 0
    fi
    warn "$1，且尚无有效快照"
    exit 1
}

# 打印实际使用的地址：MODELS_DEV_URL 可指向镜像，出问题时要能看出取的是哪一份。
echo "fetch_models_dev_snapshot: 下载 $URL"

curl -fsSL --compressed --retry 2 --connect-timeout 15 --max-time 180 "$URL" -o "$TMP" \
    || keep_or_fail "无法下载 $URL"

# 先裁剪并校验再替换：被截断或结构异常的下载不能作为数据发布。
python3 - "$TMP" "$OUT" "${PROVIDERS[@]}" <<'PY' || keep_or_fail "下载内容不像 models.dev 目录"
import json
import sys

src, dst, *providers = sys.argv[1:]
with open(src, "rb") as handle:
    root = json.load(handle)
root = root.get("providers", root) if isinstance(root, dict) else {}
assert isinstance(root, dict) and len(root) > 50, "unexpected models.dev payload"

out = {}
for name in providers:
    models = {}
    for key, m in ((root.get(name) or {}).get("models") or {}).items():
        entry = {
            "id": m.get("id"),
            "name": m.get("name"),
            "family": m.get("family"),
            "status": m.get("status"),
            "release_date": m.get("release_date"),
            "canonical_model_id": m.get("canonical_model_id"),
            "limit": {"context": (m.get("limit") or {}).get("context")},
            "reasoning_options": [
                {"type": o.get("type"), **({"values": o["values"]} if o.get("values") else {})}
                for o in (m.get("reasoning_options") or [])
                if o.get("type") in ("effort", "toggle")
            ],
            "experimental": {"modes": {k: {} for k in ((m.get("experimental") or {}).get("modes") or {})}},
            "cost": {
                k: (m.get("cost") or {}).get(k)
                for k in ("input", "output", "cache_read", "cache_write")
                if (m.get("cost") or {}).get(k) is not None
            },
            "reasoning": m.get("reasoning") or None,
            "attachment": m.get("attachment") or None,
            "knowledge": m.get("knowledge"),
            "modalities": {
                k: (m.get("modalities") or {}).get(k)
                for k in ("input", "output")
                if (m.get("modalities") or {}).get(k)
            },
        }
        entry = {k: v for k, v in entry.items() if v not in (None, "", [], {})}
        if not (entry.get("limit") or {}).get("context"):
            entry.pop("limit", None)
        if not (entry.get("experimental") or {}).get("modes"):
            entry.pop("experimental", None)
        if entry.get("id"):
            models[key] = entry
    assert models, f"provider {name} missing from models.dev payload"
    out[name] = {"models": models}

with open(dst, "w") as handle:
    json.dump(out, handle, separators=(",", ":"), sort_keys=True)
PY

mv "$OUT" "$DEST"
echo "fetch_models_dev_snapshot: wrote $(du -h "$DEST" | cut -f1) to ${DEST#"$REPO_ROOT"/}"
