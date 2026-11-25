#!/usr/bin/env bash
# 根据版本与 SHA256SUMS 生成 Homebrew Cask 定义。
set -euo pipefail

if [[ $# -ne 2 ]]; then
    echo '用法: scripts/homebrew_cask.sh <版本> <SHA256SUMS 路径>' >&2
    exit 1
fi

VERSION="$1"
SUMS_FILE="$2"

if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    echo "错误: 版本号格式无效: $VERSION" >&2
    exit 1
fi

if [[ ! -f "$SUMS_FILE" || ! -r "$SUMS_FILE" ]]; then
    echo "错误: SHA256SUMS 文件不可读: $SUMS_FILE" >&2
    exit 1
fi

ARM64_FILE="mcode-${VERSION}-arm64.dmg"
AMD64_FILE="mcode-${VERSION}-amd64.dmg"
ARM64_SHA=""
AMD64_SHA=""

while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    [[ -z "$line" ]] && continue
    if [[ "$line" =~ ^([^[:space:]]+)[[:space:]]+(.+)$ ]]; then
        hash="${BASH_REMATCH[1]}"
        file="${BASH_REMATCH[2]}"
        if [[ "$file" == "$ARM64_FILE" ]]; then
            if [[ -n "$ARM64_SHA" ]]; then
                echo "错误: SHA256SUMS 中存在重复的 $ARM64_FILE" >&2
                exit 1
            fi
            ARM64_SHA="$hash"
        elif [[ "$file" == "$AMD64_FILE" ]]; then
            if [[ -n "$AMD64_SHA" ]]; then
                echo "错误: SHA256SUMS 中存在重复的 $AMD64_FILE" >&2
                exit 1
            fi
            AMD64_SHA="$hash"
        fi
    fi
done < "$SUMS_FILE"

if [[ -z "$ARM64_SHA" ]]; then
    echo "错误: SHA256SUMS 中缺少 $ARM64_FILE" >&2
    exit 1
fi

if [[ -z "$AMD64_SHA" ]]; then
    echo "错误: SHA256SUMS 中缺少 $AMD64_FILE" >&2
    exit 1
fi

if [[ ! "$ARM64_SHA" =~ ^[0-9a-fA-F]{64}$ ]]; then
    echo "错误: $ARM64_FILE 的哈希不是有效的 64 位十六进制" >&2
    exit 1
fi

if [[ ! "$AMD64_SHA" =~ ^[0-9a-fA-F]{64}$ ]]; then
    echo "错误: $AMD64_FILE 的哈希不是有效的 64 位十六进制" >&2
    exit 1
fi

printf 'cask "mcode" do
  arch arm: "arm64", intel: "amd64"

  version "%s"
  sha256 arm:   "%s",
         intel: "%s"

  url "https://github.com/miniers/MCode/releases/download/v#{version}/mcode-#{version}-#{arch}.dmg"
  name "MCode"
  desc "Native desktop AI coding workbench"
  homepage "https://github.com/miniers/MCode"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: :sonoma

  app "MCode.app"
end\n' "$VERSION" "$ARM64_SHA" "$AMD64_SHA"
