# syntax=docker/dockerfile:1
FROM node:26-trixie-slim@sha256:4ebb5ace66f15a24c14c492e01a8beeed4fddf970a856109f5126e703e5fe503 AS build
# Node 25+ official images no longer ship Corepack; install the packageManager pin directly.
RUN npm install -g pnpm@11.23.0
WORKDIR /app
# pnpm-workspace.yaml carries the pnpm `overrides:` (pnpm 11 reads them from the
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
FROM node:26-trixie-slim@sha256:4ebb5ace66f15a24c14c492e01a8beeed4fddf970a856109f5126e703e5fe503 AS runtime
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build /app/build ./build
EXPOSE 3000
USER node
ENTRYPOINT ["node", "build"]
