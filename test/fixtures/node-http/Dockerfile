# syntax=docker/dockerfile:1

FROM node:24.22.0-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5 AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev

FROM node:24.22.0-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5 AS build
WORKDIR /app
COPY package.json package-lock.json tsconfig.json ./
COPY src ./src
RUN npm ci
ARG SOURCE_REVISION=unknown
ARG SOURCE_TREE_STATE=unknown
ENV SOURCE_REVISION=$SOURCE_REVISION \
    SOURCE_TREE_STATE=$SOURCE_TREE_STATE
RUN npm run build

FROM node:24.22.0-bookworm-slim@sha256:a5f4366e834695d5f363b920c104953a535d3a9864d1bdf74566a0ebe01daee5
WORKDIR /app
COPY --from=deps --chown=node:node /app/node_modules ./node_modules
COPY --from=build --chown=node:node /app/dist ./dist
COPY --chown=node:node package.json package-lock.json ./
USER node
ENV NODE_ENV=production \
    NODE_HTTP_MODE=baseline \
    NODE_HTTP_INTENSITY=0 \
    PORT=8080
EXPOSE 8080
CMD ["node", "--enable-source-maps", "dist/server.js"]
