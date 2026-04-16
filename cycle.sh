go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out -o coverage.html
go build -o wyze-bridge ./cmd/wyze-bridge
set -a; source .env.dev; set +a
go run ./cmd/wyze-bridge
