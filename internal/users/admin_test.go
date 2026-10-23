package users

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/fielderr"
	"github.com/Helles01/raptor-panel/internal/ids"
)

func ptr[T any](v T) *T { return &v }

func fieldOf(t *testing.T, err error) string {
	t.Helper()
	fe, ok := fielderr.Of(err)
	if !ok {
		t.Fatalf("error %v is not a *fielderr.Error", err)
	}
	return fe.Field
}

func TestPrepareLowersTheUsername(t *testing.T) {
	in := validInput()
	in.Username = "Ada.Admin"

	rec, err := Prepare(in, rand.Reader)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if rec.Username != "ada.admin" {
		t.Errorf("Username = %q, want it lower case as the reference stores it", rec.Username)
	}
}

func TestPrepareNeedsAPassword(t *testing.T) {
	in := validInput()
	in.Password = ""

	_, err := Prepare(in, rand.Reader)

	if err == nil || fieldOf(t, err) != "password" {
		t.Errorf("Prepare without a password = %v, want a password error", err)
	}
}

func TestPrepareAccountWithoutPassword(t *testing.T) {
	in := validInput()
	in.Password = ""

	rec, err := PrepareAccount(in, []string{"en"}, rand.Reader)
	if err != nil {
		t.Fatalf("PrepareAccount: %v", err)
	}
	if !rec.NeedsSetup {
		t.Error("an account without a password must be marked as needing the setup mail")
	}
	if rec.PasswordHash == "" || !strings.HasPrefix(rec.PasswordHash, "$2") {
		t.Errorf("PasswordHash = %q, want a bcrypt hash of a random password", rec.PasswordHash)
	}
}

func TestPrepareAccountRandomPasswordsDiffer(t *testing.T) {
	in := validInput()
	in.Password = ""

	a, errA := PrepareAccount(in, nil, rand.Reader)
	b, errB := PrepareAccount(in, nil, rand.Reader)

	if errA != nil || errB != nil {
		t.Fatalf("PrepareAccount: %v, %v", errA, errB)
	}
	if a.PasswordHash == b.PasswordHash {
		t.Error("two accounts got the same hash")
	}
}

func TestPrepareAccountWithPassword(t *testing.T) {
	in := validInput()

	rec, err := PrepareAccount(in, []string{"en"}, rand.Reader)
	if err != nil {
		t.Fatalf("PrepareAccount: %v", err)
	}
	if rec.NeedsSetup {
		t.Error("an account with a password needs no setup mail")
	}
	if !auth.VerifyPassword(rec.PasswordHash, in.Password) {
		t.Error("the stored hash does not verify against the given password")
	}
}

func TestPrepareAccountExternalID(t *testing.T) {
	in := validInput()
	in.ExternalID = "whmcs-1042"

	rec, err := PrepareAccount(in, nil, rand.Reader)
	if err != nil || rec.ExternalID == nil || *rec.ExternalID != "whmcs-1042" {
		t.Fatalf("PrepareAccount = %+v, %v", rec, err)
	}

	in.ExternalID = ""
	rec, err = PrepareAccount(in, nil, rand.Reader)
	if err != nil || rec.ExternalID != nil {
		t.Errorf("a blank external id must be stored as none: %+v, %v", rec, err)
	}
}

func TestPrepareAccountFailsOnRandomSourceError(t *testing.T) {
	in := validInput()
	in.Password = ""

	if _, err := PrepareAccount(in, nil, bytes.NewReader([]byte{1, 2})); err == nil {
		t.Error("a failing random source must be an error, also for the generated password")
	}
}

func TestPrepareAccountLanguages(t *testing.T) {
	tests := []struct {
		name      string
		language  string
		available []string
		wantErr   bool
	}{
		{"default is english", "", []string{"en"}, false},
		{"listed", "pt_BR", []string{"en", "pt_BR"}, false},
		{"not listed", "de", []string{"en"}, true},
		{"no list given", "de", nil, false},
		{"empty list refuses all", "en", []string{}, true},
		{"bad shape is refused before the list", "english", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			in.Language = tt.language

			_, err := PrepareAccount(in, tt.available, rand.Reader)

			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && fieldOf(t, err) != "language" {
				t.Errorf("field = %q, want language", fieldOf(t, err))
			}
		})
	}
}

func TestValidateReportsTheField(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
		field  string
	}{
		{"email", func(in *Input) { in.Email = "nope" }, "email"},
		{"username", func(in *Input) { in.Username = "a" }, "username"},
		{"first name", func(in *Input) { in.FirstName = "" }, "first_name"},
		{"last name", func(in *Input) { in.LastName = "" }, "last_name"},
		{"external id too long", func(in *Input) { in.ExternalID = strings.Repeat("x", 192) }, "external_id"},
		{"external id with newline", func(in *Input) { in.ExternalID = "a\nb" }, "external_id"},
		{"language", func(in *Input) { in.Language = "x" }, "language"},
		{"password", func(in *Input) { in.Password = "short" }, "password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validInput()
			in.Language = "en"
			tt.mutate(&in)

			if got := fieldOf(t, Validate(in)); got != tt.field {
				t.Errorf("field = %q, want %q", got, tt.field)
			}
		})
	}
}

func TestValidateExternalID(t *testing.T) {
	tests := []struct {
		id string
		ok bool
	}{
		{"1", true},
		{"client#12 / plan:gold", true},
		{"zoë", true},
		{strings.Repeat("x", 191), true},
		{strings.Repeat("é", 191), true},
		{strings.Repeat("x", 192), false},
		{"", false},
		{"tab\there", false},
		{"nul\x00", false},
		{"del\x7f", false},
	}
	for _, tt := range tests {
		if err := ValidateExternalID(tt.id); (err == nil) != tt.ok {
			t.Errorf("ValidateExternalID(%q) = %v, want ok=%v", tt.id, err, tt.ok)
		}
	}
}

func TestCheckCanDelete(t *testing.T) {
	tests := []struct {
		servers int64
		wantErr error
	}{
		{0, nil},
		{1, ErrHasServers},
		{250, ErrHasServers},
	}
	for _, tt := range tests {
		if err := CheckCanDelete(tt.servers); !errors.Is(err, tt.wantErr) {
			t.Errorf("CheckCanDelete(%d) = %v, want %v", tt.servers, err, tt.wantErr)
		}
	}
	if !strings.Contains(ErrHasServers.Error(), "delete their servers") {
		t.Error("the refusal must keep the reference's text")
	}
}

func current() Account {
	return Account{
		ExternalID: ptr("ext-1"),
		Email:      "ada@example.com",
		Username:   "ada",
		FirstName:  "Ada",
		LastName:   "Lovelace",
		Language:   "pt_BR",
		Admin:      true,
	}
}

func sameBody() Change {
	return Change{Email: "ada@example.com", Username: "ada", FirstName: "Ada", LastName: "Lovelace"}
}

func TestPlanUpdateKeepsWhatIsNotSent(t *testing.T) {
	up, fx, err := PlanUpdate(current(), sameBody(), []string{"en"})
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}

	cur := current()
	if up.Account.Email != cur.Email || up.Language != cur.Language || !up.Admin || up.ExternalID == nil || *up.ExternalID != "ext-1" {
		t.Errorf("unsent fields changed: %+v", up)
	}
	if up.PasswordHash != "" {
		t.Error("no password was sent, so none may be stored")
	}
	if fx != (Effects{}) {
		t.Errorf("effects = %+v, want none", fx)
	}
}

func TestPlanUpdateChangesFields(t *testing.T) {
	ch := Change{
		ExternalID: ptr("ext-2"),
		Email:      "ada@new.example.com",
		Username:   "Ada.New",
		FirstName:  "Augusta",
		LastName:   "King",
		Language:   ptr("pt_BR"),
		Admin:      ptr(false),
	}

	up, fx, err := PlanUpdate(current(), ch, []string{"en", "pt_BR"})
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}

	want := Account{ExternalID: ptr("ext-2"), Email: "ada@new.example.com", Username: "ada.new", FirstName: "Augusta", LastName: "King", Language: "pt_BR", Admin: false}
	if up.Email != want.Email || up.Username != want.Username || up.FirstName != want.FirstName || up.LastName != want.LastName ||
		up.Language != want.Language || up.Admin != want.Admin || up.ExternalID == nil || *up.ExternalID != "ext-2" {
		t.Errorf("update = %+v, want %+v", up.Account, want)
	}
	if !fx.EmailChanged || !fx.AdminRevoked || fx.PasswordChanged {
		t.Errorf("effects = %+v, want the email change and the revoked admin", fx)
	}
}

func TestPlanUpdateClearsTheExternalID(t *testing.T) {
	ch := sameBody()
	ch.ExternalID = ptr("")

	up, _, err := PlanUpdate(current(), ch, nil)

	if err != nil || up.ExternalID != nil {
		t.Errorf("PlanUpdate = %+v, %v, want the external id cleared", up, err)
	}
}

func TestPlanUpdatePassword(t *testing.T) {
	ch := sameBody()
	ch.Password = "a brand new password"

	up, fx, err := PlanUpdate(current(), ch, nil)
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}

	if !fx.PasswordChanged {
		t.Error("a new password must be reported as a change")
	}
	if up.PasswordHash == "" || strings.Contains(up.PasswordHash, ch.Password) || !auth.VerifyPassword(up.PasswordHash, ch.Password) {
		t.Errorf("PasswordHash = %q does not verify against the new password", up.PasswordHash)
	}
}

func TestPlanUpdateEffects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Account, *Change)
		want   Effects
	}{
		{"nothing", func(*Account, *Change) {}, Effects{}},
		{"email in another case", func(_ *Account, ch *Change) { ch.Email = "ADA@Example.com" }, Effects{}},
		{"email changed", func(_ *Account, ch *Change) { ch.Email = "other@example.com" }, Effects{EmailChanged: true}},
		{"password", func(_ *Account, ch *Change) { ch.Password = "another password" }, Effects{PasswordChanged: true}},
		{"admin removed", func(_ *Account, ch *Change) { ch.Admin = ptr(false) }, Effects{AdminRevoked: true}},
		{"admin kept", func(_ *Account, ch *Change) { ch.Admin = ptr(true) }, Effects{}},
		{"admin granted", func(cur *Account, ch *Change) { cur.Admin = false; ch.Admin = ptr(true) }, Effects{}},
		{"not admin and stays so", func(cur *Account, ch *Change) { cur.Admin = false; ch.Admin = ptr(false) }, Effects{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cur, ch := current(), sameBody()
			tt.mutate(&cur, &ch)

			_, fx, err := PlanUpdate(cur, ch, nil)

			if err != nil || fx != tt.want {
				t.Errorf("effects = %+v, %v, want %+v", fx, err, tt.want)
			}
		})
	}
}

func TestPlanUpdateRefusals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Change)
		field  string
	}{
		{"email missing", func(ch *Change) { ch.Email = "" }, "email"},
		{"username missing", func(ch *Change) { ch.Username = "" }, "username"},
		{"first name missing", func(ch *Change) { ch.FirstName = "" }, "first_name"},
		{"last name missing", func(ch *Change) { ch.LastName = "" }, "last_name"},
		{"bad username", func(ch *Change) { ch.Username = "-x-" }, "username"},
		{"external id too long", func(ch *Change) { ch.ExternalID = ptr(strings.Repeat("x", 192)) }, "external_id"},
		{"unknown language", func(ch *Change) { ch.Language = ptr("de") }, "language"},
		{"language shape", func(ch *Change) { ch.Language = ptr("") }, "language"},
		{"short password", func(ch *Change) { ch.Password = "short" }, "password"},
		{"long password", func(ch *Change) { ch.Password = strings.Repeat("a", 73) }, "password"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := sameBody()
			tt.mutate(&ch)

			up, fx, err := PlanUpdate(current(), ch, []string{"en"})

			if err == nil {
				t.Fatal("expected an error")
			}
			if got := fieldOf(t, err); got != tt.field {
				t.Errorf("field = %q, want %q", got, tt.field)
			}
			if up != (Update{}) || fx != (Effects{}) {
				t.Errorf("a refused change must return nothing to store: %+v %+v", up, fx)
			}
		})
	}
}

func TestPrepareAccountGeneratesAThirtyCharacterPassword(t *testing.T) {
	src := make([]byte, 4096)
	for i := range src {
		src[i] = byte(i % 248)
	}
	want, err := ids.RandomAlnum(bytes.NewReader(src), 30)
	if err != nil || len(want) != 30 {
		t.Fatalf("RandomAlnum = %q, %v", want, err)
	}
	in := validInput()
	in.Password = ""

	rec, err := PrepareAccount(in, nil, bytes.NewReader(src))
	if err != nil {
		t.Fatalf("PrepareAccount: %v", err)
	}

	if !auth.VerifyPassword(rec.PasswordHash, want) {
		t.Error("the hash is not that of the 30 character random password drawn first from the source")
	}
}
