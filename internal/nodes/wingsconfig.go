package nodes

import (
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
	Debug         bool        `yaml:"debug"`
	UUID          string      `yaml:"uuid"`
	TokenID       string      `yaml:"token_id"`
	Token         string      `yaml:"token"`
	API           wingsAPI    `yaml:"api"`
	System        wingsSystem `yaml:"system"`
	AllowedMounts []string    `yaml:"allowed_mounts"`
	Remote        string      `yaml:"remote"`
}

type wingsAPI struct {
	Host        string   `yaml:"host"`
	Port        int      `yaml:"port"`
	SSL         wingsSSL `yaml:"ssl"`
	UploadLimit int64    `yaml:"upload_limit"`
}

type wingsSSL struct {
	Enabled bool   `yaml:"enabled"`
	Cert    string `yaml:"cert"`
	Key     string `yaml:"key"`
}

type wingsSystem struct {
	Data string    `yaml:"data"`
	SFTP wingsSFTP `yaml:"sftp"`
}

type wingsSFTP struct {
	BindPort int `yaml:"bind_port"`
}

// WingsConfig renders the config.yml a node's Wings needs: its identity and token, how to listen, and where the panel
// is (remote, for example https://panel.example.com). The file contains the node token, so it is secret.
func WingsConfig(n WingsNode, creds Credentials, remote string) ([]byte, error) {
	u, err := url.Parse(remote)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("remote must be an http(s) URL without credentials, for example https://panel.example.com")
	}
	if creds.TokenID == "" || creds.Token == "" {
		return nil, errors.New("node credentials are missing")
	}

	mounts := n.AllowedMounts
	if mounts == nil {
		mounts = []string{}
	}
	host := strings.ToLower(n.FQDN)

	out, err := yaml.Marshal(wingsConfig{
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
	})
	if err != nil {
		return nil, fmt.Errorf("render Wings configuration: %w", err)
	}
	return out, nil
}
