/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** "1" serves the UI from the in-browser mock backend. */
  readonly VITE_MOCK?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
