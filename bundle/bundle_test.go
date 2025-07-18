package bundle

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/databricks/cli/bundle/config"
	"github.com/databricks/cli/bundle/config/resources"
	"github.com/databricks/cli/bundle/env"
	"github.com/databricks/cli/internal/testutil"
	"github.com/databricks/cli/libs/dyn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadNotExists(t *testing.T) {
	b, err := Load(context.Background(), "/doesntexist")
	assert.ErrorIs(t, err, fs.ErrNotExist)
	assert.Nil(t, b)
}

func TestLoadExists(t *testing.T) {
	b, err := Load(context.Background(), "./tests/basic")
	assert.NoError(t, err)
	assert.NotNil(t, b)
}

func TestBundleCacheDir(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	f1, err := os.Create(filepath.Join(projectDir, "databricks.yml"))
	require.NoError(t, err)
	f1.Close()

	bundle, err := Load(ctx, projectDir)
	require.NoError(t, err)

	// Artificially set target.
	// This is otherwise done by [mutators.SelectTarget].
	bundle.Config.Bundle.Target = "default"

	// unset env variable in case it's set
	t.Setenv("DATABRICKS_BUNDLE_TMP", "")

	cacheDir, err := bundle.CacheDir(ctx)

	// format is <CWD>/.databricks/bundle/<target>
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(projectDir, ".databricks", "bundle", "default"), cacheDir)
}

func TestBundleCacheDirOverride(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	bundleTmpDir := t.TempDir()
	f1, err := os.Create(filepath.Join(projectDir, "databricks.yml"))
	require.NoError(t, err)
	f1.Close()

	bundle, err := Load(ctx, projectDir)
	require.NoError(t, err)

	// Artificially set target.
	// This is otherwise done by [mutators.SelectTarget].
	bundle.Config.Bundle.Target = "default"

	// now we expect to use 'bundleTmpDir' instead of CWD/.databricks/bundle
	t.Setenv("DATABRICKS_BUNDLE_TMP", bundleTmpDir)

	cacheDir, err := bundle.CacheDir(ctx)

	// format is <DATABRICKS_BUNDLE_TMP>/<target>
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(bundleTmpDir, "default"), cacheDir)
}

func TestBundleMustLoadSuccess(t *testing.T) {
	t.Setenv(env.RootVariable, "./tests/basic")
	b, err := MustLoad(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tests/basic", filepath.ToSlash(b.BundleRootPath))
}

func TestBundleMustLoadFailureWithEnv(t *testing.T) {
	t.Setenv(env.RootVariable, "./tests/doesntexist")
	_, err := MustLoad(context.Background())
	require.Error(t, err, "not a directory")
}

func TestBundleMustLoadFailureIfNotFound(t *testing.T) {
	testutil.Chdir(t, t.TempDir())
	_, err := MustLoad(context.Background())
	require.Error(t, err, "unable to find bundle root")
}

func TestBundleTryLoadSuccess(t *testing.T) {
	t.Setenv(env.RootVariable, "./tests/basic")
	b, err := TryLoad(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tests/basic", filepath.ToSlash(b.BundleRootPath))
}

func TestBundleTryLoadFailureWithEnv(t *testing.T) {
	t.Setenv(env.RootVariable, "./tests/doesntexist")
	_, err := TryLoad(context.Background())
	require.Error(t, err, "not a directory")
}

func TestBundleTryLoadOkIfNotFound(t *testing.T) {
	testutil.Chdir(t, t.TempDir())
	b, err := TryLoad(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, b)
}

func TestBundleGetResourceConfigJobsPointer(t *testing.T) {
	rootCfg := config.Root{
		Resources: config.Resources{
			Jobs: map[string]*resources.Job{
				"my_job": {
					// Empty job config is sufficient for this test.
				},
			},
		},
	}

	// Initialize the dynamic representation so GetResourceConfig can query it.
	require.NoError(t, rootCfg.Mutate(func(v dyn.Value) (dyn.Value, error) { return v, nil }))

	b := &Bundle{Config: rootCfg}

	res, ok := b.GetResourceConfig("jobs", "my_job")
	require.True(t, ok, "expected to find jobs.my_job in config")

	_, isJob := res.(*resources.Job)
	assert.True(t, isJob, "expected *resources.Job, got %T", res)

	res, ok = b.GetResourceConfig("jobs", "not_found")
	require.False(t, ok)
	require.Nil(t, res)

	res, ok = b.GetResourceConfig("not_found", "my_job")
	require.False(t, ok)
	require.Nil(t, res)
}
