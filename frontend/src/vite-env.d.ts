/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** The release this UI was built from (the image build sets it); unset in development. */
  readonly VITE_SERVTUNER_VERSION?: string;
}
