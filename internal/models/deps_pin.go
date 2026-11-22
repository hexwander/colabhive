//go:build tools

// Package models pins pure-Go workspace dependencies so `go mod tidy` preserves
// all canonical backend libraries before downstream subpackages land.
package models

import (
	_ "github.com/google/uuid"
	_ "github.com/gorilla/websocket"
	_ "github.com/pelletier/go-toml/v2"
	_ "golang.org/x/net/proxy"
	_ "gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)
