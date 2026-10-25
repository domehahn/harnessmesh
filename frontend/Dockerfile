FROM node:22-alpine AS build
WORKDIR /app
ARG VITE_OIDC_AUTHORITY
ARG VITE_OIDC_CLIENT_ID=harnessmesh-dashboard
ARG VITE_OIDC_SCOPE="openid profile email"
ENV VITE_OIDC_AUTHORITY=$VITE_OIDC_AUTHORITY VITE_OIDC_CLIENT_ID=$VITE_OIDC_CLIENT_ID VITE_OIDC_SCOPE=$VITE_OIDC_SCOPE
COPY package*.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:1.31-alpine3.24-slim
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
HEALTHCHECK --interval=10s --timeout=3s --retries=5 CMD nginx -t || exit 1
