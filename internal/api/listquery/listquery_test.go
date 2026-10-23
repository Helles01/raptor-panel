package listquery

import (
	"errors"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

func query(t *testing.T, raw string) url.Values {
	t.Helper()
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("bad test query %q: %v", raw, err)
	}
	return q
}

func apiErr(t *testing.T, err error) *apierr.Error {
	t.Helper()
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("error %v is not an *apierr.Error", err)
	}
	return ae
}

func TestFilters(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []Condition
	}{
		{"none", "", nil},
		{"one value", "filter[email]=ada", []Condition{{"email", Partial, []string{"ada"}}}},
		{"comma list", "filter[email]=a,b", []Condition{{"email", Partial, []string{"a", "b"}}}},
		{"bracket list", "filter[email][]=a&filter[email][]=b", []Condition{{"email", Partial, []string{"a", "b"}}}},
		{"repeated key is a list", "filter[email]=a&filter[email]=b", []Condition{{"email", Partial, []string{"a", "b"}}}},
		{"empty alternatives are dropped", "filter[email]=a,,b,", []Condition{{"email", Partial, []string{"a", "b"}}}},
		{"only empty alternatives add nothing", "filter[email]=,", nil},
		{"an empty value among repeated ones is dropped", "filter[email]=a&filter[email]=", []Condition{{"email", Partial, []string{"a"}}}},
		{"a lone empty value is kept", "filter[email]=", []Condition{{"email", Partial, []string{""}}}},
		{"a lone empty value in brackets adds nothing", "filter[email][]=", nil},
		{"encoded brackets", "filter%5Bemail%5D=ada", []Condition{{"email", Partial, []string{"ada"}}}},
		{"value with spaces and percent", "filter[username]=my%20server%25", []Condition{{"username", Partial, []string{"my server%"}}}},
		{"true stays text", "filter[username]=true", []Condition{{"username", Partial, []string{"true"}}}},
		{"conditions follow the order of the spec", "filter[external_id]=x&filter[email]=y", []Condition{
			{"email", Partial, []string{"y"}}, {"external_id", Partial, []string{"x"}},
		}},
		{"a plain filter parameter is ignored", "filter=ada", nil},
		{"an unrelated parameter is ignored", "other[email]=ada&page=2", nil},
		{"a broken key is ignored", "filter[email=ada", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(query(t, tt.query), Users)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(got.Conditions, tt.want) {
				t.Errorf("conditions = %+v\nwant         %+v", got.Conditions, tt.want)
			}
		})
	}
}

func TestExactFilter(t *testing.T) {
	got, err := Parse(query(t, "filter[ip]=10.0.0.1&filter[port]=25565,25566"), Allocations)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Condition{{"ip", Exact, []string{"10.0.0.1"}}, {"port", Exact, []string{"25565", "25566"}}}
	if !reflect.DeepEqual(got.Conditions, want) {
		t.Errorf("conditions = %+v, want %+v", got.Conditions, want)
	}
}

func TestOwnerFilter(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"filter[server_id]=5", []string{"5"}},
		{"filter[server_id]=05", []string{"05"}},
		{"filter[server_id]=", nil},
		{"filter[server_id]=0", nil},
		{"filter[server_id]=abc", nil},
		{"filter[server_id]=-3", nil},
		{"filter[server_id]=5,6", nil},
		{"filter[server_id]=5,", nil},
		{"filter[server_id][]=5", nil},
		{"filter[server_id]=5&filter[server_id]=6", nil},
		{"filter[server_id]=5%20", nil},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got, err := Parse(query(t, tt.query), Allocations)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(got.Conditions) != 1 || got.Conditions[0].Match != Owner || !reflect.DeepEqual(got.Conditions[0].Values, tt.want) {
				t.Errorf("conditions = %+v, want one Owner condition with values %v", got.Conditions, tt.want)
			}
		})
	}
}

func TestUnknownFilter(t *testing.T) {
	_, err := Parse(query(t, "filter[password]=x&filter[Email]=y&filter[email]=z"), Users)

	ae := apiErr(t, err)
	if ae.Status != 400 || ae.Code != "InvalidFilterQuery" {
		t.Errorf("error = %d %s, want 400 InvalidFilterQuery", ae.Status, ae.Code)
	}
	want := "Requested filter(s) `Email, password` are not allowed. Allowed filter(s) are `email, uuid, username, external_id`."
	if ae.Detail != want {
		t.Errorf("detail = %q\nwant     %q", ae.Detail, want)
	}
}

func TestFilterWithBrokenNameIsUnknown(t *testing.T) {
	for _, raw := range []string{"filter[]=x", "filter[][]=x", "filter[a][b]=x"} {
		if _, err := Parse(query(t, raw), Users); apiErr(t, err).Code != "InvalidFilterQuery" {
			t.Errorf("%s: want InvalidFilterQuery, got %v", raw, err)
		}
	}
}

func TestTooManyValues(t *testing.T) {
	ok := "filter[email]=" + strings.Repeat("a,", MaxValues-1) + "a"
	if _, err := Parse(query(t, ok), Users); err != nil {
		t.Errorf("%d values must be accepted: %v", MaxValues, err)
	}

	tooMany := "filter[email]=" + strings.Repeat("a,", MaxValues) + "a"
	if _, err := Parse(query(t, tooMany), Users); apiErr(t, err).Status != 400 {
		t.Errorf("%d values must be refused, got %v", MaxValues+1, err)
	}
}

func TestSorts(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []Order
	}{
		{"none", "", nil},
		{"empty", "sort=", nil},
		{"only commas", "sort=,,", nil},
		{"ascending", "sort=id", []Order{{"id", false}}},
		{"descending", "sort=-id", []Order{{"id", true}}},
		{"several", "sort=-id,uuid", []Order{{"id", true}, {"uuid", false}}},
		{"several dashes", "sort=--id", []Order{{"id", true}}},
		{"bracket form", "sort[]=uuid&sort[]=-id", []Order{{"uuid", false}, {"id", true}}},
		{"same column twice keeps the first", "sort=id,-id", []Order{{"id", false}}},
		{"spaces", "sort=%20-id%20", []Order{{"id", true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(query(t, tt.query), Users)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(got.Order, tt.want) {
				t.Errorf("order = %+v, want %+v", got.Order, tt.want)
			}
		})
	}
}

func TestUnknownSort(t *testing.T) {
	for _, raw := range []string{"sort=email", "sort=id,-password", "sort=-", "sort=ID"} {
		t.Run(raw, func(t *testing.T) {
			_, err := Parse(query(t, raw), Users)

			ae := apiErr(t, err)
			if ae.Status != 400 || ae.Code != "InvalidSortQuery" {
				t.Errorf("error = %d %s, want 400 InvalidSortQuery", ae.Status, ae.Code)
			}
		})
	}

	_, err := Parse(query(t, "sort=email,-name"), Users)
	want := "Requested sort(s) `email, name` is not allowed. Allowed sort(s) are `id, uuid`."
	if got := apiErr(t, err).Detail; got != want {
		t.Errorf("detail = %q\nwant     %q", got, want)
	}
}

func TestASpecWithoutSortsRefusesEverySort(t *testing.T) {
	if _, err := Parse(query(t, "sort=id"), Allocations); apiErr(t, err).Code != "InvalidSortQuery" {
		t.Errorf("err = %v, want InvalidSortQuery", err)
	}
}

func TestIncludes(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []Include
	}{
		{"none", "", nil},
		{"one", "include=servers", []Include{{Name: "servers"}}},
		{"several with spaces", "include=%20allocations%20,%20location", []Include{{Name: "allocations"}, {Name: "location"}}},
		{"unknown ones are left out", "include=allocations,bogus,servers", []Include{{Name: "allocations"}, {Name: "servers"}}},
		{"nothing known", "include=bogus", nil},
		{"duplicates once", "include=servers,servers", []Include{{Name: "servers"}}},
		{"empty entries", "include=,,servers,", []Include{{Name: "servers"}}},
		{"bracket form", "include[]=location&include[]=servers", []Include{{Name: "location"}, {Name: "servers"}}},
		{"second level", "include=servers.allocations", []Include{{Name: "servers", Sub: []string{"allocations"}}}},
		{"second levels merge", "include=servers.allocations,allocations,servers.location,servers.allocations",
			[]Include{{Name: "servers", Sub: []string{"allocations", "location"}}, {Name: "allocations"}}},
		{"parent and child", "include=servers,servers.location", []Include{{Name: "servers", Sub: []string{"location"}}}},
		{"third level is dropped", "include=servers.allocations.node", nil},
		{"unknown parent drops the child", "include=bogus.servers", nil},
		{"empty child", "include=servers.", []Include{{Name: "servers"}}},
		{"case matters", "include=Servers", nil},
	}
	allowed := []string{"allocations", "location", "servers"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseIncludes(query(t, tt.query), allowed)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("includes = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseCarriesTheIncludesAndTable(t *testing.T) {
	got, err := Parse(query(t, "include=servers"), Users)
	if err != nil || got.Table != "users" || len(got.Includes) != 1 || got.Includes[0].Name != "servers" {
		t.Errorf("Parse = %+v, %v", got, err)
	}
}

func TestPartialPattern(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ada", "%ada%"},
		{"ADA", "%ada%"},
		{"ÉVA", "%éva%"},
		{"", "%%"},
		{"50%", "%50|%%"},
		{"a_b", "%a|_b%"},
		{"a|b", "%a||b%"},
		{`back\slash`, `%back\slash%`},
		{"%_|", "%|%|_||%"},
	}
	for _, tt := range tests {
		if got := PartialPattern(tt.in); got != tt.want {
			t.Errorf("PartialPattern(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWhere(t *testing.T) {
	tests := []struct {
		name     string
		q        Query
		wantSQL  string
		wantArgs []any
	}{
		{"no filters", Query{Table: "users"}, "", nil},
		{"partial", Query{Table: "users", Conditions: []Condition{{"email", Partial, []string{"Ada"}}}},
			"(LOWER(`users`.`email`) LIKE ? ESCAPE '|')", []any{"%ada%"}},
		{"alternatives", Query{Table: "users", Conditions: []Condition{{"email", Partial, []string{"a", "b_"}}}},
			"(LOWER(`users`.`email`) LIKE ? ESCAPE '|' OR LOWER(`users`.`email`) LIKE ? ESCAPE '|')", []any{"%a%", "%b|_%"}},
		{"exact", Query{Table: "allocations", Conditions: []Condition{{"port", Exact, []string{"1", "2", "3"}}}},
			"`allocations`.`port` IN (?,?,?)", []any{"1", "2", "3"}},
		{"owner is free", Query{Table: "allocations", Conditions: []Condition{{"server_id", Owner, nil}}},
			"`allocations`.`server_id` IS NULL", nil},
		{"owner is a server", Query{Table: "allocations", Conditions: []Condition{{"server_id", Owner, []string{"9"}}}},
			"`allocations`.`server_id` = ?", []any{"9"}},
		{"combined", Query{Table: "allocations", Conditions: []Condition{
			{"ip", Exact, []string{"10.0.0.1"}}, {"ip_alias", Partial, []string{"mc"}}, {"server_id", Owner, nil}}},
			"`allocations`.`ip` IN (?) AND (LOWER(`allocations`.`ip_alias`) LIKE ? ESCAPE '|') AND `allocations`.`server_id` IS NULL",
			[]any{"10.0.0.1", "%mc%"}},
		{"no table", Query{Conditions: []Condition{{"port", Exact, []string{"1"}}}}, "`port` IN (?)", []any{"1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := tt.q.Where()
			if sql != tt.wantSQL || !reflect.DeepEqual(args, tt.wantArgs) {
				t.Errorf("Where() = %q, %v\nwant       %q, %v", sql, args, tt.wantSQL, tt.wantArgs)
			}
		})
	}
}

func TestValuesNeverReachTheSQL(t *testing.T) {
	evil := "x' OR 1=1 -- `; DROP TABLE users"
	q, err := Parse(query(t, "filter[email]="+url.QueryEscape(evil)+"&filter[uuid][]="+url.QueryEscape(evil)), Users)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	sql, args := q.Where()

	if strings.Contains(sql, "DROP") || strings.Contains(sql, "OR 1=1") {
		t.Errorf("a value got into the SQL text: %s", sql)
	}
	if strings.Count(sql, "?") != len(args) {
		t.Errorf("%d placeholders for %d arguments in %s", strings.Count(sql, "?"), len(args), sql)
	}
}

func TestOrderBy(t *testing.T) {
	tests := []struct {
		name  string
		order []Order
		tie   string
		want  string
	}{
		{"default", nil, "id", "ORDER BY `users`.`id` ASC"},
		{"one key gets the tie breaker", []Order{{"uuid", false}}, "id", "ORDER BY `users`.`uuid` ASC, `users`.`id` ASC"},
		{"descending", []Order{{"uuid", true}}, "id", "ORDER BY `users`.`uuid` DESC, `users`.`id` ASC"},
		{"the tie breaker was asked for", []Order{{"id", true}}, "id", "ORDER BY `users`.`id` DESC"},
		{"the tie breaker in the middle ends it", []Order{{"uuid", false}, {"id", false}}, "id", "ORDER BY `users`.`uuid` ASC, `users`.`id` ASC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := Query{Table: "users", Order: tt.order}
			if got := q.OrderBy(tt.tie); got != tt.want {
				t.Errorf("OrderBy = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEverySpecNameIsASafeIdentifier(t *testing.T) {
	safe := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	specs := map[string]Spec{
		"Users": Users, "Nodes": Nodes, "Locations": Locations, "Servers": Servers, "Allocations": Allocations,
		"Nests": Nests, "Eggs": Eggs, "ServerDatabases": ServerDatabases, "ClientServers": ClientServers, "ClientActivity": ClientActivity,
	}
	for name, s := range specs {
		if !safe.MatchString(s.Table) {
			t.Errorf("%s: table %q is not a plain identifier", name, s.Table)
		}
		for _, f := range s.Filters {
			if f.Match != Search && !safe.MatchString(f.Name) {
				t.Errorf("%s: filter %q is not a plain identifier", name, f.Name)
			}
		}
		for _, c := range s.Sorts {
			if !safe.MatchString(c) {
				t.Errorf("%s: sort %q is not a plain identifier", name, c)
			}
		}
	}
}

func TestSpecsMatchTheReference(t *testing.T) {
	names := func(fs []Filter) []string {
		out := make([]string, len(fs))
		for i, f := range fs {
			out[i] = f.Name
		}
		return out
	}
	tests := []struct {
		name     string
		spec     Spec
		filters  []string
		sorts    []string
		includes []string
	}{
		{"users", Users, []string{"email", "uuid", "username", "external_id"}, []string{"id", "uuid"}, []string{"servers"}},
		{"nodes", Nodes, []string{"uuid", "name", "fqdn", "daemon_token_id"}, []string{"id", "uuid", "memory", "disk"}, []string{"allocations", "location", "servers"}},
		{"locations", Locations, []string{"short", "long"}, []string{"id"}, []string{"nodes", "servers"}},
		{"servers", Servers, []string{"uuid", "uuidShort", "name", "description", "image", "external_id"}, []string{"id", "uuid"},
			[]string{"allocations", "user", "subusers", "nest", "egg", "variables", "location", "node", "databases", "transfer"}},
		{"allocations", Allocations, []string{"ip", "port", "ip_alias", "server_id"}, nil, []string{"node", "server"}},
		{"nests", Nests, nil, nil, []string{"eggs", "servers"}},
		{"eggs", Eggs, nil, nil, []string{"nest", "servers", "config", "script", "variables"}},
		{"server databases", ServerDatabases, nil, nil, []string{"password", "host"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := names(tt.spec.Filters); !reflect.DeepEqual(got, tt.filters) && !(len(got) == 0 && len(tt.filters) == 0) {
				t.Errorf("filters = %v, want %v", got, tt.filters)
			}
			if !reflect.DeepEqual(tt.spec.Sorts, tt.sorts) {
				t.Errorf("sorts = %v, want %v", tt.spec.Sorts, tt.sorts)
			}
			if !reflect.DeepEqual(tt.spec.Includes, tt.includes) {
				t.Errorf("includes = %v, want %v", tt.spec.Includes, tt.includes)
			}
		})
	}
}

func TestNodeTokenIDIsMatchedExactly(t *testing.T) {
	f, ok := find(Nodes.Filters, "daemon_token_id")
	if !ok || f.Match != Exact {
		t.Errorf("daemon_token_id filter = %+v, %v, want an exact match", f, ok)
	}
}

func TestAKeyWithoutValuesIsHarmless(t *testing.T) {
	q := url.Values{"filter[server_id]": {}, "filter[email]": {}}

	got, err := Parse(q, Allocations)
	if err == nil {
		t.Fatalf("filter[email] is not offered for allocations, got %+v", got)
	}

	got, err = Parse(url.Values{"filter[server_id]": {}}, Allocations)
	if err != nil || len(got.Conditions) != 1 || got.Conditions[0].Values != nil {
		t.Errorf("Parse = %+v, %v, want one Owner condition for the free allocations", got, err)
	}
}

func TestSearchFilter(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  []Condition
	}{
		{"a text", "filter[*]=survival", []Condition{{"*", Search, []string{"survival"}}}},
		{"commas stay in the text", "filter[*]=my,server", []Condition{{"*", Search, []string{"my,server"}}}},
		{"empty adds nothing", "filter[*]=", nil},
		{"encoded star", "filter%5B*%5D=abc", []Condition{{"*", Search, []string{"abc"}}}},
		{"only the first value counts", "filter[*]=a&filter[*]=b", []Condition{{"*", Search, []string{"a"}}}},
		{"with a column filter", "filter[name]=x&filter[*]=y", []Condition{
			{"name", Partial, []string{"x"}}, {"*", Search, []string{"y"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(query(t, tt.query), ClientServers)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !reflect.DeepEqual(got.Conditions, tt.want) {
				t.Errorf("conditions = %+v\nwant         %+v", got.Conditions, tt.want)
			}
		})
	}
}

func TestSearchTakesLongTextWithManyCommas(t *testing.T) {
	_, err := Parse(query(t, "filter[*]="+strings.Repeat("a,", MaxValues+5)), ClientServers)

	if err != nil {
		t.Errorf("a search text is not a list of alternatives: %v", err)
	}
}

func TestSearchIsNotOfferedByTheOtherSpecs(t *testing.T) {
	if _, err := Parse(query(t, "filter[*]=x"), Users); apiErr(t, err).Code != "InvalidFilterQuery" {
		t.Errorf("err = %v, want InvalidFilterQuery", err)
	}
}

func TestSearchWhere(t *testing.T) {
	const textSQL = "(`servers`.`uuid` = ? OR `servers`.`uuid` LIKE ? ESCAPE '|' OR `servers`.`uuidShort` = ? OR `servers`.`external_id` = ? OR LOWER(`servers`.`name`) LIKE ? ESCAPE '|')"
	const sub = " IN (SELECT `server_id` FROM `allocations` WHERE `server_id` IS NOT NULL AND "
	tests := []struct {
		name     string
		value    string
		wantSQL  string
		wantArgs []any
	}{
		{"a name", "Survival", textSQL, []any{"Survival", "Survival%", "Survival", "Survival", "%survival%"}},
		{"an uuid prefix", "9f4d2c10", textSQL, []any{"9f4d2c10", "9f4d2c10%", "9f4d2c10", "9f4d2c10", "%9f4d2c10%"}},
		{"wildcards are plain characters", "50%_|", textSQL, []any{"50%_|", "50|%|_||%", "50%_|", "50%_|", "%50|%|_||%"}},
		{"an address", "10.0.0.5", "`servers`.`id`" + sub + "`ip` = ?)", []any{"10.0.0.5"}},
		{"an address with a port", "10.0.0.5:2556", "`servers`.`id`" + sub + "`ip` = ? AND `port` LIKE ?)", []any{"10.0.0.5", "2556%"}},
		{"a port", ":255", "`servers`.`id`" + sub + "`port` LIKE ?)", []any{"255%"}},
		{"not an address: four numbers and a letter", "10.0.0.5a", textSQL, []any{"10.0.0.5a", "10.0.0.5a%", "10.0.0.5a", "10.0.0.5a", "%10.0.0.5a%"}},
		{"not an address: a port without a colon", "25565", textSQL, []any{"25565", "25565%", "25565", "25565", "%25565%"}},
		{"not an address: too many digits in the port", ":123456", textSQL, []any{":123456", ":123456%", ":123456", ":123456", "%:123456%"}},
		{"not an address: junk before it", "a10.0.0.5", textSQL, []any{"a10.0.0.5", "a10.0.0.5%", "a10.0.0.5", "a10.0.0.5", "%a10.0.0.5%"}},
		{"not an address: junk before it, too many digits", "1234.0.0.5", textSQL, []any{"1234.0.0.5", "1234.0.0.5%", "1234.0.0.5", "1234.0.0.5", "%1234.0.0.5%"}},
		{"an address with a six digit port", "10.0.0.5:123456", textSQL, []any{"10.0.0.5:123456", "10.0.0.5:123456%", "10.0.0.5:123456", "10.0.0.5:123456", "%10.0.0.5:123456%"}},
		{"not an address: a trailing newline", "10.0.0.5\n", textSQL, []any{"10.0.0.5\n", "10.0.0.5\n%", "10.0.0.5\n", "10.0.0.5\n", "%10.0.0.5\n%"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := searchWhere("servers", tt.value)

			if sql != tt.wantSQL || !reflect.DeepEqual(args, tt.wantArgs) {
				t.Errorf("searchWhere(%q) =\n%s %v\nwant\n%s %v", tt.value, sql, args, tt.wantSQL, tt.wantArgs)
			}
			if strings.Count(sql, "?") != len(args) {
				t.Errorf("%d placeholders for %d arguments", strings.Count(sql, "?"), len(args))
			}
		})
	}
}

func TestWhereJoinsTheSearchWithTheOtherFilters(t *testing.T) {
	q, err := Parse(query(t, "filter[name]=x&filter[*]=10.0.0.5"), ClientServers)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	sql, args := q.Where()

	want := "(LOWER(`servers`.`name`) LIKE ? ESCAPE '|') AND `servers`.`id` IN (SELECT `server_id` FROM `allocations` WHERE `server_id` IS NOT NULL AND `ip` = ?)"
	if sql != want || !reflect.DeepEqual(args, []any{"%x%", "10.0.0.5"}) {
		t.Errorf("Where() = %q, %v", sql, args)
	}
}

func TestSearchValuesNeverReachTheSQL(t *testing.T) {
	q, _ := Parse(query(t, "filter[*]="+url.QueryEscape("x' OR 1=1 --")), ClientServers)

	sql, _ := q.Where()

	if strings.Contains(sql, "OR 1=1") {
		t.Errorf("the text got into the SQL: %s", sql)
	}
}

func TestClientListSpecs(t *testing.T) {
	if got := ClientActivity.Sorts; !reflect.DeepEqual(got, []string{"timestamp"}) {
		t.Errorf("activity sorts = %v", got)
	}
	if len(ClientActivity.Filters) != 1 || ClientActivity.Filters[0] != (Filter{"event", Partial}) {
		t.Errorf("activity filters = %v", ClientActivity.Filters)
	}
	names := make([]string, len(ClientServers.Filters))
	for i, f := range ClientServers.Filters {
		names[i] = f.Name
	}
	if !reflect.DeepEqual(names, []string{"uuid", "name", "description", "external_id", "*"}) {
		t.Errorf("server filters = %v", names)
	}
	if len(ClientServers.Sorts) != 0 {
		t.Error("the client's server list offers no sort")
	}
	if _, err := Parse(query(t, "sort=id"), ClientServers); apiErr(t, err).Code != "InvalidSortQuery" {
		t.Errorf("err = %v, want InvalidSortQuery", err)
	}
}

func TestASearchKeyWithoutValuesIsHarmless(t *testing.T) {
	got, err := Parse(url.Values{"filter[*]": {}}, ClientServers)

	if err != nil || len(got.Conditions) != 0 {
		t.Errorf("Parse = %+v, %v, want no condition and no panic", got, err)
	}
}
