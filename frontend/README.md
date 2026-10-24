# HarnessMesh Console

Modern React/TypeScript SaaS console for HarnessMesh.

```sh
npm install
npm run dev
```

The Vite development server proxies `/api`, `/healthz` and `/metrics` to the
HarnessMesh MCP HTTP process on `127.0.0.1:8787`. Production builds are emitted
to `dist/` and should be served by the deployment's TLS-terminating web tier.

The console uses the existing tenant API-key contract and never stores keys in
the application server.
