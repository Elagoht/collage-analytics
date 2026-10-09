# elagoht/analytics

A collage plugin that adds an analytics snippet to every page's head — Plausible,
Umami, GoatCounter, or Google Analytics 4 — in production and in static builds,
never in development, with Do Not Track and consent respected before anything
loads.

```go
app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{analytics.New(analytics.Options{
		Plausible: &analytics.Plausible{Domain: "example.com"},
	})},
})
```

Requires collage v0.57.0 or later. The layout places the snippet with
`{{hoist "head"}}`; without the marker nothing appears.

## Providers

| Provider | Needs | Tag |
| --- | --- | --- |
| `Plausible` | `Domain`; `Script` for a server of your own | `<script defer data-domain="example.com" src="https://plausible.io/js/script.js">` |
| `Umami` | `WebsiteID`; `Script` for a server of your own | `<script defer data-website-id="…" src="https://cloud.umami.is/script.js">` |
| `GoatCounter` | `Code`, or `Endpoint` for a server of your own; `Script` | `<script async data-goatcounter="https://mysite.goatcounter.com/count" src="https://gc.zgo.at/count.js">` |
| `GoogleAnalytics` | `MeasurementID` (`G-…`) | through the loader, below |

Plausible, Umami and GoatCounter count visits without cookies and without personal
data. **Google Analytics 4 is not privacy-friendly**: it sets cookies and sends
what it collects to Google, and in much of the world it may only load after the
visitor agrees — use it with `RequireConsent`. It is here because a site is
sometimes asked for it.

Several providers can be set at once, for a move from one to another. Plausible's
`Domain` takes several sites, comma-separated, as Plausible does. Plausible's newer
per-site scripts need an inline call to start; the plugin uses the `data-domain`
script, which needs none — `Script` can name any of its variants
(`script.outbound-links.js`, …).

## Where it runs

- **In production** every page gets the snippet, a registered error page included.
- **In a static build** too: a static site is where analytics runs, and the
  snippet is in every written page. The loader, when there is one, is written as a
  file beside them.
- **In development** nothing is added and nothing is served: every view would be
  the developer's own. The configuration is still checked, so a mistake shows up
  before it is deployed.

`Exclude` names pages that get no snippet — an admin page, a page behind a login.
The snippet is hoisted under the key `analytics`, so a page declaring that key
itself replaces it.

## Do Not Track and consent

A page cached for every reader cannot decide on the server whether one reader
wants to be tracked. So with either option the page loads a small script of the
plugin's own, from the site itself — `/collage-analytics.js` by default,
`LoaderPath` moves it — and that script decides in the browser, before any
provider's script is fetched:

- **`RespectDNT`** loads nothing when the browser sends Do Not Track or Global
  Privacy Control. Umami is also told to check for itself.
- **`RequireConsent`** loads nothing until the page calls
  `window.collageAnalyticsConsent()`. The site owns its banner; the plugin owns
  only the waiting:

  ```js
  document.querySelector("#accept-analytics").addEventListener("click", () => {
    window.collageAnalyticsConsent();
    banner.hidden = true;
  });
  ```

  The answer is remembered in `localStorage` under `collage-analytics-consent`, so
  the next page loads without asking again. `collageAnalyticsConsent(false)`
  forgets it; a script already loaded stays until the next page. The function is
  defined by the deferred loader, so call it from the banner's handlers, not from
  a script that runs before the page has parsed.

Google Analytics always goes through the loader, because it has to be configured by
a call once its script is on the page, and an inline script is what a strict policy
refuses.

## Consent with elagoht/consent

With [elagoht/consent](https://github.com/Elagoht/collage-consent) on the site,
set `ConsentCategory` instead of `RequireConsent`. Every tag the plugin writes
becomes plain text the consent plugin runs once the visitor has agreed to that
category, and the visitor gets one banner and one place to change their mind:

```json
{ "elagoht/analytics": { "plausible": { "domain": "example.com" }, "consentCategory": "analytics" } }
```

```html
<script type="text/plain" data-consent="analytics" defer data-domain="example.com" src="https://plausible.io/js/script.js"></script>
```

- The plugin does not import the consent plugin; it only writes the markup, so
  `consentCategory` must name a category the consent plugin knows
  (`^[a-z0-9-]+$`), or the tags never run.
- Google Analytics is written as two gated tags, its script and the inline call
  that configures it, with no loader.
- `RespectDNT` still applies: the page then holds one gated tag, the loader, so
  it runs only after consent and checks Do Not Track before loading anything.
- `ConsentCategory` and `RequireConsent` together stop the application from
  starting; so does a category that is not lowercase letters, digits and hyphens.
- Google Analytics' inline tag is an inline script. The plugin cannot put a
  nonce on it, so under a strict policy it needs a hash in `script-src`; see the
  consent plugin's notes on gated inline scripts.

## Content-Security-Policy

Every script is external, so a strict policy needs origins, not `'unsafe-inline'`.
With [collage-secure](https://github.com/Elagoht/collage-secure):

| Provider | `script-src` | `connect-src` |
| --- | --- | --- |
| Plausible | `https://plausible.io` | `https://plausible.io` |
| Umami | `https://cloud.umami.is` | the server it sends to |
| GoatCounter | `https://gc.zgo.at` | `https://mysite.goatcounter.com` (and `img-src`) |
| Google Analytics | `https://www.googletagmanager.com` | `https://*.google-analytics.com https://*.analytics.google.com https://*.googletagmanager.com` |

With the loader, add `'self'` to `script-src`. A self-hosted provider's own origin
takes the place of the one in the table.

## Configuration

```json
{
  "elagoht/analytics": {
    "plausible": { "domain": "example.com", "script": "https://stats.example.com/js/script.js" },
    "umami": { "websiteId": "94db1cb1-74f4-4a40-ad6c-962362670409", "script": "https://cloud.umami.is/script.js" },
    "goatcounter": { "code": "mysite" },
    "googleAnalytics": { "measurementId": "G-XXXXXXXXXX" },
    "exclude": ["admin"],
    "respectDnt": true,
    "requireConsent": true,
    "loaderPath": "/collage-analytics.js"
  }
}
```

`consentCategory` replaces `requireConsent`; use one.

No provider, a Plausible domain with a scheme or a path, an Umami id that is not a
UUID, a GoatCounter code that is not one, a Universal Analytics `UA-` id, a script
or endpoint URL that is not absolute http or https, and a loader path not beginning
with `/` stop the application from starting — in development too.

## Limitations

- The Do Not Track and consent checks run in the browser. A visitor with scripts
  off loads no analytics either way; one who edits `localStorage` consents for
  themselves, which is theirs to do.
- An excluded page name the plugin cannot find when it starts is a warning, not an
  error: a page another plugin registers later is not there yet.
- Ad blockers block the providers' scripts, and some block a loader whose name
  says "analytics"; `LoaderPath` renames it, the providers' own are theirs.
- The plugin cannot put `{{cspNonce}}` on its tags, so a nonce-only policy with
  `'strict-dynamic'` and no host sources refuses them; list the origins.

## Changes

- **0.2.0** `ConsentCategory` gates every tag behind elagoht/consent. Uses collage
  v0.57.0.
