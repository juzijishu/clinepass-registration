.DEFAULT_GOAL := run
.PHONY: run build ensure-env browser-deps worker-test pay-tool
run:
	go run .
build:
	go build -o ../bin/auto .
ensure-env:
	bash scripts/ensure-worker-env.sh
browser-deps:
	sudo apt-get install -y xvfb libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libgbm1 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libpango-1.0-0 libcairo2 fonts-liberation fonts-noto-color-emoji fonts-freefont-ttf fonts-unifont libasound2t64 || \
	sudo apt-get install -y xvfb libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 libgbm1 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libpango-1.0-0 libcairo2 fonts-liberation fonts-noto-color-emoji fonts-freefont-ttf fonts-unifont libasound2
# 执行器代码使用 3.10+ 语法，优先用与运行时代码一致的虚拟环境解释器。
WORKER_PY := $(if $(wildcard worker/.venv/bin/python),$(CURDIR)/worker/.venv/bin/python,python3)
worker-test:
	@cd worker && "$(WORKER_PY)" -c 'import sys; sys.version_info >= (3, 10) or sys.exit("需要 Python 3.10+ 的运行环境（当前 %d.%d），请先运行 make ensure-env" % sys.version_info[:2])'
	cd worker && "$(WORKER_PY)" -m unittest discover -p 'test_*.py' -v
pay-tool:
	mkdir -p login-tool/dist
	go build -ldflags="-s -w" -o login-tool/dist/pay-linux ./login-tool
	GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o login-tool/dist/pay.exe ./login-tool
