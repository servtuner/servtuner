# Build stage: the Node major matches .tool-versions (nodejs 24).
FROM node:24-alpine3.24 AS build
WORKDIR /app
COPY package.json package-lock.json .npmrc ./
RUN npm ci
# api/openapi.json is not in this build context, and src/api/schema.d.ts is
# checked in, so the build needs no generation step.
COPY index.html tsconfig.json vite.config.ts ./
COPY public ./public
COPY src ./src
RUN npm run build

# Runtime stage: unprivileged nginx, listening on 8080 (chart value frontend.port).
FROM nginxinc/nginx-unprivileged:1.31.7-alpine3.24
# The templates dir is rendered with envsubst at start. base-path.envsh runs first
# and validates SERVTUNER_BASE_PATH (default "/"), the context path of the deployment.
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --chmod=0755 base-path.envsh /docker-entrypoint.d/12-servtuner-base-path.envsh
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
