package eggs

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const v2Egg = `{
  "_comment": "DO NOT EDIT: FILE GENERATED AUTOMATICALLY BY PTERODACTYL PANEL - PTERODACTYL.IO",
  "meta": {"version": "PTDL_v2", "update_url": null},
  "exported_at": "2026-01-01T00:00:00+00:00",
  "name": "Paper",
  "author": "parker@example.com",
  "description": "Minecraft Paper",
  "features": ["eula", "java_version"],
  "docker_images": {"Java 21": "ghcr.io/example/yolks:java_21", "Java 17": "ghcr.io/example/yolks:java_17"},
  "file_denylist": ["", "*.jar"],
  "startup": "java -Xms128M -jar {{SERVER_JARFILE}}",
  "config": {
    "files": "{\"server.properties\": {\"parser\": \"properties\", \"find\": {\"server-port\": \"{{server.build.default.port}}\"}}}",
    "startup": "{\"done\": \")! For help, type \"}",
    "logs": "{}",
    "stop": "stop"
  },
  "scripts": {"installation": {"script": "#!/bin/ash\necho install", "container": "alpine:3.4", "entrypoint": "ash"}},
  "variables": [
    {"name": "Server Jar File", "description": "jar", "env_variable": "SERVER_JARFILE", "default_value": "server.jar",
     "user_viewable": true, "user_editable": true, "rules": "required|string|max:20", "field_type": "text"},
    {"name": "Build", "description": "", "env_variable": "BUILD_NUMBER", "default_value": 123,
     "user_viewable": 1, "user_editable": 0, "rules": "required|string", "field_type": "text"}
  ]
}`

func TestParseV2(t *testing.T) {
	egg, err := Parse(strings.NewReader(v2Egg))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if egg.Name != "Paper" || egg.Author != "parker@example.com" || egg.Startup == "" {
		t.Errorf("basic fields wrong: %+v", egg)
	}
	if want := []string{"*.jar"}; !reflect.DeepEqual(egg.FileDenylist, want) {
		t.Errorf("FileDenylist = %v, want %v (empty entries dropped)", egg.FileDenylist, want)
	}
	if want := []DockerImage{{"Java 21", "ghcr.io/example/yolks:java_21"}, {"Java 17", "ghcr.io/example/yolks:java_17"}}; !reflect.DeepEqual(egg.DockerImages, want) {
		t.Errorf("DockerImages = %v, want %v (file order kept)", egg.DockerImages, want)
	}
	if egg.ConfigStop != "stop" || egg.ScriptContainer != "alpine:3.4" || egg.ScriptEntry != "ash" {
		t.Errorf("config or script wrong: %+v", egg)
	}

	wantVars := []Variable{
		{Name: "Server Jar File", Description: "jar", EnvVariable: "SERVER_JARFILE", DefaultValue: "server.jar", UserViewable: true, UserEditable: true, Rules: "required|string|max:20"},
		{Name: "Build", EnvVariable: "BUILD_NUMBER", DefaultValue: "123", UserViewable: true, UserEditable: false, Rules: "required|string"},
	}
	if !reflect.DeepEqual(egg.Variables, wantVars) {
		t.Errorf("Variables = %+v\nwant       %+v", egg.Variables, wantVars)
	}

	got, err := egg.DockerImagesJSON()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"Java 21":"ghcr.io/example/yolks:java_21","Java 17":"ghcr.io/example/yolks:java_17"}`; got != want {
		t.Errorf("DockerImagesJSON = %s, want %s", got, want)
	}
}

func TestParseV1(t *testing.T) {
	const v1 = `{
	  "meta": {"version": "PTDL_v1"},
	  "name": "Old", "author": "a@example.com", "description": null,
	  "image": "quay.io/example/old:latest",
	  "startup": "./run",
	  "config": {"files": "{}", "startup": "{}", "logs": "{}", "stop": "^C"},
	  "scripts": {"installation": {"script": "x", "container": "debian:buster-slim", "entrypoint": "bash"}},
	  "variables": [{"name": "V", "description": "d", "env_variable": "V", "default_value": null, "user_viewable": false, "user_editable": false, "rules": "nullable"}]
	}`

	egg, err := Parse(strings.NewReader(v1))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if want := []DockerImage{{"quay.io/example/old:latest", "quay.io/example/old:latest"}}; !reflect.DeepEqual(egg.DockerImages, want) {
		t.Errorf("DockerImages = %v, want %v", egg.DockerImages, want)
	}
	if egg.ScriptContainer != "debian:buster-slim" || egg.Variables[0].DefaultValue != "" {
		t.Errorf("unexpected egg: %+v", egg)
	}

	multi := strings.Replace(v1, `"image": "quay.io/example/old:latest"`, `"images": ["a/b:1", "c/d:2"]`, 1)
	egg, err = Parse(strings.NewReader(multi))
	if err != nil || len(egg.DockerImages) != 2 || egg.DockerImages[1].Image != "c/d:2" {
		t.Errorf("v1 with an images list: %+v, %v", egg.DockerImages, err)
	}
}

// mutate returns v2Egg with one textual replacement applied.
func mutate(t *testing.T, old, new string) string {
	t.Helper()
	if !strings.Contains(v2Egg, old) {
		t.Fatalf("test bug: %q is not in the egg", old)
	}
	return strings.Replace(v2Egg, old, new, 1)
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"not json", "not json", "not valid JSON"},
		{"empty", "", "not valid JSON"},
		{"json of the wrong shape", `[1,2,3]`, "not valid JSON"},
		{"no version", mutate(t, `"meta": {"version": "PTDL_v2", "update_url": null}`, `"meta": {}`), "not in a recognised format"},
		{"unknown version", mutate(t, `PTDL_v2`, `PTDL_v3`), "not in a recognised format"},
		{"empty name", mutate(t, `"name": "Paper"`, `"name": ""`), "name must be"},
		{"name too long", mutate(t, `"name": "Paper"`, `"name": "`+strings.Repeat("a", 192)+`"`), "name must be"},
		{"author is not an email", mutate(t, `parker@example.com`, `Parker`), "e-mail"},
		{"author with display name", mutate(t, `parker@example.com`, `Parker <parker@example.com>`), "e-mail"},
		{"no images", mutate(t, `{"Java 21": "ghcr.io/example/yolks:java_21", "Java 17": "ghcr.io/example/yolks:java_17"}`, `{}`), "between 1 and"},
		{"image with a shell metacharacter", mutate(t, `ghcr.io/example/yolks:java_21`, `x; rm -rf /`), "not a valid image name"},
		{"image values must be strings", mutate(t, `"ghcr.io/example/yolks:java_21"`, `5`), "values must be strings"},
		{"docker_images is not an object", mutate(t, `{"Java 21": "ghcr.io/example/yolks:java_21", "Java 17": "ghcr.io/example/yolks:java_17"}`, `["a"]`), "must be an object"},
		{"empty startup", mutate(t, `"startup": "java -Xms128M -jar {{SERVER_JARFILE}}"`, `"startup": "  "`), "startup command"},
		{"empty stop command", mutate(t, `"stop": "stop"`, `"stop": ""`), "stop command"},
		{"config.files is not JSON", mutate(t, `"logs": "{}"`, `"logs": "not json"`), "config.logs must be a JSON document"},
		{"reserved variable", mutate(t, `"env_variable": "SERVER_JARFILE"`, `"env_variable": "STARTUP"`), "reserved"},
		{"bad variable name", mutate(t, `"env_variable": "SERVER_JARFILE"`, `"env_variable": "BAD-NAME"`), "environment variable name"},
		{"duplicate variable", mutate(t, `"env_variable": "BUILD_NUMBER"`, `"env_variable": "SERVER_JARFILE"`), "defined twice"},
		{"variable without rules", mutate(t, `"rules": "required|string|max:20"`, `"rules": ""`), "rules are required"},
		{"default of the wrong type", mutate(t, `"default_value": 123`, `"default_value": {"a": 1}`), "not valid JSON"},
		{"flag of the wrong type", mutate(t, `"user_viewable": 1`, `"user_viewable": "yes"`), "not valid JSON"},
		{"too large", `{"x": "` + strings.Repeat("a", MaxFileSize) + `"}`, "larger than"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseDuplicateImageLabel(t *testing.T) {
	input := mutate(t, `"Java 17"`, `"Java 21"`)
	if _, err := Parse(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "used twice") {
		t.Errorf("error = %v, want a duplicate label error", err)
	}
}

// The official eggs shipped with the reference panel must all be accepted.
func TestParseReferenceEggs(t *testing.T) {
	root := filepath.Join("..", "..", "reference", "database", "Seeders", "eggs")
	if _, err := os.Stat(root); err != nil {
		t.Skip("reference/ is not present")
	}

	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".json" {
			return err
		}
		count++
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := Parse(f); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("no egg files found in the reference")
	}
	t.Logf("parsed %d reference eggs", count)
}
