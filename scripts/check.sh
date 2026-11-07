#!/usr/bin/env bash
set -euo pipefail

# 统一检查入口：格式、构建、go vet、依赖规则、单测，可选 race。
# 兼容 macOS 自带 bash 3.2（不用 mapfile / readarray / 关联数组）。
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

RACE=0
for arg in "$@"; do
    case "$arg" in
    --race)
        RACE=1
        ;;
    *)
        echo "用法: $0 [--race]" >&2
        exit 2
        ;;
    esac
done

echo "==> 收集待检查包"
# build.noindex 是被 git 忽略的打包产物目录，应用包的 Contents/Resources 里带 .go 文件，
# ./... 会把它们扫进来，这里过滤掉；发布打包产物目录 dist/ 同样被 git 忽略、里面也带 .go 文件，
# 所以一并排除。
pkgs=()
while IFS= read -r pkg; do
    pkgs+=("$pkg")
done < <(go list ./... | grep -vE '^mcode/(build\.noindex|dist)/' || true)
if [[ ${#pkgs[@]} -eq 0 ]]; then
    echo "没有可检查的包" >&2
    exit 1
fi

# build 用单独的包列表：像 mcode/scripts 这样只有测试文件的包，显式交给 go build
# 会报 no non-test Go files 并失败，所以只挑出含非测试 Go 文件的包；vet 和 test 仍用 pkgs。
build_pkgs=()
while IFS= read -r pkg; do
    if [[ -z "$pkg" ]]; then
        continue
    fi
    build_pkgs+=("$pkg")
done < <(CGO_ENABLED=0 go list -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./... | grep -vE '^mcode/(build\.noindex|dist)/' || true)
if [[ ${#build_pkgs[@]} -eq 0 ]]; then
    echo "没有可构建的包" >&2
    exit 1
fi

echo "==> gofmt 检查"
unformatted="$(git ls-files -z '*.go' | xargs -0 gofmt -l)"
if [[ -n "$unformatted" ]]; then
    echo "以下文件未格式化，请运行 gofmt -w:" >&2
    echo "$unformatted" >&2
    exit 1
fi

echo "==> go build"
CGO_ENABLED=0 go build "${build_pkgs[@]}"

echo "==> go vet"
CGO_ENABLED=0 go vet "${pkgs[@]}"

echo "==> 依赖规则检查"
bash scripts/check-deps.sh

echo "==> go test"
CGO_ENABLED=0 go test -timeout 120s "${pkgs[@]}"

if [[ $RACE -eq 1 ]]; then
    echo "==> go test -race"
    # race 检测需要 cgo，这里解除外层可能设置的 CGO_ENABLED=0。
    env -u CGO_ENABLED go test -race -timeout 300s "${pkgs[@]}"
fi

echo "==> 前端类型检查"
if [[ -d web/node_modules ]]; then
    (cd web && npx tsc --noEmit)
else
    echo "web/node_modules 不存在，跳过前端类型检查"
fi

echo "全部检查通过"
