package config_test

import (
	"testing"

	"docker-cp/internal/config"
)

func TestDefault(t *testing.T) {
	c := config.Default()
	want := config.Config{Listen: ":8800", DataDir: "/opt/docker-cp/data", StacksDir: "/opt/docker-cp/stacks",
		CaddyAdmin: "http://127.0.0.1:2019", CaddyfilePath: "/etc/caddy/Caddyfile",
		DockerSock: "/var/run/docker.sock", DockerConfig: "/root/.docker/config.json"}
	if c != want {
		t.Fatalf("got %+v", c)
	}
	if c.StatePath() != "/opt/docker-cp/data/state.json" {
		t.Fatal(c.StatePath())
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("DCP_DATA_DIR", "/tmp/x")
	t.Setenv("DCP_LISTEN", "127.0.0.1:9000")
	t.Setenv("DCP_STACKS_DIR", "")
	c := config.FromEnv(config.Default())
	if c.DataDir != "/tmp/x" || c.Listen != "127.0.0.1:9000" || c.StacksDir != "/opt/docker-cp/stacks" {
		t.Fatalf("%+v", c)
	}
}
