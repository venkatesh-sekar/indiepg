//go:build e2e

package harness

import (
	"path/filepath"
	"runtime"
)

// Fixed, deterministic stack parameters. These MUST agree with
// docker/build-preinstalled.sh (the admin password) and docker/compose.yaml
// (creds + bucket + image names).
const (
	// AdminPassword is the fixed admin password baked into the preinstalled image
	// by build-preinstalled.sh. Non-install scenarios log in with it.
	AdminPassword = "E2eTestAdminPassword-v1"

	// BaseImage runs a real install from scratch (scenario 1).
	BaseImage = "indiepg-e2e-base:latest"
	// PreinstalledImage starts from a provisioned cluster + enabled panel unit.
	PreinstalledImage = "indiepg-e2e-preinstalled:latest"

	// MinIO credentials + bucket (compose defaults).
	MinIOAccessKey = "e2eaccess"
	MinIOSecretKey = "e2esecretkey"
	MinIOBucket    = "indiepg-backups"

	// MinIOEndpointInternal is how the PANEL (inside the compose network) reaches
	// MinIO: plain host on the default S3 port, path-style, TLS verified against the
	// CA baked into the panel image. This is the endpoint a backup scenario hands to
	// the panel via Panel.ConfigureS3.
	MinIOEndpointInternal = "minio"

	// panelPort / minioPort are the in-container ports the compose stack publishes
	// to ephemeral host ports.
	panelContainerPort = "8443"
	minioContainerPort = "443"
)

// PanelImage selects which image the panel service runs.
type PanelImage int

const (
	// ImagePreinstalled (default) — provisioned cluster + enabled panel unit.
	ImagePreinstalled PanelImage = iota
	// ImageBase — a bare systemd box for a real install-from-scratch.
	ImageBase
)

func (i PanelImage) ref() string {
	if i == ImageBase {
		return BaseImage
	}
	return PreinstalledImage
}

// dockerDir returns the absolute path to test/e2e/docker (compose file + certs),
// resolved relative to this source file so it works from any working directory.
func dockerDir() string {
	_, self, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "docker"))
}

func composeFile() string { return filepath.Join(dockerDir(), "compose.yaml") }
func caCertPath() string  { return filepath.Join(dockerDir(), "certs", "ca.crt") }
