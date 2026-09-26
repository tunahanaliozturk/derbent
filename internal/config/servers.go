package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Server is one downstream MCP server from the config, with every ${env:NAME} reference resolved.
// Exactly one of Command and URL is set.
type Server struct {
	Name    string
	Command []string
	Env     map[string]string
	URL     string
	Headers map[string]string
}

type serverFile struct {
	Command []string          `toml:"command"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
	Headers map[string]string `toml:"headers"`
}

var (
	serverName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	envRef     = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)
)

// reservedServers are prefixes the gate uses for its own tools.
var reservedServers = []string{"memory", "native"}

// servers validates the [servers] tables and resolves their environment references. It returns the
// servers sorted by name and every value that came from an environment variable in env or headers, so
// those values can be masked wherever arguments are stored.
func servers(files map[string]serverFile) ([]Server, []string, error) {
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
		s := Server{Name: name, Command: f.Command, URL: f.URL}
		var err error
		if s.URL != "" {
			if err = checkURL(name, s.URL); err != nil {
				return nil, nil, err
			}
		}
		if s.Env, err = expandMap(name, f.Env, &secrets); err != nil {
			return nil, nil, err
		}
		if s.Headers, err = expandMap(name, f.Headers, &secrets); err != nil {
			return nil, nil, err
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Server) int { return strings.Compare(a.Name, b.Name) })
	return out, secrets, nil
}

// expand replaces every ${env:NAME} in value and adds each resolved value to secrets. A variable that
// is not set is an error naming the variable, never showing any value.
func expand(server, value string, secrets *[]string) (string, error) {
	var missing string
	out := envRef.ReplaceAllStringFunc(value, func(ref string) string {
		name := envRef.FindStringSubmatch(ref)[1]
		v, ok := os.LookupEnv(name)
		if !ok {
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

func expandMap(server string, values map[string]string, secrets *[]string) (map[string]string, error) {
	if values == nil {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		resolved, err := expand(server, v, secrets)
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
