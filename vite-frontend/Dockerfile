# 多阶段构建 - 构建阶段
FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:fe4f6d74e20370acb04ad889077d14d0f64a9c8ea6a42bc60cddf1e844699914 AS builder

WORKDIR /app

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN corepack enable pnpm && pnpm install --frozen-lockfile

COPY . .
RUN pnpm run build

# 生产阶段
FROM nginx:stable-alpine@sha256:0e52bd054126cdc040b1276c0af347aeb02a41b9cfe305d6094767ac30e97949 AS production-stage

COPY nginx.conf /etc/nginx/nginx.conf

COPY --from=builder /app/dist /usr/share/nginx/html

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
