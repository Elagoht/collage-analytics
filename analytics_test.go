package analytics_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	analytics "github.com/Elagoht/collage-analytics"
	"github.com/Elagoht/collage/pkg/collage"
)

const layout = `<!doctype html><html><head><title>t</title>{{hoist "head"}}</head><body>x</body></html>`

func app(t *testing.T, dev bool, opts analytics.Options, config string) *collage.App {
	t.Helper()
	cfg := &collage.Config{
		DevMode:  dev,
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(layout)}}, Root: "t"},
		Plugins:  []collage.Plugin{analytics.New(opts)},
	}
	if config != "" {
		cfg.PluginConfig = map[string]json.RawMessage{analytics.Name: json.RawMessage(config)}
	}
	a, err := collage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*collage.Page{
		collage.NewPage("home").WithContent(collage.NewFragment("home", "p.html").Build()).WithPath("en", "/").Static().Build(),
		collage.NewPage("admin").WithContent(collage.NewFragment("admin", "p.html").Build()).WithPath("en", "/admin").Static().Build(),
	} {
		if err := a.RegisterPage(p); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func get(a *collage.App, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func head(t *testing.T, body string) string {
	t.Helper()
	start, end := strings.Index(body, "<head>"), strings.Index(body, "</head>")
	if start < 0 || end < start {
		t.Fatalf("no head in %s", body)
	}
	return body[start:end]
}

func TestProvidersInTheHead(t *testing.T) {
	cases := map[string]struct {
		opts analytics.Options
		want string
	}{
		"plausible": {
			analytics.Options{Plausible: &analytics.Plausible{Domain: "example.com"}},
			`<script defer data-domain="example.com" src="https://plausible.io/js/script.js"></script>`,
		},
		"plausible self-hosted": {
			analytics.Options{Plausible: &analytics.Plausible{Domain: "example.com,all.example.com", Script: "https://stats.example.com/js/script.js"}},
			`<script defer data-domain="example.com,all.example.com" src="https://stats.example.com/js/script.js"></script>`,
		},
		"umami": {
			analytics.Options{Umami: &analytics.Umami{WebsiteID: "94db1cb1-74f4-4a40-ad6c-962362670409"}},
			`<script defer data-website-id="94db1cb1-74f4-4a40-ad6c-962362670409" src="https://cloud.umami.is/script.js"></script>`,
		},
		"goatcounter": {
			analytics.Options{GoatCounter: &analytics.GoatCounter{Code: "mysite"}},
			`<script async data-goatcounter="https://mysite.goatcounter.com/count" src="https://gc.zgo.at/count.js"></script>`,
		},
		"goatcounter self-hosted": {
			analytics.Options{GoatCounter: &analytics.GoatCounter{Endpoint: "https://stats.example.com/count?a=1&b=2"}},
			`<script async data-goatcounter="https://stats.example.com/count?a=1&amp;b=2" src="https://gc.zgo.at/count.js"></script>`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := app(t, false, tc.opts, "")
			if got := head(t, get(a, "/").Body.String()); !strings.Contains(got, tc.want) {
				t.Errorf("head = %s\nwant %s", got, tc.want)
			}
			if code := get(a, "/collage-analytics.js").Code; code != http.StatusNotFound {
				t.Errorf("a loader is served when none is needed: %d", code)
			}
		})
	}
}

func TestConfiguredFromJSON(t *testing.T) {
	a := app(t, false, analytics.Options{}, `{"plausible": {"domain": "example.org"}}`)
	if got := head(t, get(a, "/").Body.String()); !strings.Contains(got, `data-domain="example.org"`) {
		t.Errorf("head = %s", got)
	}
}

func TestExclude(t *testing.T) {
	a := app(t, false, analytics.Options{Plausible: &analytics.Plausible{Domain: "example.com"}, Exclude: []string{"admin"}}, "")
	if strings.Contains(get(a, "/admin").Body.String(), "plausible") {
		t.Error("an excluded page has the snippet")
	}
	if !strings.Contains(get(a, "/").Body.String(), "plausible") {
		t.Error("the other page lost it")
	}
}

// In development nothing is added and nothing is served; a mistake in the
// configuration is still found.
func TestOffInDevelopment(t *testing.T) {
	a := app(t, true, analytics.Options{GoogleAnalytics: &analytics.GoogleAnalytics{MeasurementID: "G-ABC123"}, RequireConsent: true}, "")
	if body := get(a, "/").Body.String(); strings.Contains(body, "collage-analytics") || strings.Contains(body, "G-ABC123") {
		t.Errorf("snippet in development:\n%s", body)
	}
	if code := get(a, "/collage-analytics.js").Code; code != http.StatusNotFound {
		t.Errorf("loader served in development: %d", code)
	}
	if code := get(app(t, true, analytics.Options{}, ""), "/").Code; code != http.StatusServiceUnavailable {
		t.Errorf("a misconfigured plugin started in development: %d", code)
	}
}

// With consent required, the page loads only the site's own loader, which holds
// the provider back until collageAnalyticsConsent is called.
func TestConsentLoadsTheLoader(t *testing.T) {
	a := app(t, false, analytics.Options{
		Umami:          &analytics.Umami{WebsiteID: "94db1cb1-74f4-4a40-ad6c-962362670409"},
		RequireConsent: true,
		RespectDNT:     true,
	}, "")
	h := head(t, get(a, "/").Body.String())
	if !strings.Contains(h, `<script defer src="/collage-analytics.js"></script>`) || strings.Contains(h, "umami.is") {
		t.Errorf("head = %s", h)
	}
	w := get(a, "/collage-analytics.js")
	js := w.Body.String()
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("loader: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		`"dnt":true`, `"consent":true`,
		`"src":"https://cloud.umami.is/script.js"`,
		`"data-do-not-track":"true"`,
		`window.collageAnalyticsConsent = function`,
		`navigator`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("loader lacks %s:\n%s", want, js)
		}
	}
}

// Google Analytics needs its configuration call, which the loader makes, so it is
// never an inline script.
func TestGoogleAnalyticsUsesTheLoader(t *testing.T) {
	a := app(t, false, analytics.Options{GoogleAnalytics: &analytics.GoogleAnalytics{MeasurementID: "G-ABC123"}, LoaderPath: "/js/a.js"}, "")
	h := head(t, get(a, "/").Body.String())
	if !strings.Contains(h, `<script defer src="/js/a.js"></script>`) || strings.Contains(h, "gtag") {
		t.Errorf("head = %s", h)
	}
	js := get(a, "/js/a.js").Body.String()
	if !strings.Contains(js, `"ga":"G-ABC123"`) || !strings.Contains(js, "googletagmanager.com/gtag/js") || !strings.Contains(js, `"consent":false`) {
		t.Errorf("loader = %s", js)
	}
}

// Nothing a configuration holds can break out of the loader's script.
func TestLoaderEscapes(t *testing.T) {
	a := app(t, false, analytics.Options{GoatCounter: &analytics.GoatCounter{Endpoint: "https://x.example/count?</script><b>"}, RequireConsent: true}, "")
	js := get(a, "/collage-analytics.js").Body.String()
	if strings.Contains(js, "</script>") || strings.Contains(js, "<b>") {
		t.Errorf("loader = %s", js)
	}
}

// A static build carries the snippet, and the loader.
func TestStaticBuild(t *testing.T) {
	out := t.TempDir()
	a := app(t, false, analytics.Options{Plausible: &analytics.Plausible{Domain: "example.com"}, RequireConsent: true}, "")
	b, err := collage.NewBuilder(a, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil || !strings.Contains(string(page), `src="/collage-analytics.js"`) {
		t.Errorf("index.html: %v\n%s", err, page)
	}
	js, err := os.ReadFile(filepath.Join(out, "collage-analytics.js"))
	if err != nil || !strings.Contains(string(js), "plausible.io") {
		t.Errorf("loader not written: %v", err)
	}
}

func TestMisconfigurationStopsStartup(t *testing.T) {
	for name, opts := range map[string]analytics.Options{
		"no provider":           {},
		"plausible with scheme": {Plausible: &analytics.Plausible{Domain: "https://example.com"}},
		"plausible bad script":  {Plausible: &analytics.Plausible{Domain: "example.com", Script: "/js/script.js"}},
		"umami bad id":          {Umami: &analytics.Umami{WebsiteID: "abc"}},
		"goatcounter bad code":  {GoatCounter: &analytics.GoatCounter{Code: "My Site"}},
		"goatcounter endpoint":  {GoatCounter: &analytics.GoatCounter{Endpoint: "stats.example.com/count"}},
		"ga universal id":       {GoogleAnalytics: &analytics.GoogleAnalytics{MeasurementID: "UA-12345-1"}},
		"relative loader path":  {Plausible: &analytics.Plausible{Domain: "example.com"}, LoaderPath: "a.js"},
	} {
		t.Run(name, func(t *testing.T) {
			if code := get(app(t, false, opts, ""), "/").Code; code != http.StatusServiceUnavailable {
				t.Errorf("status %d, want 503", code)
			}
		})
	}
}
