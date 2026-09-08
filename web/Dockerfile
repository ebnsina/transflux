FROM node:24-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run build

FROM nginx:1.27-alpine
# The dashboard and the API are served from one origin. That is not only
# convenient: it means the browser never makes a cross-origin request, so there
# is no CORS configuration to keep in step between development and production.
COPY nginx.conf /etc/nginx/conf.d/default.conf
# The SPA build has no index.html — every route is served by the fallback — so
# nginx's own welcome page has to go, or it wins at "/".
RUN rm -rf /usr/share/nginx/html/*
COPY --from=build /src/build /usr/share/nginx/html
