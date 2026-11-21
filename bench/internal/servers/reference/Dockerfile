# @modelcontextprotocol/server-postgres 0.6.2, the reference server, as its README runs it (`npx -y
# @modelcontextprotocol/server-postgres <url>`), but installed once from package-lock.json: the package pins
# @modelcontextprotocol/sdk exactly and pg with a caret range, so the lock is what makes every build the same.
# node:22-alpine, pinned by the digest of its multi-platform index (linux/amd64 and linux/arm64).
FROM node:22-alpine@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund
USER node
ENTRYPOINT ["node", "/app/node_modules/@modelcontextprotocol/server-postgres/dist/index.js"]
