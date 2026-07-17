FROM node:22-alpine AS builder
WORKDIR /app

ENV CI=true
ENV NEXT_TELEMETRY_DISABLED=1

COPY package.json package-lock.json ./
RUN apk add --no-cache libc6-compat \
    && npm ci

COPY . .
RUN npm run build

FROM node:22-alpine
WORKDIR /app
RUN apk add --no-cache libc6-compat curl
COPY --from=builder /app/.next/standalone ./
COPY --from=builder /app/.next/static ./.next/static
COPY --from=builder /app/public ./public

EXPOSE 3100
ENV NODE_ENV=production
ENV HOSTNAME=0.0.0.0
ENV PORT=3100
CMD ["node", "server.js"]
