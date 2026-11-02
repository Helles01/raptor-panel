package nodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.yaml.in/yaml/v3"
)

// WingsNode is the part of a node record the Wings configuration needs.
type WingsNode struct {
	UUID         string
	FQDN         string
	Scheme       string
	BehindProxy  bool
	DaemonBase   string
	DaemonSFTP   int
	DaemonListen int
	UploadSize   int64
	// AllowedMounts are the source paths of the mounts attached to the node.
	AllowedMounts []string
}

// The structs mirror Node::getConfiguration() of the reference panel, key for key, because Wings reads this file.
type wingsConfig struct {
	Debug         bool        `yaml:"debug" json:"debug"`
	UUID          string      `yaml:"uuid" json:"uuid"`
	TokenID       string      `yaml:"token_id" json:"token_id"`
	Token         string      `yaml:"token" json:"token"`
	API           wingsAPI    `yaml:"api" json:"api"`
	System        wingsSystem `yaml:"system" json:"system"`
	AllowedMounts []string    `yaml:"allowed_mounts" json:"allowed_mounts"`
	Remote        string      `yaml:"remote" json:"remote"`
}

type wingsAPI struct {
	Host        string   `yaml:"host" json:"host"`
	Port        int      `yaml:"port" json:"port"`
	SSL         wingsSSL `yaml:"ssl" json:"ssl"`
	UploadLimit int64    `yaml:"upload_limit" json:"upload_limit"`
}

type wingsSSL struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Cert    string `yaml:"cert" json:"cert"`
	Key     string `yaml:"key" json:"key"`
}

type wingsSystem struct {
	Data string    `yaml:"data" json:"data"`
	SFTP wingsSFTP `yaml:"sftp" json:"sftp"`
}

type wingsSFTP struct {
	BindPort int `yaml:"bind_port" json:"bind_port"`
}

// WingsConfig renders the config.yml a node's Wings needs: its identity and token, how to listen, and where the panel
// is (remote, for example https://panel.example.com). The file contains the node token, so it is secret.
func WingsConfig(n WingsNode, creds Credentials, remote string) ([]byte, error) {
	cfg, err := buildWingsConfig(n, creds, remote)
	if err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("render Wings configuration: %w", err)
	}
	return out, nil
}

// WingsConfigJSON is the same configuration as JSON, which is what the node-configuration route of the Application API answers with and what
// the panel sends a node that is running when its settings change. It holds the node token as well.
func WingsConfigJSON(n WingsNode, creds Credentials, remote string) ([]byte, error) {
	cfg, err := buildWingsConfig(n, creds, remote)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("render Wings configuration: %w", err)
	}
	return out, nil
}

func buildWingsConfig(n WingsNode, creds Credentials, remote string) (wingsConfig, error) {
	u, err := url.Parse(remote)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return wingsConfig{}, errors.New("remote must be an http(s) URL without credentials, for example https://panel.example.com")
	}
	if creds.TokenID == "" || creds.Token == "" {
		return wingsConfig{}, errors.New("node credentials are missing")
	}

	mounts := n.AllowedMounts
	if mounts == nil {
		mounts = []string{}
	}
	host := strings.ToLower(n.FQDN)

	return wingsConfig{
		UUID:    n.UUID,
		TokenID: creds.TokenID,
		Token:   creds.Token,
		API: wingsAPI{
			Host: "0.0.0.0",
			Port: n.DaemonListen,
			SSL: wingsSSL{
				Enabled: !n.BehindProxy && n.Scheme == "https",
				Cert:    "/etc/letsencrypt/live/" + host + "/fullchain.pem",
				Key:     "/etc/letsencrypt/live/" + host + "/privkey.pem",
			},
			UploadLimit: n.UploadSize,
		},
		System:        wingsSystem{Data: n.DaemonBase, SFTP: wingsSFTP{BindPort: n.DaemonSFTP}},
		AllowedMounts: mounts,
		Remote:        strings.TrimRight(remote, "/"),
	}, nil
}
