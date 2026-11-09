package config

import "testing"

func TestWebReload(t *testing.T) {
	cfg, err := Load("", env(nil))
	if err != nil || cfg.Web.Reload {
		t.Fatalf("the default: %v, %v", cfg.Web.Reload, err)
	}
	cfg, err = Load(writeFile(t, "web:\n  reload: true\n"), env(nil))
	if err != nil || !cfg.Web.Reload {
		t.Errorf("from the file: %v, %v", cfg.Web.Reload, err)
	}
	// The environment wins over the file, both ways.
	cfg, err = Load(writeFile(t, "web:\n  reload: true\n"), env(map[string]string{"RAPTOR_WEB_RELOAD": "false"}))
	if err != nil || cfg.Web.Reload {
		t.Errorf("the environment turns it off: %v, %v", cfg.Web.Reload, err)
	}
	cfg, err = Load("", env(map[string]string{"RAPTOR_WEB_RELOAD": "true"}))
	if err != nil || !cfg.Web.Reload {
		t.Errorf("the environment turns it on: %v, %v", cfg.Web.Reload, err)
	}
	if _, err := Load("", env(map[string]string{"RAPTOR_WEB_RELOAD": "sometimes"})); err == nil {
		t.Error("a value that is no boolean was accepted")
	}
}
