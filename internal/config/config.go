// Package config holds runtime paths and addresses for docker-cp.
package config

import "os"

type Config struct {
	Listen        string
	DataDir       string
	StacksDir     string
	CaddyAdmin    string
	CaddyfilePath string
	DockerSock    string
	DockerConfig  string
}

func Default() Config {
	return Config{
		Listen:        ":8800",
		DataDir:       "/opt/docker-cp/data",
		StacksDir:     "/opt/docker-cp/stacks",
		CaddyAdmin:    "http://127.0.0.1:2019",
		CaddyfilePath: "/etc/caddy/Caddyfile",
		DockerSock:    "/var/run/docker.sock",
		DockerConfig:  "/root/.docker/config.json",
	}
}

// FromEnv overrides base with DCP_* variables; empty values are ignored.
func FromEnv(base Config) Config {
	set := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	set(&base.Listen, "DCP_LISTEN")
	set(&base.DataDir, "DCP_DATA_DIR")
	set(&base.StacksDir, "DCP_STACKS_DIR")
	set(&base.CaddyAdmin, "DCP_CADDY_ADMIN")
	set(&base.CaddyfilePath, "DCP_CADDYFILE")
	set(&base.DockerSock, "DCP_DOCKER_SOCK")
	set(&base.DockerConfig, "DCP_DOCKER_CONFIG")
	return base
}

func (c Config) StatePath() string { return c.DataDir + "/state.json" }
