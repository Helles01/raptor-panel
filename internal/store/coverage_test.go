package store_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Helles01/raptor-panel/internal/app"
	"github.com/Helles01/raptor-panel/internal/store"
)

// notHere are the stores of app.Stores that the SQL layer of this package does not make: the call to a node, and the store of the secret migration, which
// lives next to the secrets in internal/db.
var notHere = []string{"RemoteapiOldNodes", "SecretmigrationStore"}

// Every other store of app.Stores has a constructor on *store.DB that returns exactly its interface, so that app.Build can fill the whole of it.
func TestEveryStoreHasAConstructor(t *testing.T) {
	db := reflect.TypeOf((*store.DB)(nil))
	have := map[reflect.Type]bool{}
	for i := 0; i < db.NumMethod(); i++ {
		m := db.Method(i).Type // the receiver is the first argument
		if m.NumIn() == 1 && m.NumOut() == 1 {
			have[m.Out(0)] = true
		}
	}
	stores := reflect.TypeOf(app.Stores{})
	var missing []string
	for i := 0; i < stores.NumField(); i++ {
		f := stores.Field(i)
		if !have[f.Type] && !slices.Contains(notHere, f.Name) {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("no constructor on *store.DB for %d stores: %v", len(missing), missing)
	}
}
