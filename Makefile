.PHONY: web server run

web:
	cd web && pnpm install && pnpm build

server:
	cd server && go build -o ../antigravity2api .

run: web server
	ADMIN_TOKEN=$${ADMIN_TOKEN:-admin-token} API_KEY=$${API_KEY:-sk-antigravity} DATA_DIR=./data ./antigravity2api
