# GoReleaser + GitHub Actions CI/CD（Docker Hub 多架構）

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-18
**狀態**: 🟢 完成
**優先級**: Medium

## 背景與目標

目前 xero-mirror 已有單架構 Dockerfile（linux/amd64 only），需要：

1. **自動化建構與推送**：push tag 時由 GitHub Actions 自動 build & push to Docker Hub
2. **多架構支援**：同時支援 `linux/amd64` 與 `linux/arm64`
3. **GoReleaser 方案**：使用 GoReleaser 統一管理 cross-compile + Docker multi-arch + GitHub Release
4. **版本標籤策略**：semver tag（`v1.2.3`）觸發 release，`latest` 指向最新 stable

### 非目標
- 不做 PR 建構預覽（可後續擴充）
- 不做 GitHub Container Registry（只用 Docker Hub）

## 架構概覽

```
git push tag v1.2.3
        │
        ▼
GitHub Actions (.github/workflows/release.yml)
        │
        └─ 單一 job: release
            │
            ├─ checkout + setup Go
            ├─ setup QEMU（只給 Docker arm64 用）
            ├─ setup Docker Buildx
            ├─ docker login (Docker Hub)
            └─ goreleaser release --clean
                    │
                    ├─ cross-compile: linux/amd64, linux/arm64
                    ├─ docker buildx build --platform linux/amd64
                    ├─ docker buildx build --platform linux/arm64
                    ├─ push 各平台 image
                    ├─ docker manifest create + push（multi-arch）
                    └─ 產生 GitHub Release + checksums
```

**QEMU 用途**：只給 Docker build arm64 image 用（跑 `apk add` + `COPY`，開銷極小）。Go 編譯是原生 cross-compile，不走 QEMU。

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 建立 `.goreleaser.yaml` 設定檔 | ✅ 完成 | general | GoReleaser 完整接管 Docker + manifest |
| 2 | 新增 `Dockerfile.goreleaser` | ✅ 完成 | general | 精簡 runtime image |
| 3 | 建立 `.github/workflows/release.yml` | ✅ 完成 | general | 單一 job，35 行 |
| 4 | 更新 `.dockerignore` | ⏭️ 跳過 | — | 現有設定已適用 |
| 5 | 本機驗證 | ✅ 完成 | — | 已推送 tag 觸發 CI |

## 詳細設計

### 1. `.goreleaser.yaml`

GoReleaser 完整接管：cross-compile + Docker multi-arch + manifest + GitHub Release。

```yaml
# GoReleaser 設定 for xero-mirror
# 完整方案：cross-compile + Docker multi-arch + GitHub Release

version: 2

before:
  hooks:
    - go mod tidy

builds:
  - id: xero-mirror
    binary: xero-mirror
    env:
      - CGO_ENABLED=0
    goos:
      - linux
    goarch:
      - amd64
      - arm64
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.Commit}}
      - -X main.date={{.Date}}

archives:
  - formats: [tar.gz]
    name_template: "{{ .ProjectName }}_{{ .Tag }}_{{ .Os }}_{{ .Arch }}"

dockers:
  - id: amd64
    dockerfile: Dockerfile.goreleaser
    goos: linux
    goarch: amd64
    use: buildx
    image_templates:
      - "mmhk/xero-mirror:{{ .Tag }}-amd64"
      - "mmhk/xero-mirror:latest-amd64"
    build_flag_templates:
      - "--platform=linux/amd64"

  - id: arm64
    dockerfile: Dockerfile.goreleaser
    goos: linux
    goarch: arm64
    use: buildx
    image_templates:
      - "mmhk/xero-mirror:{{ .Tag }}-arm64"
      - "mmhk/xero-mirror:latest-arm64"
    build_flag_templates:
      - "--platform=linux/arm64"

docker_manifests:
  - name_template: "mmhk/xero-mirror:{{ .Tag }}"
    image_templates:
      - "mmhk/xero-mirror:{{ .Tag }}-amd64"
      - "mmhk/xero-mirror:{{ .Tag }}-arm64"
  - name_template: "mmhk/xero-mirror:latest"
    image_templates:
      - "mmhk/xero-mirror:latest-amd64"
      - "mmhk/xero-mirror:latest-arm64"

checksum:
  name_template: "checksums.txt"

release:
  disable: false
```

### 2. `Dockerfile.goreleaser`

精簡 runtime image，binary 由 GoReleaser cross-compile 完成後放進 build context。

```dockerfile
# Dockerfile.goreleaser — CI 建構用，binary 由 GoReleaser 預先編譯
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY xero-mirror .

EXPOSE 8080

ENTRYPOINT ["/app/xero-mirror"]
CMD ["serve"]
```

保留原 `Dockerfile` 供本機 `docker build` 使用（含 Go build stage）。

### 3. `.github/workflows/release.yml`

單一 job，GoReleaser 一次處理所有步驟。

```yaml
name: Release

on:
  push:
    tags:
      - "v*"

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"

      - uses: docker/setup-qemu-action@v3
        with:
          platforms: arm64

      - uses: docker/setup-buildx-action@v3

      - uses: docker/login-action@v3
        with:
          username: ${{ secrets.DOCKERHUB_USERNAME }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}

      - uses: goreleaser/goreleaser-action@v6
        with:
          distribution: goreleaser
          version: latest
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

### 4. 需要的 GitHub Secrets

| Secret | 說明 |
|--------|------|
| `DOCKERHUB_USERNAME` | Docker Hub 帳號（mmhk） |
| `DOCKERHUB_TOKEN` | Docker Hub Access Token |

### 5. 觸發方式與產出

- `git tag v1.0.0 && git push origin v1.0.0` → 自動觸發
- 建構產出：
  - `mmhk/xero-mirror:v1.0.0`（multi-arch manifest）
  - `mmhk/xero-mirror:v1.0.0-amd64`
  - `mmhk/xero-mirror:v1.0.0-arm64`
  - `mmhk/xero-mirror:latest`（multi-arch manifest）
  - GitHub Release（含 checksums.txt + tar.gz）

## 人類 Review 記錄

| 日期 | 動作 | 備註 |
|------|------|------|
| 2026-11-18 | ✅ 已批准 | 開始實作 |
| 2026-11-18 | 🟢 完成 | 已推送 v0.1.0 觸發 CI |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|

## 備註

- GoReleaser 免費版支援多架構 Docker，不需要 Pro 授權
- `dist/` 已在 `.gitignore` 內（GoReleaser 預設輸出目錄）
- 本機測試：`goreleaser release --snapshot --clean` 可模擬完整流程
- QEMU 只給 Docker build arm64 用，Go 編譯是原生 cross-compile
- 若未來需要 PR 預覽建構，可加 `.github/workflows/docker-pr.yml`
