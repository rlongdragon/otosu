package server

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
)

//go:embed install.sh.tmpl
var installTmpl string

var installer = template.Must(template.New("install.sh").Parse(installTmpl))

// renderInstaller produces the setup script with this server's URL baked in,
// which is what makes installing on a new dev box a single paste with nothing
// to fill in.
func renderInstaller(baseURL string) ([]byte, error) {
	var buf bytes.Buffer
	if err := installer.Execute(&buf, struct{ BaseURL string }{baseURL}); err != nil {
		return nil, fmt.Errorf("server: render installer: %w", err)
	}
	return buf.Bytes(), nil
}
