// Package listquery reads the filter, sort and include parameters of the Application API's list endpoints. The names
// that may be filtered, sorted or included are fixed per resource in a Spec; anything else is refused, so that a
// request can never put its own text into the SQL that is built from a Query.
package listquery

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

// Match is how a filter compares its values with the column.
type Match uint8

const (
	// Partial: the column contains the value, without regard to case. Several values are alternatives.
	Partial Match = iota
	// Exact: the column equals the value, or one of the values.
	Exact
	// Owner is the server_id filter of allocations: a number selects the allocations of that server, anything else
	// the free ones. The reference does the same, which lets a client ask for free allocations with filter[server_id]=.
	Owner
	// Search is the filter[*] of the client's server list: one text that is looked for in several columns, or an address
	// and port in the allocations (see searchWhere). The text is not split at commas.
	Search
)

// Filter is an allowed filter. Name is the query parameter and also the column.
type Filter struct {
	Name  string
	Match Match
}

// Spec is what a list endpoint allows. Table and every name in it are constants of the code, never request data.
type Spec struct {
	Table    string
	Filters  []Filter
	Sorts    []string
	Includes []string
}

// Condition is one filter that was asked for. Values is never empty, except for Owner, where none means "not
// assigned to a server".
type Condition struct {
	Column string
	Match  Match
	Values []string
}

// Order is one sort key.
type Order struct {
	Column string
	Desc   bool
}

// Include is a related resource to attach to the answer. Sub holds the names after the dot (include=servers.node):
// the reference follows two levels and drops longer paths. Which sub names exist is up to the resource that renders it.
type Include struct {
	Name string
	Sub  []string
}

// Query is a parsed request for one list.
type Query struct {
	Table      string
	Conditions []Condition
	Order      []Order
	Includes   []Include
}

const (
	// MaxValues bounds the alternatives of one filter.
	MaxValues = 50
	// maxIncludePath is the number of levels Fractal follows (limitRecursion(2) in the reference).
	maxIncludePath = 2
)

var filterKey = regexp.MustCompile(`^filter\[([^\[\]]*)\]((?:\[[^\[\]]*\])*)$`)

// Parse reads the filters, sorts and includes of q. A filter or sort that spec does not offer is a 400, as in the
// reference. An include that is not offered is left out, also as there.
func Parse(q url.Values, spec Spec) (Query, error) {
	conds, err := parseFilters(q, spec)
	if err != nil {
		return Query{}, err
	}
	order, err := parseSorts(q, spec)
	if err != nil {
		return Query{}, err
	}
	return Query{Table: spec.Table, Conditions: conds, Order: order, Includes: ParseIncludes(q, spec.Includes)}, nil
}

type rawFilter struct {
	values []string
	array  bool
}

func parseFilters(q url.Values, spec Spec) ([]Condition, error) {
	asked := map[string]*rawFilter{}
	for key, vals := range q {
		m := filterKey.FindStringSubmatch(key)
		if m == nil {
			continue
		}
		f := asked[m[1]]
		if f == nil {
			f = &rawFilter{}
			asked[m[1]] = f
		}
		f.values = append(f.values, vals...)
		f.array = f.array || m[2] != "" || len(vals) > 1
	}

	names := make([]string, 0, len(asked))
	for name := range asked {
		names = append(names, name)
	}
	sort.Strings(names)

	var unknown []string
	for _, name := range names {
		if _, ok := find(spec.Filters, name); !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		allowed := make([]string, len(spec.Filters))
		for i, f := range spec.Filters {
			allowed[i] = f.Name
		}
		return nil, apierr.InvalidFilter(fmt.Sprintf("Requested filter(s) `%s` are not allowed. Allowed filter(s) are `%s`.",
			strings.Join(unknown, ", "), strings.Join(allowed, ", ")))
	}

	var conds []Condition
	for _, f := range spec.Filters {
		raw, ok := asked[f.Name]
		if !ok {
			continue
		}
		if f.Match == Search {
			if len(raw.values) > 0 && raw.values[0] != "" {
				conds = append(conds, Condition{Column: f.Name, Match: Search, Values: []string{raw.values[0]}})
			}
			continue
		}
		values, array, err := splitValues(f.Name, raw)
		if err != nil {
			return nil, err
		}
		if f.Match == Owner {
			conds = append(conds, Condition{Column: f.Name, Match: Owner, Values: ownerValue(values, array)})
			continue
		}
		if len(values) == 0 {
			continue
		}
		conds = append(conds, Condition{Column: f.Name, Match: f.Match, Values: values})
	}
	return conds, nil
}

func find(filters []Filter, name string) (Filter, bool) {
	for _, f := range filters {
		if f.Name == name {
			return f, true
		}
	}
	return Filter{}, false
}

// splitValues cuts the values at commas. A value that stands alone is kept even when it is empty; among alternatives
// the empty ones are dropped, as in the reference. array tells whether there were alternatives.
func splitValues(name string, raw *rawFilter) (values []string, array bool, err error) {
	array = raw.array
	for _, v := range raw.values {
		if strings.Contains(v, ",") {
			array = true
			values = append(values, strings.Split(v, ",")...)
			continue
		}
		values = append(values, v)
	}
	if array {
		kept := values[:0]
		for _, v := range values {
			if v != "" {
				kept = append(kept, v)
			}
		}
		values = kept
	}
	if len(values) > MaxValues {
		return nil, array, apierr.BadRequest(fmt.Sprintf("The filter %s may have at most %d values.", name, MaxValues))
	}
	return values, array, nil
}

// ownerValue is the id the Owner filter selects, or nil for the allocations that have no server. Like the
// reference, only a lone value of digits that is not "0" counts as an id.
func ownerValue(values []string, array bool) []string {
	if array || len(values) != 1 || values[0] == "0" || values[0] == "" || strings.Trim(values[0], "0123456789") != "" {
		return nil
	}
	return values
}

func parseSorts(q url.Values, spec Spec) ([]Order, error) {
	var order []Order
	var unknown []string
	seen := map[string]bool{}
	for _, part := range listValues(q, "sort") {
		key := strings.TrimLeft(part, "-")
		if !contains(spec.Sorts, key) {
			unknown = append(unknown, key)
			continue
		}
		if !seen[key] {
			seen[key] = true
			order = append(order, Order{Column: key, Desc: strings.HasPrefix(part, "-")})
		}
	}
	if len(unknown) > 0 {
		return nil, apierr.InvalidSort(fmt.Sprintf("Requested sort(s) `%s` is not allowed. Allowed sort(s) are `%s`.",
			strings.Join(unknown, ", "), strings.Join(spec.Sorts, ", ")))
	}
	return order, nil
}

// ParseIncludes reads the include parameter: names separated by commas, each of them trimmed, with a dot for a
// second level. Names that are not in allowed are left out. The names come back once each, in the order asked.
func ParseIncludes(q url.Values, allowed []string) []Include {
	var out []Include
	index := map[string]int{}
	for _, part := range listValues(q, "include") {
		path := strings.Split(part, ".")
		if len(path) > maxIncludePath || !contains(allowed, path[0]) {
			continue
		}
		i, ok := index[path[0]]
		if !ok {
			i = len(out)
			index[path[0]] = i
			out = append(out, Include{Name: path[0]})
		}
		if len(path) == 2 && path[1] != "" && !contains(out[i].Sub, path[1]) {
			out[i].Sub = append(out[i].Sub, path[1])
		}
	}
	return out
}

// listValues reads a parameter that holds names separated by commas, given once or as name[]. Entries are trimmed
// and empty ones dropped.
func listValues(q url.Values, name string) []string {
	var out []string
	for _, key := range []string{name, name + "[]"} {
		for _, v := range q[key] {
			for _, part := range strings.Split(v, ",") {
				if part = strings.TrimSpace(part); part != "" {
					out = append(out, part)
				}
			}
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// PartialPattern is the LIKE pattern for a Partial value: lower case, with the characters that mean something to LIKE
// made literal. It goes with ESCAPE '|' (see Where).
func PartialPattern(value string) string {
	return "%" + likeEscape(strings.ToLower(value)) + "%"
}

func likeEscape(value string) string {
	return strings.NewReplacer("|", "||", "%", "|%", "_", "|_").Replace(value)
}

func quote(table, column string) string {
	if table == "" {
		return "`" + column + "`"
	}
	return "`" + table + "`.`" + column + "`"
}

// Where returns the SQL condition for the filters, to be joined to the rest of the WHERE with AND, and its
// arguments. The condition is empty when there are no filters. Every name in it comes from the Spec; the values
// only ever travel as arguments.
func (q Query) Where() (string, []any) {
	parts := make([]string, 0, len(q.Conditions))
	var args []any
	for _, c := range q.Conditions {
		col := quote(q.Table, c.Column)
		switch c.Match {
		case Partial:
			alts := make([]string, len(c.Values))
			for i, v := range c.Values {
				alts[i] = "LOWER(" + col + ") LIKE ? ESCAPE '|'"
				args = append(args, PartialPattern(v))
			}
			parts = append(parts, "("+strings.Join(alts, " OR ")+")")
		case Exact:
			marks := strings.TrimSuffix(strings.Repeat("?,", len(c.Values)), ",")
			parts = append(parts, col+" IN ("+marks+")")
			for _, v := range c.Values {
				args = append(args, v)
			}
		case Search:
			sql, a := searchWhere(q.Table, c.Values[0])
			parts = append(parts, sql)
			args = append(args, a...)
		case Owner:
			if len(c.Values) == 0 {
				parts = append(parts, col+" IS NULL")
				continue
			}
			parts = append(parts, col+" = ?")
			args = append(args, c.Values[0])
		}
	}
	return strings.Join(parts, " AND "), args
}

// OrderBy returns the ORDER BY clause. The tie breaker, a unique column, always ends it, so that the pages of a list
// do not shuffle between requests: the reference leaves equal rows in whatever order the database likes.
func (q Query) OrderBy(tieBreaker string) string {
	keys := make([]string, 0, len(q.Order)+1)
	tied := false
	for _, o := range q.Order {
		dir := " ASC"
		if o.Desc {
			dir = " DESC"
		}
		keys = append(keys, quote(q.Table, o.Column)+dir)
		tied = tied || o.Column == tieBreaker
	}
	if !tied {
		keys = append(keys, quote(q.Table, tieBreaker)+" ASC")
	}
	return "ORDER BY " + strings.Join(keys, ", ")
}

var (
	addressSearch = regexp.MustCompile(`^(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?::[0-9]{1,5})?$`)
	portSearch    = regexp.MustCompile(`^:[0-9]{1,5}$`)
)

// searchWhere is the condition of the Search filter on the servers table. A text that is an IPv4 address, optionally with a
// port, or only a port with its colon, is looked for in the allocations of the servers: the address must be equal and the port
// must start with the digits, as in the reference. Anything else is looked for in the name (contains), and in the UUID (equal or
// begins with), short UUID and external id (equal). The reference lets a "%" or "_" in the text act as a wildcard; here they are
// ordinary characters.
func searchWhere(table, value string) (string, []any) {
	id := quote(table, "id")
	if addressSearch.MatchString(value) || portSearch.MatchString(value) {
		ip, port, hasPort := strings.Cut(value, ":")
		var parts []string
		var args []any
		if ip != "" {
			parts = append(parts, "`ip` = ?")
			args = append(args, ip)
		}
		if hasPort {
			parts = append(parts, "`port` LIKE ?")
			args = append(args, port+"%")
		}
		return id + " IN (SELECT `server_id` FROM `allocations` WHERE `server_id` IS NOT NULL AND " + strings.Join(parts, " AND ") + ")", args
	}

	sql := "(" + quote(table, "uuid") + " = ? OR " + quote(table, "uuid") + " LIKE ? ESCAPE '|' OR " + quote(table, "uuidShort") + " = ? OR " +
		quote(table, "external_id") + " = ? OR LOWER(" + quote(table, "name") + ") LIKE ? ESCAPE '|')"
	return sql, []any{value, likeEscape(value) + "%", value, value, PartialPattern(value)}
}
