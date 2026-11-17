# ============================================================
# xero-mirror Docker image
# 建構：docker build -t mmhk/xero-mirror .
# 執行：docker run --rm mmhk/xero-mirror serve
# ============================================================

# --- Stage 1: Build ---
FROM golang:1.24-alpine AS builder

WORKDIR /src

# 先複製依賴檔，利用 Docker layer cache 加速重複建構
COPY go.mod go.sum ./
RUN go mod download

# 複製原始碼並編譯（CGO_ENABLED=0：純靜態 binary，alpine musl 相容）
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /xero-mirror .

# --- Stage 2: Runtime ---
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /xero-mirror .

# serve 監聽 port（配合 HTTP_ADDR 環境變數）
EXPOSE 8080

# 預設 subcommand 是 serve；可覆寫為 sync / auth
ENTRYPOINT ["/app/xero-mirror"]
CMD ["serve"]
