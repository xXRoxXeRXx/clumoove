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
FROM nginx:alpine AS prod
COPY --from=builder /app/dist /usr/share/nginx/html
# The nginx entrypoint expands this template after validating the runtime API
# origin and writes the runtime config before nginx starts.
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY docker-entrypoint.d/30-runtime-api-config.sh /docker-entrypoint.d/30-runtime-api-config.sh
RUN chmod +x /docker-entrypoint.d/30-runtime-api-config.sh
EXPOSE 3000
CMD ["nginx", "-g", "daemon off;"]
