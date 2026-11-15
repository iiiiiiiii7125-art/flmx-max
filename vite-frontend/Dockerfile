# 多阶段构建 - 构建阶段
FROM --platform=$BUILDPLATFORM node:26-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS builder

WORKDIR /app

COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN corepack enable pnpm && pnpm install --frozen-lockfile

COPY . .
RUN pnpm run build

# 生产阶段
FROM nginx:stable-alpine@sha256:0e52bd054126cdc040b1276c0af347aeb02a41b9cfe305d6094767ac30e97949 AS production-stage

COPY nginx.conf /etc/nginx/nginx.conf

COPY --from=builder /app/dist /usr/share/nginx/html

# F-U1-01：构建上下文文件模式随检出 umask 而变（umask 0007 时 public/
# 直出文件进镜像是 660），而 nginx worker 以 nginx 用户运行、读不了
# root 属主的 660 文件 → 全部 403。构建期统一补可读位，与 umask
# 无关；a+rX 只给目录补执行位（遍历所需），文件不补执行位。
RUN chmod -R a+rX /usr/share/nginx/html

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
