package pg

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogPackageName(t *testing.T) {
	t.Run("versioned template fills the major", func(t *testing.T) {
		e, ok := LookupCatalog("vector")
		require.True(t, ok)
		require.Equal(t, "postgresql-17-pgvector", e.PackageName(17))
	})
	t.Run("postgis is versioned", func(t *testing.T) {
		e, ok := LookupCatalog("postgis")
		require.True(t, ok)
		require.Equal(t, "postgresql-16-postgis-3", e.PackageName(16))
	})
	t.Run("contrib package has no version verb", func(t *testing.T) {
		e, ok := LookupCatalog("hstore")
		require.True(t, ok)
		require.Equal(t, contribPackage, e.PackageName(17))
	})
}

func TestCatalogPreloadFlags(t *testing.T) {
	preload := map[string]string{
		"pg_cron":            "pg_cron",
		"pg_stat_statements": "pg_stat_statements",
	}
	for _, e := range Catalog {
		t.Run(e.Name, func(t *testing.T) {
			require.NotEmpty(t, e.Name)
			require.NotEmpty(t, e.Description)
			require.NotEmpty(t, e.PackageTemplate)
			if lib, ok := preload[e.Name]; ok {
				require.True(t, e.RequiresPreload, "expected RequiresPreload for %s", e.Name)
				require.Equal(t, lib, e.PreloadLib)
			} else {
				require.False(t, e.RequiresPreload, "unexpected RequiresPreload for %s", e.Name)
				require.Empty(t, e.PreloadLib)
			}
		})
	}
}

func TestLookupCatalogMiss(t *testing.T) {
	_, ok := LookupCatalog("not_a_real_extension")
	require.False(t, ok)
}
