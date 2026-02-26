package provision

import (
	"bytes"
	_ "embed"
	"text/template"
)

//go:embed embed/cloud-init.yaml.tmpl
var cloudInitTemplate string

// CloudInitOpts holds per-instance values for cloud-init template rendering.
type CloudInitOpts struct {
	Username  string
	UID       int
	GID       int
	Sudo      bool
	Docker    bool
	DevTools  bool
	SudoGroup string // "sudo" on Ubuntu, "wheel" on Alpine
}

var cloudInitTmpl = template.Must(template.New("cloud-init").Parse(cloudInitTemplate))

// RenderCloudInit renders the cloud-init user-data template for the given opts.
// The returned string starts with the required "#cloud-config\n" header.
func RenderCloudInit(opts CloudInitOpts) (string, error) {
	var buf bytes.Buffer
	if err := cloudInitTmpl.Execute(&buf, opts); err != nil {
		return "", err
	}
	return buf.String(), nil
}
