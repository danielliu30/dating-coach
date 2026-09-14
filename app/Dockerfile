# Exports the Expo web bundle, then serves it as static files. The nginx
# reverse proxy in ../nginx fronts this container; it is never exposed directly.
FROM node:20-alpine AS build
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY . .
# Baked into the bundle at export time. Empty means same-origin, i.e. REST and
# the chat WebSocket both go through whatever origin served the page.
ARG EXPO_PUBLIC_API_URL=
ENV EXPO_PUBLIC_API_URL=${EXPO_PUBLIC_API_URL}
ARG EXPO_PUBLIC_GOOGLE_CLIENT_ID=
ENV EXPO_PUBLIC_GOOGLE_CLIENT_ID=${EXPO_PUBLIC_GOOGLE_CLIENT_ID}
RUN npx expo export --platform web

FROM nginx:alpine
COPY --from=build /src/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
EXPOSE 80
