# Build from the repository root: docker build -f docker/Dockerfile .
ARG MAGPIE_VERSION=latest
ARG DOCKER_VERSION=29.9.2
ARG OPENBAO_VERSION=2.7.2
ARG GO_VERSION=1.26
FROM ghcr.io/yetone/magpie:${MAGPIE_VERSION} AS magpie
FROM quay.io/openbao/openbao:${OPENBAO_VERSION} AS openbao
FROM docker:${DOCKER_VERSION}-dind AS docker
# The board page is a Vite build that team-worker embeds; it is not committed.
FROM node:22-bookworm-slim AS web
ARG NPM_REGISTRY=https://registry.npmjs.org/
WORKDIR /src/internal/board/web
COPY internal/board/web/package.json internal/board/web/package-lock.json ./
RUN npm_config_registry="$NPM_REGISTRY" npm ci --no-audit --no-fund
COPY internal/board/web/ ./
RUN npm run build
FROM golang:${GO_VERSION}-bookworm AS team-worker
ARG GOPROXY=https://proxy.golang.org,direct
WORKDIR /src
COPY go.mod go.sum ./
RUN GOPROXY="$GOPROXY" go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=web /src/internal/board/dist/ internal/board/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/ ./cmd/...
WORKDIR /src/docker/ops
COPY docker/ops/go.mod docker/ops/go.sum ./
RUN GOPROXY="$GOPROXY" go mod download
COPY docker/ops/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/team-worker-ops .
FROM node:22-bookworm-slim
ARG CLAUDE_VERSION=latest
ARG CODEX_VERSION=latest
ARG PI_VERSION=1.4.0
# Package mirrors for slow networks; the defaults are the upstream sources.
ARG DEBIAN_MIRROR=deb.debian.org
ARG NPM_REGISTRY=https://registry.npmjs.org/
ARG PIP_INDEX_URL=https://pypi.org/simple
ARG BUN_DOWNLOAD_BASE=https://github.com/oven-sh/bun/releases/download
RUN sed -i "s#deb.debian.org#${DEBIAN_MIRROR}#g" /etc/apt/sources.list.d/debian.sources \
    && apt-get update && apt-get install -y --no-install-recommends \
    bash ca-certificates curl git tmux python3 python3-venv ripgrep nginx \
    && rm -rf /var/lib/apt/lists/*
RUN python3 -m venv /opt/venv \
    && npm_config_registry="$NPM_REGISTRY" npm install -g "@anthropic-ai/claude-code@${CLAUDE_VERSION}" \
        "@openai/codex@${CODEX_VERSION}" "@earendil-works/pi-coding-agent@${PI_VERSION}"
# Python Playwright with Chromium (full browser and headless shell) for browser
# testing skills; browsers live in the image so every agent can launch them.
ARG PLAYWRIGHT_VERSION=1.64.1
# Empty uses Playwright's own CDN; a mirror such as
# https://cdn.npmmirror.com/binaries/playwright serves the same build paths.
ARG PLAYWRIGHT_DOWNLOAD_HOST=
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
RUN /opt/venv/bin/pip install --no-cache-dir "playwright==${PLAYWRIGHT_VERSION}" \
    && PLAYWRIGHT_DOWNLOAD_HOST="$PLAYWRIGHT_DOWNLOAD_HOST" /opt/venv/bin/playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/* \
    && chmod -R a+rX "$PLAYWRIGHT_BROWSERS_PATH"
# dsh-tui accepts only the dsh releases it names, so both stay pinned. Its
# profile is built here and seeded into the home volume on first start.
ARG DSH_VERSION=0.2.1-rc.2
ARG DSH_TUI_VERSION=0.17.0
ARG PNPM_VERSION=12.15.0
RUN npm_config_registry="$NPM_REGISTRY" npm install -g "@deepseek-ai/dsh@${DSH_VERSION}" "pnpm@${PNPM_VERSION}" \
    && npm_config_registry="$NPM_REGISTRY" DSH_HOME=/opt/dsh dsh plugin --profile tui add \
        "@deepseek-harness-tui/dsh-tui@${DSH_TUI_VERSION}" \
    && rm -rf /opt/dsh/profiles/tui/.plugin-manager /root/.local/share/pnpm /root/.cache \
    && chmod -R a+rX /opt/dsh
ARG BUN_VERSION=1.4.2
ARG TARGETARCH
# Magpie runs plugins on Bun and otherwise downloads it from GitHub at runtime.
RUN case "${TARGETARCH:-amd64}" in \
        amd64) bun_target=linux-x64-baseline ;; \
        arm64) bun_target=linux-aarch64 ;; \
        *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
    && bun_base="${BUN_DOWNLOAD_BASE}/bun-v${BUN_VERSION}" \
    && curl -fsSL -o /tmp/bun.zip "$bun_base/bun-$bun_target.zip" \
    && curl -fsSL "$bun_base/SHASUMS256.txt" \
        | awk -v f="bun-$bun_target.zip" '$2 == f { print $1 "  /tmp/bun.zip" }' \
        | grep . | sha256sum -c - \
    && python3 -c 'import sys, zipfile; sys.stdout.buffer.write(zipfile.ZipFile("/tmp/bun.zip").read(sys.argv[1]))' \
        "bun-$bun_target/bun" > /usr/local/bin/bun \
    && chmod 755 /usr/local/bin/bun \
    && rm /tmp/bun.zip \
    && bun --version
RUN apt-get update && apt-get install -y --no-install-recommends \
    iptables iproute2 util-linux xz-utils systemd systemd-sysv dbus procps \
    && rm -rf /var/lib/apt/lists/*
# NARB_DISABLE_NATIVE_CACHE: dsh loads native addons from node_modules instead of
# copying them into /tmp, which the systemd mode mounts as a noexec tmpfs.
# /opt/team-worker/bin comes first: team-worker.sh mounts freshly built
# team-worker binaries there, so updates need no image rebuild. The copies
# in /usr/local/bin are the fallback when nothing is mounted.
ENV PATH="/opt/team-worker/bin:/opt/venv/bin:${PATH}" HOME=/home/node TERM=xterm-256color \
    TEAM_WORKER_DATA_DIR=/home/node/.team-worker MAGPIE_ADDR=127.0.0.1:3425 \
    TEAM_WORKER_PROVIDERS_FILE=/etc/team-worker/providers.yaml \
    DISABLE_AUTOUPDATER=1 MAGPIE_BUN=/usr/local/bin/bun \
    NARB_DISABLE_NATIVE_CACHE=1
COPY --from=magpie /magpie /usr/local/bin/magpie
COPY --from=openbao /usr/bin/bao /usr/local/bin/bao
COPY --from=docker /usr/local/bin/docker /usr/local/bin/dockerd \
    /usr/local/bin/docker-init /usr/local/bin/docker-proxy \
    /usr/local/bin/containerd-shim-runc-v2 \
    /usr/local/bin/ctr /usr/local/bin/runc /usr/local/bin/
COPY --from=docker /usr/local/libexec/docker/cli-plugins/ /usr/local/libexec/docker/cli-plugins/
COPY --from=team-worker /out/team-worker /out/team-workerd /out/team-worker-ops /usr/local/bin/
# Login shells (a window opened by hand in tmux) reset PATH from /etc/profile;
# keep the mounted binaries first there too.
RUN mkdir -p /opt/team-worker/bin \
    && printf 'export PATH="/opt/team-worker/bin:/opt/venv/bin:$PATH"\n' > /etc/profile.d/team-worker.sh
COPY skills/team-worker/ /opt/team-worker/skills/team-worker/
COPY docker/nginx.conf /etc/nginx/team-worker.conf
COPY docker/nginx-proxy.conf /etc/nginx/team-worker-proxy.conf
COPY docker/openbao.hcl /etc/openbao/openbao.hcl
COPY docker/subpath.js /etc/nginx/subpath.js
COPY docker/systemd/ /etc/systemd/system/
RUN systemctl enable docker.service magpie.service openbao.service openbao-agent.service team-worker.service team-worker-skills-update.timer \
    && systemctl mask getty.target console-getty.service systemd-udevd.service \
       systemd-udevd-control.socket systemd-udevd-kernel.socket systemd-logind.service \
    && truncate -s 0 /etc/machine-id \
    && rm -f /var/lib/dbus/machine-id \
    && ln -s /etc/machine-id /var/lib/dbus/machine-id
COPY docker/magpie-config/ /etc/team-worker/
COPY --chmod=755 docker/leader-session.sh /usr/local/bin/leader-session
COPY --chmod=755 docker/container-entrypoint.sh /usr/local/bin/container-entrypoint
COPY --chmod=755 docker/docker-entrypoint.sh /usr/local/bin/team-worker-docker-entrypoint
USER node
WORKDIR /workspace
EXPOSE 8080
ENTRYPOINT ["team-worker-docker-entrypoint"]
CMD ["start"]
