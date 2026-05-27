FROM node:22-bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl tini \
 && rm -rf /var/lib/apt/lists/*

ARG CODEX_VERSION=0.134.0
RUN npm install -g @openai/codex@${CODEX_VERSION} \
 && codex --version

WORKDIR /app

# server.mjs and schemas/ are bind-mounted from the repo by docker-compose so that
# edits to the bridge can be rolled out with `docker restart codex-bridge` without
# a full image rebuild.

ENV PORT=9002 \
    CODEX_BIN=codex \
    CODEX_BRIDGE_MODEL=gpt-5.5 \
    CODEX_BRIDGE_SCHEMA=/app/schemas/kdb_extract.schema.json \
    CODEX_BRIDGE_TIMEOUT_MS=90000

EXPOSE 9002

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["node", "/app/server.mjs"]
