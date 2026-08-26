package sites

import "html/template"
import "bytes"

// placeholderTemplate is the landing page a new site serves until something is
// deployed to it.
//
// html/template escapes the domain on the way in. It is already validated to
// be a hostname, but escaping costs nothing and keeps this correct if that
// validation is ever relaxed.
var placeholderTemplate = template.Must(template.New("placeholder").Parse(
	`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{ . }}</title>
<style>
  body { font-family: system-ui, -apple-system, sans-serif; margin: 0;
         min-height: 100vh; display: grid; place-items: center;
         background: #f8fafc; color: #0f172a; }
  main { text-align: center; padding: 2rem; }
  h1 { font-size: 1.25rem; margin: 0 0 .5rem; }
  p { color: #64748b; margin: 0; font-size: .875rem; }
</style>
</head>
<body>
<main>
  <h1>{{ . }}</h1>
  <p>This site is ready. Upload your content to replace this page.</p>
</main>
</body>
</html>
`))

// placeholderPage renders the landing page for a domain.
func placeholderPage(domain string) string {
	var out bytes.Buffer
	if err := placeholderTemplate.Execute(&out, domain); err != nil {
		// The template is a compile-time constant with one string field, so a
		// failure here is not recoverable input-dependent behaviour. A plain
		// fallback keeps the site serving something.
		return "<!doctype html><title>Site ready</title><p>This site is ready.</p>"
	}
	return out.String()
}
