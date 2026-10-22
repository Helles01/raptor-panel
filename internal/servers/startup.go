package servers

import (
	"strconv"
	"strings"
)

const hiddenValue = "[hidden]"

// StartupCommand renders the startup command for display: the built-in placeholders and the egg variables are replaced by
// their values. Variables the user may not view show as "[hidden]", and with hideAll every variable does. This is for
// showing to users only; the node starts the server from the raw command and its own environment.
//
// The replacement is a single pass: a value that itself contains a {{PLACEHOLDER}} is shown as written.
func StartupCommand(s Server, hideAll bool) string {
	pairs := []string{
		"{{SERVER_MEMORY}}", strconv.FormatInt(s.Memory, 10),
		"{{SERVER_IP}}", s.Allocation.IP,
		"{{SERVER_PORT}}", strconv.Itoa(s.Allocation.Port),
	}
	for _, v := range s.Variables {
		value := hiddenValue
		if v.UserViewable && !hideAll {
			value = v.Current()
		}
		pairs = append(pairs, "{{"+v.EnvName+"}}", value)
	}
	// With duplicate names the first pair wins, so the built-ins cannot be overridden by an egg variable.
	return strings.NewReplacer(pairs...).Replace(s.Startup)
}
