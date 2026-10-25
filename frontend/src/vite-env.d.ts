/// <reference types="vite/client" />
declare module "*.css";
interface ImportMetaEnv { readonly VITE_OIDC_AUTHORITY?: string; readonly VITE_OIDC_CLIENT_ID?: string; readonly VITE_OIDC_SCOPE?: string; }
