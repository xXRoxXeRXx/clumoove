# --- Development Stage ---
FROM node:24-alpine AS dev
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .
EXPOSE 3000
CMD ["npm", "run", "dev", "--", "--host"]

# --- Builder Stage (Runs natively on build host CPU) ---
FROM --platform=$BUILDPLATFORM node:24-alpine AS builder
WORKDIR /app
COPY package*.json ./
RUN npm ci
COPY . .
RUN chmod -R +x node_modules/.bin/ && npm run build

# --- Production Stage ---
FROM nginx:1.27-alpine AS prod
# Configure Nginx for unprivileged non-root operation
RUN rm -f /etc/nginx/conf.d/default.conf && \
    sed -i '/^[[:space:]]*user[[:space:]]/d' /etc/nginx/nginx.conf && \
    sed -i '/^[[:space:]]*pid[[:space:]]/d' /etc/nginx/nginx.conf && \
    sed -i '1i pid /tmp/nginx.pid;' /etc/nginx/nginx.conf && \
    mkdir -p /run /var/run && \
    touch /run/nginx.pid && \
    chown -R nginx:nginx /run/nginx.pid /var/cache/nginx /var/log/nginx /etc/nginx/conf.d /docker-entrypoint.d

COPY --from=builder --chown=nginx:nginx /app/dist /usr/share/nginx/html
# The nginx entrypoint expands this template after validating the runtime API
# origin and writes the runtime config before nginx starts.
COPY --chown=nginx:nginx nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --chown=nginx:nginx docker-entrypoint.d/30-runtime-api-config.sh /docker-entrypoint.d/30-runtime-api-config.sh
RUN chmod +x /docker-entrypoint.d/30-runtime-api-config.sh && \
    chown -R nginx:nginx /usr/share/nginx/html /etc/nginx/templates

USER nginx
EXPOSE 3000
CMD ["nginx", "-g", "daemon off;"]
