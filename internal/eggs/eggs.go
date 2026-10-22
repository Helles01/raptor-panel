// Package eggs reads egg files (the PTDL_v1 and PTDL_v2 JSON formats of the reference panel) and validates them.
//
// An egg describes how to run one kind of game server: Docker images, the startup command, how Wings should
// configure and stop it, an install script and its variables. Egg files come from third parties, so everything in them
// is checked against allow-lists before it can be stored.
package eggs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/eggrules"
)

// MaxFileSize is the largest egg file that is read.
const MaxFileSize = 5 << 20

const (
	maxField     = 191
	maxVariables = 200
	maxImages    = 50
	maxFeatures  = 50
	maxDenylist  = 500
)

var (
	imagePattern   = regexp.MustCompile(`^[\w#\./\- ]*\|?~?[\w\./\-:@ ]*$`)
	envNamePattern = regexp.MustCompile(`^\w{1,191}$`)
)

// reservedEnvNames can never be egg variables because the panel sets them itself.
var reservedEnvNames = map[string]bool{
	"SERVER_MEMORY": true, "SERVER_IP": true, "SERVER_PORT": true, "ENV": true, "HOME": true,
	"USER": true, "STARTUP": true, "SERVER_UUID": true, "UUID": true,
}

// Egg is a validated egg.
type Egg struct {
	Name         string
	Author       string
	Description  string
	Features     []string
	DockerImages []DockerImage // in file order: the first one is the default
	FileDenylist []string
	UpdateURL    string

	// The four config values are JSON documents (stop is a plain command), as the Wings process configuration expects.
	ConfigFiles   string
	ConfigStartup string
	ConfigLogs    string
	ConfigStop    string

	Startup string

	ScriptInstall   string
	ScriptContainer string
	ScriptEntry     string

	Variables []Variable
}

// DockerImage is a label shown to users and the image it stands for.
type DockerImage struct {
	Label string
	Image string
}

// Variable is an environment variable of the egg.
type Variable struct {
	Name         string
	Description  string
	EnvVariable  string
	DefaultValue string
	UserViewable bool
	UserEditable bool
	Rules        string
}

// DockerImagesJSON returns the images as the JSON object the panel stores, keeping the order.
func (e Egg) DockerImagesJSON() (string, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, img := range e.DockerImages {
		if i > 0 {
			b.WriteByte(',')
		}
		label, err := json.Marshal(img.Label)
		if err != nil {
			return "", err
		}
		image, err := json.Marshal(img.Image)
		if err != nil {
			return "", err
		}
		b.Write(label)
		b.WriteByte(':')
		b.Write(image)
	}
	b.WriteByte('}')
	return b.String(), nil
}

// Parse reads and validates an egg file. Any problem is an error; nothing is guessed or defaulted except the install
// container (alpine:3.4) and entrypoint (ash), which are the database defaults of the reference panel.
func Parse(r io.Reader) (Egg, error) {
	raw, err := io.ReadAll(io.LimitReader(r, MaxFileSize+1))
	if err != nil {
		return Egg{}, fmt.Errorf("read egg file: %w", err)
	}
	if len(raw) > MaxFileSize {
		return Egg{}, fmt.Errorf("egg file is larger than %d MB", MaxFileSize>>20)
	}

	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return Egg{}, fmt.Errorf("the egg file is not valid JSON of the expected shape: %w", err)
	}
	if f.Meta.Version != "PTDL_v1" && f.Meta.Version != "PTDL_v2" {
		return Egg{}, errors.New("the egg file is not in a recognised format (meta.version must be PTDL_v1 or PTDL_v2)")
	}

	egg := Egg{
		Name:            f.Name,
		Author:          f.Author,
		Description:     f.Description,
		Features:        f.Features,
		FileDenylist:    nonEmpty(f.FileDenylist),
		UpdateURL:       f.Meta.UpdateURL,
		ConfigFiles:     f.Config.Files,
		ConfigStartup:   f.Config.Startup,
		ConfigLogs:      f.Config.Logs,
		ConfigStop:      f.Config.Stop,
		Startup:         f.Startup,
		ScriptInstall:   f.Scripts.Installation.Script,
		ScriptContainer: f.Scripts.Installation.Container,
		ScriptEntry:     f.Scripts.Installation.Entrypoint,
	}

	// PTDL_v1 has a single image (or a plain list); PTDL_v2 has labelled images.
	switch {
	case f.Meta.Version == "PTDL_v2":
		egg.DockerImages = f.DockerImages
	case len(f.Images) > 0:
		for _, image := range f.Images {
			egg.DockerImages = append(egg.DockerImages, DockerImage{Label: image, Image: image})
		}
	case f.Image != "":
		egg.DockerImages = []DockerImage{{Label: f.Image, Image: f.Image}}
	}

	for _, v := range f.Variables {
		egg.Variables = append(egg.Variables, Variable{
			Name:         v.Name,
			Description:  v.Description,
			EnvVariable:  v.EnvVariable,
			DefaultValue: string(v.DefaultValue),
			UserViewable: bool(v.UserViewable),
			UserEditable: bool(v.UserEditable),
			Rules:        v.Rules,
		})
	}

	if egg.ScriptContainer == "" {
		egg.ScriptContainer = "alpine:3.4"
	}
	if egg.ScriptEntry == "" {
		egg.ScriptEntry = "ash"
	}

	if err := egg.Validate(); err != nil {
		return Egg{}, err
	}
	return egg, nil
}

// Validate checks the egg against the rules of the reference panel's Egg and EggVariable models.
func (e Egg) Validate() error {
	if err := textBetween("name", e.Name, 1, maxField); err != nil {
		return err
	}
	if addr, err := mail.ParseAddress(e.Author); err != nil || addr.Address != e.Author {
		return errors.New("author must be an e-mail address")
	}
	if len(e.Features) > maxFeatures {
		return fmt.Errorf("an egg can have at most %d features", maxFeatures)
	}
	for _, feature := range e.Features {
		if err := textBetween("feature", feature, 1, maxField); err != nil {
			return err
		}
	}
	if len(e.FileDenylist) > maxDenylist {
		return fmt.Errorf("the file denylist can have at most %d entries", maxDenylist)
	}

	if len(e.DockerImages) < 1 || len(e.DockerImages) > maxImages {
		return fmt.Errorf("an egg needs between 1 and %d docker images", maxImages)
	}
	labels := map[string]bool{}
	for _, img := range e.DockerImages {
		if err := textBetween("docker image label", img.Label, 1, maxField); err != nil {
			return err
		}
		if labels[img.Label] {
			return fmt.Errorf("docker image label %q is used twice", img.Label)
		}
		labels[img.Label] = true
		if n := utf8.RuneCountInString(img.Image); n < 1 || n > maxField || !imagePattern.MatchString(img.Image) {
			return fmt.Errorf("docker image %q is not a valid image name", img.Image)
		}
	}

	if strings.TrimSpace(e.Startup) == "" {
		return errors.New("startup command is required")
	}
	if err := textBetween("stop command", e.ConfigStop, 1, maxField); err != nil {
		return err
	}
	for name, value := range map[string]string{"config.files": e.ConfigFiles, "config.startup": e.ConfigStartup, "config.logs": e.ConfigLogs} {
		if !json.Valid([]byte(value)) {
			return fmt.Errorf("%s must be a JSON document", name)
		}
	}
	if err := textBetween("install container", e.ScriptContainer, 1, maxField); err != nil {
		return err
	}
	if err := textBetween("install entrypoint", e.ScriptEntry, 1, maxField); err != nil {
		return err
	}

	if len(e.Variables) > maxVariables {
		return fmt.Errorf("an egg can have at most %d variables", maxVariables)
	}
	env := map[string]bool{}
	for _, v := range e.Variables {
		if err := textBetween("variable name", v.Name, 1, maxField); err != nil {
			return err
		}
		if !envNamePattern.MatchString(v.EnvVariable) {
			return fmt.Errorf("variable %q: environment variable name must be 1 to 191 letters, digits or underscores", v.Name)
		}
		if reservedEnvNames[v.EnvVariable] {
			return fmt.Errorf("variable %q: %s is reserved", v.Name, v.EnvVariable)
		}
		if env[v.EnvVariable] {
			return fmt.Errorf("environment variable %s is defined twice", v.EnvVariable)
		}
		env[v.EnvVariable] = true
		if strings.TrimSpace(v.Rules) == "" {
			return fmt.Errorf("variable %q: rules are required", v.Name)
		}
		// Rules that cannot be applied would leave a variable with nothing to check its values against.
		if _, err := eggrules.Parse(v.Rules); err != nil {
			return fmt.Errorf("variable %q: %w", v.Name, err)
		}
	}
	return nil
}

func textBetween(what, s string, min, max int) error {
	if n := utf8.RuneCountInString(s); n < min || n > max {
		return fmt.Errorf("%s must be %d to %d characters", what, min, max)
	}
	if strings.ContainsRune(s, 0) {
		return fmt.Errorf("%s must not contain a NUL character", what)
	}
	return nil
}

func nonEmpty(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// file is the JSON shape shared by both versions.
type file struct {
	Meta struct {
		Version   string `json:"version"`
		UpdateURL string `json:"update_url"`
	} `json:"meta"`
	Name         string        `json:"name"`
	Author       string        `json:"author"`
	Description  string        `json:"description"`
	Features     []string      `json:"features"`
	DockerImages dockerImages  `json:"docker_images"`
	Image        string        `json:"image"`
	Images       []string      `json:"images"`
	FileDenylist []string      `json:"file_denylist"`
	Startup      string        `json:"startup"`
	Config       fileConfig    `json:"config"`
	Scripts      fileScripts   `json:"scripts"`
	Variables    []fileVarItem `json:"variables"`
}

type fileConfig struct {
	Files   string `json:"files"`
	Startup string `json:"startup"`
	Logs    string `json:"logs"`
	Stop    string `json:"stop"`
}

type fileScripts struct {
	Installation struct {
		Script     string `json:"script"`
		Container  string `json:"container"`
		Entrypoint string `json:"entrypoint"`
	} `json:"installation"`
}

type fileVarItem struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	EnvVariable  string     `json:"env_variable"`
	DefaultValue flexString `json:"default_value"`
	UserViewable flexBool   `json:"user_viewable"`
	UserEditable flexBool   `json:"user_editable"`
	Rules        string     `json:"rules"`
}

// dockerImages reads a JSON object of label -> image and keeps the order of the file.
type dockerImages []DockerImage

func (d *dockerImages) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("docker_images must be an object")
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		var image string
		if err := dec.Decode(&image); err != nil {
			return errors.New("docker_images values must be strings")
		}
		*d = append(*d, DockerImage{Label: key.(string), Image: image})
	}
	_, err := dec.Token()
	return err
}

// flexString accepts a JSON string, number, boolean or null, as egg files in the wild use all of them for defaults.
type flexString string

func (s *flexString) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	switch {
	case trimmed == "null":
		*s = ""
	case strings.HasPrefix(trimmed, `"`):
		var v string
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*s = flexString(v)
	case trimmed == "true" || trimmed == "false" || isNumber(trimmed):
		*s = flexString(trimmed)
	default:
		return errors.New("a variable default must be a string, number, boolean or null")
	}
	return nil
}

func isNumber(s string) bool {
	var n json.Number
	return json.Unmarshal([]byte(s), &n) == nil && s != "" && s[0] != '"'
}

// flexBool accepts true/false and 0/1.
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "true", "1", `"1"`:
		*b = true
	case "false", "0", `"0"`, "null":
		*b = false
	default:
		return errors.New("a flag must be true, false, 1 or 0")
	}
	return nil
}
