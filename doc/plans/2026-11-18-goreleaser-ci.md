# GoReleaser + GitHub Actions CI/CD（Docker Hub 多架構）

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。所有內容必須經過**人類 Review + 確認**後才能進入實施階段。未經確認前，不得開始任何開發工作。

**建立日期**: 2026-11-18
**狀態**: 🟡 等待 Review
**優先級**: Medium

## 背景與目標

目前 xero-mirror 已有單架構 Dockerfile（linux/amd64 only），需要：

1. **自動化建構與推送**：push tag 時由 GitHub Actions 自動 build & push to Docker Hub
2. **多架構支援**：同時支援 `linux/amd64` 與 `linux/arm64`
3. **GoReleaser 方案**：使用 GoReleaser 統一管理 cross-compile + GitHub Release
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
        ├─ Job 1: build (matrix)
        │   ├─ ubuntu-latest (amd64)
        │   │   └─ GoReleaser cross-compile linux/amd64 binary
        │   │   └─ docker build --platform linux/amd64
        │   │   └─ docker push mmhk/xero-mirror:v1.2.3-amd64
        │   │
        │   └─ ubuntu-24.04-arm (arm64)
        │       └─ GoReleaser cross-compile linux/arm64 binary
        │       └─ docker build --platform linux/arm64
        │       └─ docker push mmhk/xero-mirror:v1.2.3-arm64
        │
        ├─ Job 2: manifest (needs: build)
        │   └─ docker manifest create mmhk/xero-mirror:v1.2.3
        │   └─ docker manifest create mmhk/xero-mirror:latest
        │   └─ docker manifest push
        │
        └─ Job 3: github-release (needs: build)
            └─ GoReleaser 產生 GitHub Release + checksums
```

**不使用 QEMU**：
- Go 原生 cross-compile 產出多架構 binary
- GitHub Actions 原生 ARM64 runner（`ubuntu-24.04-arm`）建構 arm64 image
- 各架構在原生環境 build，效能最佳、無模擬開銷

## 任務列表

| # | 任務 | 狀態 | Sub Agent | 備註 |
|---|------|------|-----------|------|
| 1 | 建立 `.goreleaser.yaml` 設定檔（只管 cross-compile + release） | ✅ 完成 | general | 不含 docker，docker 由 workflow 處理 |
| 2 | 新增 `Dockerfile.goreleaser`（精簡 runtime image） | ✅ 完成 | general | binary 由 GoReleaser 預先編譯好放進 context |
| 3 | 建立 `.github/workflows/release.yml` | ✅ 完成 | general | matrix + 原生 runner + manifest merge |
| 4 | 更新 `.dockerignore`（配合 Goreleaser Dockerfile） | ⏭️ 跳過 | — | 現有 .dockerignore 已適用，不需修改 |
| 5 | 本機驗證 `goreleaser check` + `goreleaser release --snapshot` | ⬜ 待處理 | — | 需本機執行，待使用者驗證 |

## 詳細設計

### 1. `.goreleaser.yaml`

GoReleaser **只負責 cross-compile + GitHub Release**，Docker 建構由 GitHub Actions 原生處理（因為用原生 runner 不需要 QEMU）。

```yaml
# GoReleaser 設定 for xero-mirror
# GoReleaser 只管 cross-compile + release，Docker 由 CI workflow 處理

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

checksum:
  name_template: "checksums.txt"

changelog:
  sort: asc
  filters:
    exclude:
      - "^docs:"
      - "^test:"
      - "^chore:"
      - "merge conflict"
      - Merge pull request
      - Merge branch

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

```yaml
name: Release

on:
  push:
    tags:
      - "v*"

permissions:
  contents: write

env:
  IMAGE_NAME: mmhk/xero-mirror

jobs:
  # ── Job 1: 各架構原生建構 ──
  build:
    strategy:
      matrix:
        include:
          - runner: ubuntu-latest
            goos: linux
            goarch: amd64
            platform: linux/amd64
          - runner: ubuntu-24.04-arm
            goos: linux
            goarch: arm64
            platform: linux/arm64
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"

      - name: GoReleaser cross-compile
        uses: goreleaser/goreleaser-action@v6
        with:
          distribution: goreleaser
          version: latest
          args: build --single-target --clean --snapshot
        env:
          GOOS: ${{ matrix.goos }}
          GOARCH: ${{ matrix.goarch }}

      - name: Prepare Docker context
        run: |
          mkdir -p .docker-context
          cp dist/xero-mirror_${{ matrix.goos }}_${{ matrix.goarch }}*/xero-mirror .docker-context/xero-mirror
          cp Dockerfile.goreleaser .docker-context/Dockerfile

      - uses: docker/login-action@v3
        with:
          username: ${{ secrets.DOCKERHUB_USERNAME }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}

      - name: Build & push platform image
        run: |
          TAG="${GITHUB_REF_NAME}-${{ matrix.goarch }}"
          docker build --platform ${{ matrix.platform }} -t ${IMAGE_NAME}:${TAG} .docker-context/
          docker push ${IMAGE_NAME}:${TAG}

  # ── Job 2: 合併 multi-arch manifest ──
  manifest:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: docker/login-action@v3
        with:
          username: ${{ secrets.DOCKERHUB_USERNAME }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}

      - name: Create & push manifests
        run: |
          TAG="${GITHUB_REF_NAME}"
          # 版本 manifest
          docker manifest create ${IMAGE_NAME}:${TAG} \
            --amend ${IMAGE_NAME}:${TAG}-amd64 \
            --amend ${IMAGE_NAME}:${TAG}-arm64
          docker manifest push ${IMAGE_NAME}:${TAG}
          # latest manifest
          docker manifest create ${IMAGE_NAME}:latest \
            --amend ${IMAGE_NAME}:${TAG}-amd64 \
            --amend ${IMAGE_NAME}:${TAG}-arm64
          docker manifest push ${IMAGE_NAME}:latest

  # ── Job 3: GitHub Release（checksums + changelog）──
  github-release:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - uses: actions/setup-go@v5
        with:
          go-version: "1.24"

      - name: GoReleaser release
        uses: goreleaser/goreleaser-action@v6
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
| `DOCKERHUB_TOKEN` | Docker Hub Access Token（非密碼，建議用 PAT） |

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
| 2026-11-18 | ⏳ 等待 Review | |

## Review 日誌

| 日期 | Reviewer | 發現 | 處理方式 |
|------|----------|------|----------|

## 備註

- GoReleaser 免費版支援 cross-compile，不需要 Pro 授權
- `dist/` 已在 `.gitignore` 內（GoReleaser 預設輸出目錄）
- 本機測試：`goreleaser release --snapshot --clean` 可模擬 cross-compile
- `ubuntu-24.04-arm` 是 GitHub Actions 原生 ARM64 runner，不需要 QEMU
- 若未來需要 PR 預覽建構，可加 `.github/workflows/docker-pr.yml`
