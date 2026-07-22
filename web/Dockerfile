# syntax=docker/dockerfile:1
FROM node:26-trixie-slim@sha256:715e55e4b84e4bb0ff48e49b398a848f08e55daed8eb6a0ea1839ae53bc57583 AS build
# Node 25+ official images no longer ship Corepack; install the packageManager pin directly.
RUN npm install -g pnpm@11.15.1
WORKDIR /app
# pnpm-workspace.yaml carries the esbuild build-script allowlist (pnpm 11); it must
# be present at install time or `pnpm install` fails on the ignored build script.
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
RUN pnpm install --frozen-lockfile
COPY . .
RUN pnpm run build

# Test stage: Vitest component/unit tests + the in-browser verifier CDC tests.
FROM build AS test
CMD ["pnpm", "test"]

# adapter-node bundles a self-contained server into build/, so the runtime
# image needs no node_modules.
FROM node:26-trixie-slim@sha256:715e55e4b84e4bb0ff48e49b398a848f08e55daed8eb6a0ea1839ae53bc57583 AS runtime
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build /app/build ./build
EXPOSE 3000
USER node
ENTRYPOINT ["node", "build"]
