package app

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/web"
)

type siteStub struct{}

func (siteStub) Site(context.Context) (web.Site, error) { return web.Site{}, nil }

func TestStoresListsEveryInterfaceTheSQLLayerImplements(t *testing.T) {
	typ := reflect.TypeOf(Stores{})
	// docs/store-contract.md: 66 interfaces of the endpoints and the jobs, and the two the wiring needed (PageapiOrigins and MailSettings). A new interface is added to Stores and to the document on purpose.
	if typ.NumField() != 68 {
		t.Errorf("Stores has %d fields, the contract says 68", typ.NumField())
	}
	seen := map[reflect.Type]string{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type.Kind() != reflect.Interface {
			t.Errorf("%s is a %s, not an interface", f.Name, f.Type.Kind())
		}
		if other, dup := seen[f.Type]; dup {
			t.Errorf("%s and %s are the same interface", f.Name, other)
		}
		seen[f.Type] = f.Name
		if !strings.HasPrefix(f.Type.PkgPath(), "github.com/Helles01/raptor-panel/internal/") {
			t.Errorf("%s is %s, which is not a package of the panel", f.Name, f.Type)
		}
		// The name says where the interface is: PackageName.
		if want := strings.ToLower(f.Name); !strings.HasPrefix(want, strings.ToLower(f.Type.PkgPath()[strings.LastIndex(f.Type.PkgPath(), "/")+1:])) || !strings.HasSuffix(f.Name, f.Type.Name()) {
			t.Errorf("%s does not say that it is %s", f.Name, f.Type)
		}
	}
}

func TestMissing(t *testing.T) {
	var s Stores
	all := s.Missing()
	if len(all) != reflect.TypeOf(s).NumField() {
		t.Fatalf("an empty Stores misses %d of %d", len(all), reflect.TypeOf(s).NumField())
	}
	if all[0] != reflect.TypeOf(s).Field(0).Name {
		t.Errorf("the order is not that of the struct: %v", all[:3])
	}
	s.PageapiSettings = siteStub{}
	rest := s.Missing()
	if len(rest) != len(all)-1 {
		t.Fatalf("%d are missing after one was set", len(rest))
	}
	for _, name := range rest {
		if name == "PageapiSettings" {
			t.Error("a store that is set is reported as missing")
		}
	}
}
