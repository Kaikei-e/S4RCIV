# syntax=docker/dockerfile:1
FROM node:26-trixie-slim@sha256:3a771f83944bb763050c23c0225c260638c4b7899e7a72485ef75e5e570499e5 AS build
# Node 25+ official images no longer ship Corepack; install the packageManager pin directly.
RUN npm install -g pnpm@12.5.1
WORKDIR /app
# pnpm-workspace.yaml carries the pnpm `overrides:` (pnpm 11/12 reads them from the
# workspace file, not package.json); it must be present at install time or the
# lockfile no longer matches and --frozen-lockfile fails.
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm run build

# Test stage: Vitest component/unit tests + the in-browser verifier CDC tests.
FROM build AS test
CMD ["pnpm", "test"]

# adapter-node bundles a self-contained server into build/, so the runtime
# image needs no node_modules.
FROM node:26-trixie-slim@sha256:3a771f83944bb763050c23c0225c260638c4b7899e7a72485ef75e5e570499e5 AS runtime
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build /app/build ./build
EXPOSE 3000
USER node
ENTRYPOINT ["node", "build"]
