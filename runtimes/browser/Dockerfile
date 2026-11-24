FROM node:24.22.2-bookworm-slim@sha256:01d566e8d94d8c619591d78652babfccd3c6992a3dffa9ed5ab39e11cf6c70e9 AS dependencies
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev --ignore-scripts --no-audit --no-fund

FROM mcr.microsoft.com/playwright:v1.65.1-noble@sha256:1507cbfc550a63a6b7396791ca4f00df17beb56342d2447b3f106c306f525579
WORKDIR /app
COPY --from=dependencies /usr/local/bin/node /usr/local/bin/node
COPY --from=dependencies /usr/local/LICENSE /usr/local/share/licenses/node/LICENSE
COPY --from=dependencies /app/node_modules ./node_modules
COPY package.json ./
COPY *.ts ./
USER pwuser
EXPOSE 8080
CMD ["/usr/local/bin/node", "server.ts"]
