// Package analytics is a collage plugin that adds an analytics snippet to every
// page's head.
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{analytics.New(analytics.Options{
//			Plausible: &analytics.Plausible{Domain: "example.com"},
//		})},
//	})
//
// Plausible, Umami and GoatCounter count visits without cookies or personal data;
// Google Analytics 4 is offered too, and is not that. The snippet is an external
// script, loaded with defer or async, so it never holds up the page, and a strict
// Content-Security-Policy needs only the provider's origin in script-src.
//
// It is added in production and in static builds — a static site is where
// analytics runs — and never in development, where every page view would be the
// developer's own.
//
// With RespectDNT or RequireConsent the page loads a small script of the plugin's
// own instead, from the site itself, which loads the provider's only once the
// browser has not asked not to be tracked, and the visitor has agreed:
//
//	document.querySelector("#accept").addEventListener("click", () => window.collageAnalyticsConsent())
package analytics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and the key its configuration is found under.
const Name = "elagoht/analytics"

// Options configures the plugin. At least one provider must be set; several may
// be, during a move from one to another.
type Options struct {
	Plausible   *Plausible   `json:"plausible"`
	Umami       *Umami       `json:"umami"`
	GoatCounter *GoatCounter `json:"goatcounter"`
	// GoogleAnalytics is Google Analytics 4. It sets cookies and sends what it
	// collects to Google: it is not privacy-friendly, and in much of the world it
	// needs the visitor's consent — see RequireConsent.
	GoogleAnalytics *GoogleAnalytics `json:"googleAnalytics"`
	// Exclude are names of pages that get no snippet: an admin page, a page
	// behind a login.
	Exclude []string `json:"exclude"`
	// RespectDNT loads nothing for a browser sending Do Not Track or Global
	// Privacy Control. The check runs in the browser, before any provider's
	// script is fetched; a page cached for everyone cannot make it on the
	// server.
	RespectDNT bool `json:"respectDnt"`
	// RequireConsent loads nothing until the page calls
	// window.collageAnalyticsConsent(). The site shows its own banner and calls
	// it when the visitor agrees; the answer is remembered in localStorage, and
	// collageAnalyticsConsent(false) forgets it.
	RequireConsent bool `json:"requireConsent"`
	// LoaderPath is where the plugin's loader script is served, when there is
	// one: with RespectDNT, RequireConsent or Google Analytics. Default
	// "/collage-analytics.js".
	LoaderPath string `json:"loaderPath"`
}

// Plausible is https://plausible.io, or a server of one's own.
type Plausible struct {
	// Domain is the site as Plausible knows it: "example.com". Several,
	// comma-separated, send each view to each.
	Domain string `json:"domain"`
	// Script is the script's URL, for a self-hosted server or one of
	// Plausible's script variants. Default "https://plausible.io/js/script.js".
	Script string `json:"script"`
}

// Umami is https://umami.is, or a server of one's own.
type Umami struct {
	// WebsiteID is the site's id, a UUID.
	WebsiteID string `json:"websiteId"`
	// Script is the script's URL. Default "https://cloud.umami.is/script.js".
	Script string `json:"script"`
}

// GoatCounter is https://www.goatcounter.com, or a server of one's own.
type GoatCounter struct {
	// Code is the site's code: "mysite" counts at mysite.goatcounter.com.
	Code string `json:"code"`
	// Endpoint is the count URL, for a self-hosted server:
	// "https://stats.example.com/count". Default from Code.
	Endpoint string `json:"endpoint"`
	// Script is the script's URL. Default "https://gc.zgo.at/count.js".
	Script string `json:"script"`
}

// GoogleAnalytics is Google Analytics 4.
type GoogleAnalytics struct {
	// MeasurementID is the stream's id: "G-XXXXXXXXXX".
	MeasurementID string `json:"measurementId"`
}

// Plugin adds the snippet.
type Plugin struct {
	opts    Options
	off     bool
	exclude map[string]bool
	head    template.HTML
}

// New returns a plugin with opts as its starting point, which the application's
// own configuration is then decoded over.
func New(opts Options) *Plugin { return &Plugin{opts: opts} }

func (p *Plugin) Name() string                   { return Name }
func (p *Plugin) Version() string                { return "0.1.4" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

var (
	_ collage.Plugin           = (*Plugin)(nil)
	_ collage.BeforeRenderHook = (*Plugin)(nil)
)

// script is one provider's script: its URL, whether it loads async rather than
// deferred, and its data attributes. The same value is written as a tag in the
// head, or handed to the loader to create.
type script struct {
	Src   string            `json:"src"`
	Async bool              `json:"async,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// loaderConfig is what the loader is told.
type loaderConfig struct {
	DNT     bool     `json:"dnt"`
	Consent bool     `json:"consent"`
	Scripts []script `json:"scripts"`
	GA      string   `json:"ga,omitempty"`
}

var (
	uuidPattern   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	gaPattern     = regexp.MustCompile(`^G-[A-Z0-9]{4,20}$`)
	codePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	domainPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
)

// Init reads the configuration, checks it and, outside development, serves the
// loader when one is needed.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	cfg, err := collage.PluginConfig(host, p.opts)
	if err != nil {
		return err
	}
	p.opts = cfg
	scripts, ga, err := p.providers()
	if err != nil {
		return err
	}
	o := &p.opts
	if o.LoaderPath == "" {
		o.LoaderPath = "/collage-analytics.js"
	}
	if !strings.HasPrefix(o.LoaderPath, "/") {
		return fmt.Errorf("analytics: loader path %q must begin with /", o.LoaderPath)
	}
	p.exclude = make(map[string]bool, len(o.Exclude))
	for _, name := range o.Exclude {
		p.exclude[name] = true
		if _, ok := host.Page(name); !ok {
			// Not an error: a page another plugin registers after this one's
			// Init is not there yet to be found.
			host.Logger().Warn("analytics: excluded page is not registered (yet)", "page", name)
		}
	}

	// Checked in development too, so a mistake is found before it is deployed;
	// but nothing is served or added there.
	if host.DevMode() {
		p.off = true
		return nil
	}

	if !o.RespectDNT && !o.RequireConsent && ga == "" {
		p.head = tags(scripts)
		return nil
	}
	body, err := loader(loaderConfig{DNT: o.RespectDNT, Consent: o.RequireConsent, Scripts: scripts, GA: ga})
	if err != nil {
		return err
	}
	doc := collage.NewDocument(Name+":loader", "text/javascript; charset=utf-8").
		AtRoot(o.LoaderPath).
		WithBody(body).
		Build()
	if err := host.RegisterDocument(doc); err != nil {
		return fmt.Errorf("analytics: %w", err)
	}
	p.head = template.HTML(`<script defer src="` + html.EscapeString(o.LoaderPath) + `"></script>`) // assembled from an escaped path
	return nil
}

// providers checks each configured provider and returns its script.
func (p *Plugin) providers() ([]script, string, error) {
	o := p.opts
	var scripts []script
	if pl := o.Plausible; pl != nil {
		for _, d := range strings.Split(pl.Domain, ",") {
			if !domainPattern.MatchString(strings.TrimSpace(d)) {
				return nil, "", fmt.Errorf("analytics: plausible domain %q is not a domain: give it as example.com", pl.Domain)
			}
		}
		src, err := scriptURL("plausible", pl.Script, "https://plausible.io/js/script.js")
		if err != nil {
			return nil, "", err
		}
		scripts = append(scripts, script{Src: src, Attrs: map[string]string{"data-domain": pl.Domain}})
	}
	if u := o.Umami; u != nil {
		if !uuidPattern.MatchString(u.WebsiteID) {
			return nil, "", fmt.Errorf("analytics: umami website id %q is not a UUID", u.WebsiteID)
		}
		src, err := scriptURL("umami", u.Script, "https://cloud.umami.is/script.js")
		if err != nil {
			return nil, "", err
		}
		attrs := map[string]string{"data-website-id": u.WebsiteID}
		if o.RespectDNT {
			// Umami checks for itself too, which covers a visitor who turns
			// Do Not Track on after the page loaded.
			attrs["data-do-not-track"] = "true"
		}
		scripts = append(scripts, script{Src: src, Attrs: attrs})
	}
	if g := o.GoatCounter; g != nil {
		endpoint := g.Endpoint
		if endpoint == "" {
			if !codePattern.MatchString(g.Code) {
				return nil, "", fmt.Errorf("analytics: goatcounter code %q is not a site code; set Endpoint for a server of your own", g.Code)
			}
			endpoint = "https://" + g.Code + ".goatcounter.com/count"
		} else if _, err := scriptURL("goatcounter endpoint", endpoint, ""); err != nil {
			return nil, "", err
		}
		src, err := scriptURL("goatcounter", g.Script, "https://gc.zgo.at/count.js")
		if err != nil {
			return nil, "", err
		}
		scripts = append(scripts, script{Src: src, Async: true, Attrs: map[string]string{"data-goatcounter": endpoint}})
	}
	ga := ""
	if g := o.GoogleAnalytics; g != nil {
		if !gaPattern.MatchString(g.MeasurementID) {
			return nil, "", fmt.Errorf("analytics: google analytics measurement id %q is not a GA4 id, G-XXXXXXXXXX", g.MeasurementID)
		}
		ga = g.MeasurementID
	}
	if len(scripts) == 0 && ga == "" {
		return nil, "", errors.New("analytics: no provider is configured: set plausible, umami, goatcounter or googleAnalytics")
	}
	return scripts, ga, nil
}

// scriptURL returns raw, or def when raw is empty, after checking it is an
// absolute http or https URL.
func scriptURL(what, raw, def string) (string, error) {
	if raw == "" {
		raw = def
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("analytics: %s URL %q must be an absolute http or https URL", what, raw)
	}
	return raw, nil
}

// tags writes the scripts as tags, their attributes in a fixed order so a page
// renders the same bytes every time and its ETag holds.
func tags(scripts []script) template.HTML {
	var b strings.Builder
	for _, s := range scripts {
		b.WriteString("<script ")
		if s.Async {
			b.WriteString("async")
		} else {
			b.WriteString("defer")
		}
		keys := make([]string, 0, len(s.Attrs))
		for k := range s.Attrs {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			b.WriteString(" " + k + `="` + html.EscapeString(s.Attrs[k]) + `"`)
		}
		b.WriteString(` src="` + html.EscapeString(s.Src) + `"></script>`)
	}
	return template.HTML(b.String()) // assembled here from escaped values
}

// OnBeforeRender hoists the snippet into the page's head. Hoisted at depth zero, so
// a page can replace it by declaring the same key.
func (p *Plugin) OnBeforeRender(_ context.Context, ev *collage.BeforeRenderEvent) error {
	if p.off || p.head == "" || ev.Context == nil {
		return nil
	}
	if ev.Page != nil && p.exclude[ev.Page.Name] {
		return nil
	}
	ev.Context.Hoist("head", "analytics", p.head)
	return nil
}

// loader returns the loader script with cfg in it. It is written for every
// browser a site still serves, so it keeps to ES5.
func loader(cfg loaderConfig) ([]byte, error) {
	// encoding/json escapes <, > and &, so nothing in a value can end the
	// script early or be read as markup.
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("analytics: %w", err)
	}
	return []byte(strings.Replace(loaderSource, "__CONFIG__", string(data), 1)), nil
}

const loaderSource = `// collage-analytics loader: loads the site's analytics once the browser has not
// asked not to be tracked, and the visitor has agreed, when that is asked.
(function () {
  var cfg = __CONFIG__;
  var KEY = "collage-analytics-consent";
  var loaded = false;

  function tracked() {
    if (!cfg.dnt) return true;
    var n = window.navigator || {};
    return !(n.doNotTrack === "1" || window.doNotTrack === "1" || n.msDoNotTrack === "1" || n.globalPrivacyControl === true);
  }

  function consented() {
    if (!cfg.consent) return true;
    try { return window.localStorage.getItem(KEY) === "1"; } catch (e) { return false; }
  }

  function load() {
    if (loaded || !tracked()) return;
    loaded = true;
    for (var i = 0; i < cfg.scripts.length; i++) {
      var s = cfg.scripts[i];
      var el = document.createElement("script");
      el.src = s.src;
      if (s.async) el.async = true; else el.defer = true;
      for (var k in s.attrs) {
        if (Object.prototype.hasOwnProperty.call(s.attrs, k)) el.setAttribute(k, s.attrs[k]);
      }
      document.head.appendChild(el);
    }
    if (cfg.ga) {
      window.dataLayer = window.dataLayer || [];
      window.gtag = function () { window.dataLayer.push(arguments); };
      window.gtag("js", new Date());
      window.gtag("config", cfg.ga);
      var g = document.createElement("script");
      g.async = true;
      g.src = "https://www.googletagmanager.com/gtag/js?id=" + encodeURIComponent(cfg.ga);
      document.head.appendChild(g);
    }
  }

  // The site's banner calls this when the visitor agrees, and with false when
  // they withdraw; a script already loaded stays until the next page.
  window.collageAnalyticsConsent = function (given) {
    try {
      if (given === false) { window.localStorage.removeItem(KEY); return; }
      window.localStorage.setItem(KEY, "1");
    } catch (e) {}
    if (given !== false) load();
  };

  if (consented()) load();
})();
`
