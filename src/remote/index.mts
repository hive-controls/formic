/**
 * The remote-vendor surface: credential → endpoint builders, pure functions, no
 * browser and no recipe. A vendor choice becomes configuration the recipe reads.
 */
export {
  buildBrowserStackWsEndpoint,
  DEFAULT_CLIENT_PLAYWRIGHT_VERSION,
  type BrowserStackOptions,
} from "./browserstack.mts";
export {
  buildSauceRemoteGrid,
  type SauceLabsOptions,
  type SauceRemoteGrid,
} from "./saucelabs.mts";
