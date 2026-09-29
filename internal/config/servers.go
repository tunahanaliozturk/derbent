package config

import (
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Server is one downstream MCP server from the config, with every ${env:NAME} reference resolved.
// Exactly one of Command and URL is set.
type Server struct {
	Name    string
	Command []string
	Env     map[string]string
	URL     string
	Headers map[string]string
	// Pin is false for a server whose tools are served without pinning (pin = false; ADR 0013).
	Pin bool
}

type serverFile struct {
	Command []string          `toml:"command"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
	Headers map[string]string `toml:"headers"`
	Pin     *bool             `toml:"pin"`
}

var (
	serverName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	envRef     = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// reservedServers are prefixes of the gate's own tools, memory_ and handoff_, and of the CLIs' built-in
// tools, native__, so no server's tools can be named like them.
var reservedServers = []string{"memory", "handoff", "native"}

// servers validates the [servers] tables and resolves their environment references. It returns the
// servers sorted by name and every value that came from an environment variable in env or headers, so
// those values can be masked wherever arguments are stored. With skipUnset, a reference to a variable
// that is not set stays as written.
func servers(files map[string]serverFile, skipUnset bool) ([]Server, []string, error) {
	var out []Server
	var secrets []string
	for name, f := range files {
		if !serverName.MatchString(name) {
			return nil, nil, fmt.Errorf("server %q: names are 1 to 32 lower-case letters, digits and dashes", name)
		}
		if slices.Contains(reservedServers, name) {
			return nil, nil, fmt.Errorf("server %q: the name is reserved", name)
		}
		if (len(f.Command) == 0) == (f.URL == "") {
			return nil, nil, fmt.Errorf("server %s: set exactly one of command and url", name)
		}
		if len(f.Command) > 0 && len(f.Headers) > 0 {
			return nil, nil, fmt.Errorf("server %s: headers apply only to a url server", name)
		}
		if f.URL != "" && len(f.Env) > 0 {
			return nil, nil, fmt.Errorf("server %s: env applies only to a command server", name)
		}
		// HTTP errors quote the url, and they reach stderr and the agent, so a value resolved into
		// the url would not stay secret. Headers never appear in errors.
		if envRef.MatchString(f.URL) {
			return nil, nil, fmt.Errorf("server %s: url cannot use ${env:...}; send secrets in headers", name)
		}
		// A command line can end up in process listings and start errors, and a value resolved into it
		// would not be masked in receipts either. The server's environment is the place for secrets.
		if slices.ContainsFunc(f.Command, envRef.MatchString) {
			return nil, nil, fmt.Errorf("server %s: command cannot use ${env:...}; pass secrets to the server in env", name)
		}
		s := Server{Name: name, Command: f.Command, URL: f.URL, Pin: f.Pin == nil || *f.Pin}
		var err error
		if s.URL != "" {
			if err = checkURL(name, s.URL); err != nil {
				return nil, nil, err
			}
		}
		if s.Env, err = expandMap(name, f.Env, &secrets, skipUnset); err != nil {
			return nil, nil, err
		}
		if s.Headers, err = expandMap(name, f.Headers, &secrets, skipUnset); err != nil {
			return nil, nil, err
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Server) int { return strings.Compare(a.Name, b.Name) })
	return out, secrets, nil
}

// EnvNames lists, sorted and once each, the variables that ${env:NAME} names in the env and headers of
// the servers in the config file at path, without resolving them.
func EnvNames(path string) ([]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the path is the user's own config file
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var f file
	if _, err = toml.Decode(strings.TrimPrefix(string(data), "\uFEFF"), &f); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	var names []string
	for _, s := range f.Servers {
		for _, v := range slices.Concat(slices.Collect(maps.Values(s.Env)), slices.Collect(maps.Values(s.Headers))) {
			for _, m := range envRef.FindAllStringSubmatch(v, -1) {
				names = append(names, m[1])
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// expand replaces every ${env:NAME} in value and adds each resolved value to secrets. A variable that
// is not set is an error naming the variable, never showing any value, or with skipUnset stays as written.
func expand(server, value string, secrets *[]string, skipUnset bool) (string, error) {
	var missing string
	out := envRef.ReplaceAllStringFunc(value, func(ref string) string {
		name := envRef.FindStringSubmatch(ref)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
			if skipUnset {
				return ref
			}
			if missing == "" {
				missing = name
			}
			return ""
		}
		*secrets = append(*secrets, v)
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("server %s: environment variable %s is not set", server, missing)
	}
	return out, nil
}

func expandMap(server string, values map[string]string, secrets *[]string, skipUnset bool) (map[string]string, error) {
	if values == nil {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		resolved, err := expand(server, v, secrets, skipUnset)
		if err != nil {
			return nil, err
		}
		out[k] = resolved
	}
	return out, nil
}

// checkURL requires https, except for a server on the local machine, so a token in a header is never
// sent in the clear across a network.
func checkURL(server, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("server %s: url: %w", server, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("server %s: url must use https unless the server is on this machine", server)
	default:
		return fmt.Errorf("server %s: url must use https", server)
	}
}
