package backup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func baseS3Params() ConfigParams {
	return ConfigParams{
		Stanza:        "main",
		Endpoint:      "s3.us-west-002.backblazeb2.com",
		Region:        "us-west-002",
		Bucket:        "my-backups",
		Prefix:        "panel/inst-123",
		AccessKey:     "AKID",
		SecretKey:     "s3cr3t/with+base64=chars",
		UseSSL:        true,
		RetentionDays: 14,
		PGDataDir:     "/var/lib/postgresql/16/main",
		PGPort:        "5432",
		PGSocketDir:   "/var/run/postgresql",
	}
}

func TestRenderConfig_S3(t *testing.T) {
	out, err := RenderConfig(baseS3Params())
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(out, configMarker), "must start with managed marker")
	require.True(t, HasManagedMarker(out))

	for _, want := range []string{
		"[global]",
		"repo1-type=s3",
		"repo1-path=/panel/inst-123",
		"repo1-s3-bucket=my-backups",
		"repo1-s3-endpoint=s3.us-west-002.backblazeb2.com",
		"repo1-s3-region=us-west-002",
		"repo1-s3-key=AKID",
		"repo1-s3-key-secret=s3cr3t/with+base64=chars",
		"repo1-s3-uri-style=host",
		"repo1-retention-full-type=time",
		"repo1-retention-full=14",
		"repo1-cipher-type=none",
		"[main]",
		"pg1-path=/var/lib/postgresql/16/main",
		"pg1-port=5432",
		"pg1-socket-path=/var/run/postgresql",
	} {
		require.Contains(t, out, want)
	}
	// No TLS-weakening line when UseSSL is true.
	require.NotContains(t, out, "repo1-storage-verify-tls")
}

func TestRenderConfig_BundlesSmallFiles(t *testing.T) {
	// repo-bundle packs the many small relation/catalog files a Postgres cluster
	// is made of into a few large repo objects, so an S3 backup is not bottlenecked
	// on one HTTP round-trip per tiny file. It is unconditional (helps posix too).
	out, err := RenderConfig(baseS3Params())
	require.NoError(t, err)
	require.Contains(t, out, "repo-bundle=y")

	p := baseS3Params()
	p.Bucket, p.Endpoint = "", ""
	local, err := RenderConfig(p)
	require.NoError(t, err)
	require.Contains(t, local, "repo-bundle=y", "bundling applies to local repos too")
}

func TestRenderConfig_ProcessMaxRenderedOnlyWhenSet(t *testing.T) {
	// ProcessMax controls parallel upload workers; it is rendered only when set so
	// RenderConfig stays pure (CPU detection happens at the call boundary).
	p := baseS3Params()
	p.ProcessMax = 4
	out, err := RenderConfig(p)
	require.NoError(t, err)
	require.Contains(t, out, "process-max=4")

	p.ProcessMax = 0
	out, err = RenderConfig(p)
	require.NoError(t, err)
	require.NotContains(t, out, "process-max=")
}

func TestDefaultProcessMax_BoundedByCores(t *testing.T) {
	// Auto-sized from CPU count, but clamped to [1,4] so a tiny indie box never
	// starves Postgres and a big box does not spawn excessive S3 workers.
	got := DefaultProcessMax()
	require.GreaterOrEqual(t, got, 1)
	require.LessOrEqual(t, got, 4)
}

func TestRenderConfig_LocalWhenNoBucket(t *testing.T) {
	p := baseS3Params()
	p.Bucket = ""
	p.Endpoint = ""
	out, err := RenderConfig(p)
	require.NoError(t, err)
	require.Contains(t, out, "repo1-type=posix")
	require.Contains(t, out, "repo1-path="+defaultLocalRepoPath)
	require.NotContains(t, out, "repo1-s3-")
}

func TestRenderConfig_CipherEnablesEncryption(t *testing.T) {
	p := baseS3Params()
	p.CipherPass = "a-very-long-passphrase"
	out, err := RenderConfig(p)
	require.NoError(t, err)
	require.Contains(t, out, "repo1-cipher-type=aes-256-cbc")
	require.Contains(t, out, "repo1-cipher-pass=a-very-long-passphrase")
	require.NotContains(t, out, "repo1-cipher-type=none")
}

func TestRenderConfig_NoSSLWeakensVerifyExplicitly(t *testing.T) {
	p := baseS3Params()
	p.UseSSL = false
	out, err := RenderConfig(p)
	require.NoError(t, err)
	require.Contains(t, out, "repo1-storage-verify-tls=n")
}

func TestRenderConfig_PathURIStyle(t *testing.T) {
	p := baseS3Params()
	p.URIStyle = "path"
	out, err := RenderConfig(p)
	require.NoError(t, err)
	require.Contains(t, out, "repo1-s3-uri-style=path")
}

func TestRenderConfig_EmptyPrefixRootPath(t *testing.T) {
	p := baseS3Params()
	p.Prefix = ""
	out, err := RenderConfig(p)
	require.NoError(t, err)
	require.Contains(t, out, "repo1-path=/\n")
}

func TestRenderConfig_ZeroRetentionOmitsRetention(t *testing.T) {
	p := baseS3Params()
	p.RetentionDays = 0
	out, err := RenderConfig(p)
	require.NoError(t, err)
	require.NotContains(t, out, "repo1-retention-full")
}

// TestRenderConfig_RejectsInjection is the security-critical case: a newline (or
// any control char) in ANY interpolated value must be rejected, because the INI
// value runs to end-of-line — a newline would inject an arbitrary option.
func TestRenderConfig_RejectsInjection(t *testing.T) {
	inject := "evil\nrepo1-s3-key-secret=attacker"
	fields := map[string]func(*ConfigParams){
		"secret":      func(p *ConfigParams) { p.SecretKey = inject },
		"access":      func(p *ConfigParams) { p.AccessKey = inject },
		"bucket":      func(p *ConfigParams) { p.Bucket = inject },
		"endpoint":    func(p *ConfigParams) { p.Endpoint = inject },
		"region":      func(p *ConfigParams) { p.Region = inject },
		"prefix":      func(p *ConfigParams) { p.Prefix = inject },
		"cipher":      func(p *ConfigParams) { p.CipherPass = inject },
		"datadir":     func(p *ConfigParams) { p.PGDataDir = inject },
		"pgport":      func(p *ConfigParams) { p.PGPort = inject },
		"pgsocketdir": func(p *ConfigParams) { p.PGSocketDir = inject },
	}
	for name, mutate := range fields {
		t.Run(name, func(t *testing.T) {
			p := baseS3Params()
			mutate(&p)
			_, err := RenderConfig(p)
			require.Error(t, err, "control char in %s must be rejected", name)
			require.Equal(t, core.CodeValidation, core.CodeOf(err))
		})
	}
}

func TestRenderConfig_RejectsCarriageReturnAndNUL(t *testing.T) {
	for _, bad := range []string{"a\rb", "a\x00b", "a\tb"} {
		p := baseS3Params()
		p.Bucket = bad
		_, err := RenderConfig(p)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	}
}

func TestRenderConfig_RejectsUnicodeLineSeparators(t *testing.T) {
	for _, bad := range []string{"a\u2028b", "a\u2029b", "a\u0085b"} {
		p := baseS3Params()
		p.SecretKey = bad
		_, err := RenderConfig(p)
		require.Error(t, err, "Unicode line/paragraph/NEL separators must be rejected: %q", bad)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	}
}

func TestRenderConfig_TrimsStructuralLocatorsButNotSecret(t *testing.T) {
	p := baseS3Params()
	p.Bucket = "  my-backups  "
	p.Endpoint = " s3.example.com "
	p.Region = " us-east-1 "
	p.AccessKey = " AKID "
	p.SecretKey = "secret-with-trailing-space " // a secret may legitimately end in space
	out, err := RenderConfig(p)
	require.NoError(t, err)
	require.Contains(t, out, "repo1-s3-bucket=my-backups\n")
	require.Contains(t, out, "repo1-s3-endpoint=s3.example.com\n")
	require.Contains(t, out, "repo1-s3-region=us-east-1\n")
	require.Contains(t, out, "repo1-s3-key=AKID\n")
	// The secret is preserved verbatim (only embedded control chars are rejected).
	require.Contains(t, out, "repo1-s3-key-secret=secret-with-trailing-space \n")
}

func TestHasManagedMarker_FirstLineOnly(t *testing.T) {
	managed, err := RenderConfig(baseS3Params())
	require.NoError(t, err)
	require.True(t, HasManagedMarker(managed), "our own output is managed")

	// A foreign file that merely quotes the marker mid-file is NOT managed.
	foreign := "[global]\nrepo1-type=s3\n# I copied this from docs: " + configMarker + "\n"
	require.False(t, HasManagedMarker(foreign), "marker buried mid-file must not count as managed")
}

func TestRenderConfig_RejectsBadStanza(t *testing.T) {
	p := baseS3Params()
	p.Stanza = "Bad Stanza!"
	_, err := RenderConfig(p)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestRenderConfig_Deterministic(t *testing.T) {
	a, err := RenderConfig(baseS3Params())
	require.NoError(t, err)
	b, err := RenderConfig(baseS3Params())
	require.NoError(t, err)
	require.Equal(t, a, b, "identical input must render byte-identical output")
}
