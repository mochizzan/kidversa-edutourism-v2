# OpenWA 0.8.17 pins whatsapp-web.js 1.34.7. WhatsApp Web renamed
# Message._serialized to $1, so 1.34.7's sendMessage resolves to undefined and
# OpenWA fails every send with:
#
#   TypeError: Cannot read properties of undefined (reading 'id')
#
# whatsapp-web.js 1.34.8 carries the upstream fallback (wwebjs PR #201832,
# release 2026-11-12). Its dependency set is identical to 1.34.7, so replace
# only the package directory instead of re-running the root npm install (which
# would also pull OpenWA's dev dependencies).
ARG OPENWA_BASE=ghcr.io/rmyndharis/openwa:0.8.17
FROM ${OPENWA_BASE}

ARG WWEBJS_VERSION=1.34.8

RUN set -eux; \
    export HOME=/tmp npm_config_cache=/tmp/npm-cache; \
    cd /tmp; \
    npm pack "whatsapp-web.js@${WWEBJS_VERSION}" --silent; \
    mv /app/node_modules/whatsapp-web.js/node_modules /tmp/wwebjs-nested-deps; \
    rm -rf /app/node_modules/whatsapp-web.js; \
    mkdir -p /app/node_modules/whatsapp-web.js; \
    tar -xzf "whatsapp-web.js-${WWEBJS_VERSION}.tgz" \
        -C /app/node_modules/whatsapp-web.js --strip-components=1; \
    mv /tmp/wwebjs-nested-deps /app/node_modules/whatsapp-web.js/node_modules; \
    rm -f "whatsapp-web.js-${WWEBJS_VERSION}.tgz"; \
    node -e "const v = require('/app/node_modules/whatsapp-web.js/package.json').version; if (v !== '${WWEBJS_VERSION}') { console.error('expected ${WWEBJS_VERSION}, got ' + v); process.exit(1); }"
