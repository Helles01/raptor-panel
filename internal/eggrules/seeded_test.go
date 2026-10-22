package eggrules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Helles01/raptor-panel/internal/eggrules"
	"github.com/Helles01/raptor-panel/internal/eggs"
)

// Every rule of every seeded egg of the reference must be understood, and the default of a variable must pass its own rules
// unless it is empty (an optional variable has an empty default).
func TestSeededEggs(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "..", "reference", "database", "Seeders", "eggs", "*", "*.json"))
	if len(paths) == 0 {
		t.Skip("reference/ is not present")
	}

	variables := 0
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		egg, err := eggs.Parse(f)
		f.Close()
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}

		for _, v := range egg.Variables {
			variables++
			r, err := eggrules.Parse(v.Rules)
			if err != nil {
				t.Errorf("%s %s: %q: %v", filepath.Base(path), v.EnvVariable, v.Rules, err)
				continue
			}
			def := v.DefaultValue
			if msgs := r.Validate(eggrules.Normalize(&def), v.EnvVariable); len(msgs) != 0 && def != "" {
				t.Errorf("%s %s: the default %q does not pass its own rules %q: %q", filepath.Base(path), v.EnvVariable, def, v.Rules, msgs)
			}
		}
	}
	if variables < 70 {
		t.Errorf("only %d variables were checked", variables)
	}
}
