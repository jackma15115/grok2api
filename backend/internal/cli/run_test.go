package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigPathFindsRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	backendDir := filepath.Join(root, "backend")
	if err := os.Mkdir(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("server: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(backendDir)

	if path := defaultConfigPath(); path != filepath.Join("..", "config.yaml") {
		t.Fatalf("defaultConfigPath() = %q", path)
	}
}

func TestParseOptionsSupportsContainerListenOverride(t *testing.T) {
	options, err := parseOptions([]string{"--config", "/app/config.yaml", "--listen", "0.0.0.0:8000"})
	if err != nil {
		t.Fatal(err)
	}
	if options.configPath != "/app/config.yaml" || options.listen != "0.0.0.0:8000" {
		t.Fatalf("options = %#v", options)
	}
	if _, err := parseOptions([]string{"--listen"}); err == nil {
		t.Fatal("missing --listen value was accepted")
	}
}

func TestParseOptionsRecognizesSQLiteMigrationCommand(t *testing.T) {
	options, err := parseOptions([]string{"migrate-sqlite-to-postgres", "--config", "/app/config.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if options.command != "migrate-sqlite-to-postgres" || options.configPath != "/app/config.yaml" {
		t.Fatalf("migration options = %#v", options)
	}

	serve, err := parseOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if serve.command != "serve" {
		t.Fatalf("default command = %q", serve.command)
	}
}
