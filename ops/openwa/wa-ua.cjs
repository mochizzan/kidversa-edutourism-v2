// OpenWA runs whatsapp-web.js in headless Chrome. whatsapp-web.js pins a
// Chrome/101 default user agent (node_modules/whatsapp-web.js/src/util/
// Constants.js -> DefaultOptions.userAgent), and WhatsApp Web answers that
// (and every other headless-shaped UA) with its "WhatsApp works with Google
// Chrome 100+" gate. That gate page has no QR, so session start dies with
// "Engine initialization timed out" and the dashboard connect modal never
// gets a code.
//
// OpenWA exposes no user-agent setting: PUPPETEER_ARGS cannot carry one
// because its value is split on whitespace, and the adapter overrides any
// launch flag with page.setUserAgent(). This preload
// (NODE_OPTIONS=--require=/opt/wa-ua.cjs) rewrites the default before
// whatsapp-web.js is imported, so the engine launches with a real desktop UA.
//
// Override with WA_WEB_USER_AGENT if the image ever ships a browser major
// where the gate disagrees with this string.
const userAgent =
  process.env.WA_WEB_USER_AGENT ||
  'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36'

try {
  const constants = require('/app/node_modules/whatsapp-web.js/src/util/Constants.js')
  if (constants && constants.DefaultOptions) {
    constants.DefaultOptions.userAgent = userAgent
  }
} catch (err) {
  // Never block OpenWA startup: on an image where the module moved, the
  // engine still runs with its own default rather than crash-looping.
  console.warn(
    '[wa-ua] could not override whatsapp-web.js user agent:',
    err && err.message,
  )
}
