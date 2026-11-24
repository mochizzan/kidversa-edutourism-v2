FROM node:22-alpine AS build
WORKDIR /app

COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --fetch-retries=6 --fetch-retry-mintimeout=15000 \
           --fetch-retry-maxtimeout=120000 --fetch-timeout=600000

COPY index.html *.json *.ts ./
COPY src ./src
COPY public ./public
RUN npm run build

FROM nginx:alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
