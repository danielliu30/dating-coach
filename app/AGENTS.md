# app guardrails (Expo SDK 57 web)

# Expo HAS CHANGED

Read the exact versioned docs at https://docs.expo.dev/versions/v57.0.0/ before writing any code.

TypeScript in `src/`, entry `App.tsx`/`index.ts`, served as a static `expo export --platform web` bundle behind `nginx.conf`. Jest unit tests in `jest.config.js`/`jest.setup.ts`.

## Required gate (`typecheck-export`)

```sh
cd app
npm ci
npm run typecheck        # tsc --noEmit
npm test -- --ci         # jest
npx expo export --platform web
```

## Invariants

- **Build-time config:** `WEB_API_URL`/`API_PUBLIC_URL` and `GOOGLE_CLIENT_ID` are baked into the bundle at build — changing them requires a rebuild; there is no runtime override.
- **Never commit or log tokens** (JWT lives in storage and WS upgrade headers); don't add logging that captures auth headers or verification codes.
- **Resilience is contractual:** offline queueing, token refresh, and WS replay are covered by the e2e `resilience` spec — changes to auth/network layers must keep it green.
- **`nginx.conf` is security-sensitive:** the web image's response headers (CSP/security headers) are set there — don't weaken them.
- Web-only target: don't add native-only APIs or dependencies that break `expo export --platform web`.
