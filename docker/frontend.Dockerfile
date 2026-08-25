# JotHost frontend image.

# ---------- dev ----------
FROM node:22-alpine AS dev
WORKDIR /app
ENV NODE_ENV=development
# Dependencies are installed at image build time; the source tree is
# bind-mounted at runtime.
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
EXPOSE 5173
CMD ["npm", "run", "dev"]

# ---------- builder ----------
FROM node:22-alpine AS builder
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ---------- runtime ----------
# Production serves pre-built static files through Nginx
# (ARCHITECTURE.md section 15).
FROM nginx:1.27-alpine AS runtime
COPY docker/nginx/frontend.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/dist /usr/share/nginx/html

HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD wget -q -O /dev/null http://127.0.0.1/ || exit 1
