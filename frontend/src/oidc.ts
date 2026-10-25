const verifierKey = "harnessmesh_oidc_pkce_verifier";
const stateKey = "harnessmesh_oidc_state";
const idTokenKey = "harnessmesh_oidc_id_token";
const refreshTokenKey = "harnessmesh_oidc_refresh_token";

const config = {
  authority: (import.meta.env.VITE_OIDC_AUTHORITY as string | undefined)?.replace(/\/$/, "") ?? "",
  clientId: (import.meta.env.VITE_OIDC_CLIENT_ID as string | undefined) ?? "harnessmesh-dashboard",
  scope: (import.meta.env.VITE_OIDC_SCOPE as string | undefined) ?? "openid profile email",
};

export function oidcConfigured(): boolean { return Boolean(config.authority && config.clientId); }
function randomValue(): string { const bytes = new Uint8Array(32); crypto.getRandomValues(bytes); return btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""); }
async function challenge(verifier: string): Promise<string> { const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier)); return btoa(String.fromCharCode(...new Uint8Array(digest))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""); }
function redirectURI(): string { return `${window.location.origin}${window.location.pathname}`; }

export async function beginOIDC(): Promise<void> {
  if (!oidcConfigured()) throw new Error("OIDC is not configured for this console build");
  const verifier = randomValue();
  sessionStorage.setItem(verifierKey, verifier);
  sessionStorage.setItem(stateKey, randomValue());
  const query = new URLSearchParams({ client_id: config.clientId, response_type: "code", redirect_uri: redirectURI(), scope: config.scope, state: sessionStorage.getItem(stateKey) ?? "", code_challenge: await challenge(verifier), code_challenge_method: "S256" });
  window.location.assign(`${config.authority}/protocol/openid-connect/auth?${query.toString()}`);
}

export async function completeOIDC(): Promise<string | null> {
  const params = new URLSearchParams(window.location.search);
  const code = params.get("code");
  if (!code) return null;
  const state = params.get("state");
  if (!state || state !== sessionStorage.getItem(stateKey)) throw new Error("OIDC state validation failed");
  const verifier = sessionStorage.getItem(verifierKey);
  if (!verifier) throw new Error("OIDC PKCE verifier is missing");
  const response = await fetch(`${config.authority}/protocol/openid-connect/token`, { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ grant_type: "authorization_code", client_id: config.clientId, redirect_uri: redirectURI(), code, code_verifier: verifier }) });
  if (!response.ok) throw new Error(`OIDC token exchange failed (${response.status})`);
  const payload = await response.json() as { access_token?: string; id_token?: string; refresh_token?: string };
  if (!payload.access_token) throw new Error("OIDC token response did not contain an access token");
  if (payload.id_token) sessionStorage.setItem(idTokenKey, payload.id_token);
  if (payload.refresh_token) sessionStorage.setItem(refreshTokenKey, payload.refresh_token);
  sessionStorage.removeItem(verifierKey); sessionStorage.removeItem(stateKey);
  window.history.replaceState({}, document.title, window.location.pathname + window.location.hash);
  return payload.access_token;
}

export async function refreshOIDC(): Promise<string | null> {
  const refreshToken = sessionStorage.getItem(refreshTokenKey);
  if (!oidcConfigured() || !refreshToken) return null;
  const response = await fetch(`${config.authority}/protocol/openid-connect/token`, { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: new URLSearchParams({ grant_type: "refresh_token", client_id: config.clientId, refresh_token: refreshToken }) });
  if (!response.ok) { sessionStorage.removeItem(refreshTokenKey); return null; }
  const payload = await response.json() as { access_token?: string; id_token?: string; refresh_token?: string };
  if (!payload.access_token) return null;
  sessionStorage.setItem("harnessmesh_oidc_access_token", payload.access_token);
  if (payload.id_token) sessionStorage.setItem(idTokenKey, payload.id_token);
  if (payload.refresh_token) sessionStorage.setItem(refreshTokenKey, payload.refresh_token);
  return payload.access_token;
}

export function logoutOIDC(): void {
  const idToken = sessionStorage.getItem(idTokenKey);
  sessionStorage.removeItem(idTokenKey);
  sessionStorage.removeItem(refreshTokenKey);
  if (!oidcConfigured() || !idToken) return;
  const query = new URLSearchParams({ id_token_hint: idToken, post_logout_redirect_uri: redirectURI(), client_id: config.clientId });
  window.location.assign(`${config.authority}/protocol/openid-connect/logout?${query.toString()}`);
}
