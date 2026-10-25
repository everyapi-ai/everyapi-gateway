package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/gui"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func envDefault(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func defaultDataDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "everyapi-web")
}

// secret supports container secret mounts without putting credentials on the command line.
func secret(name string) (string, error) {
	v, file := strings.TrimSpace(os.Getenv(name)), strings.TrimSpace(os.Getenv(name+"_FILE"))
	if file == "" {
		return v, nil
	}
	if v != "" {
		return "", fmt.Errorf("set only one of %s and %s_FILE", name, name)
	}
	f, err := os.Open(file)
	if err != nil {
		return "", fmt.Errorf("read %s_FILE: %w", name, err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(b) > 4096 {
		return "", fmt.Errorf("cannot read %s_FILE (maximum 4096 bytes)", name)
	}
	v = strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s_FILE is empty", name)
	}
	return v, nil
}

// apiRoot accepts an origin or a deployment prefix, with an optional /v1 suffix.
func apiRoot(base, region string) (string, error) {
	if region != "global" && region != "cn" {
		return "", errors.New("EVERYAPI_REGION must be global or cn")
	}
	if base == "" {
		if region == "cn" {
			base = "https://api-cn.everyapi.ai"
		} else {
			base = "https://api.everyapi.ai"
		}
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", errors.New("EVERYAPI_API_BASE must be an http(s) URL without credentials, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path != "" && path.Clean(u.Path) != u.Path {
		return "", errors.New("EVERYAPI_API_BASE must have a clean path")
	}
	u.Path = strings.TrimSuffix(u.Path, "/v1")
	return u.String(), nil
}

func randomKey(prefix string) string {
	b := make([]byte, 24)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func publicURL(addr string) (string, error) {
	base := strings.TrimSpace(os.Getenv("EVERYAPI_PUBLIC_URL"))
	if base == "" {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return "", err
		}
		if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
			host = "127.0.0.1"
		}
		base = "http://" + net.JoinHostPort(host, port)
	}
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return "", errors.New("EVERYAPI_PUBLIC_URL must be an http(s) origin without a path, credentials, query or fragment")
	}
	u.Path = ""
	return u.String(), nil
}

// privateKey keeps generated bootstrap keys stable across restarts and never logs them.
func privateKey(file, supplied, prefix string) (string, error) {
	if supplied != "" {
		return supplied, nil
	}
	b, err := os.ReadFile(file)
	if err == nil {
		if k := strings.TrimSpace(string(b)); k != "" {
			return k, nil
		}
		return "", fmt.Errorf("empty key file: %s", file)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	k := randomKey(prefix)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, writeErr := f.WriteString(k + "\n")
	closeErr := f.Close()
	return k, errors.Join(writeErr, closeErr)
}

func prepare(data, addr string) error {
	if !filepath.IsAbs(data) {
		return errors.New("--data-dir / EVERYAPI_DATA_DIR must be an absolute path")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return errors.New("--addr / EVERYAPI_WEB_ADDR must be host:port")
	}
	internal := envDefault("EVERYAPI_INTERNAL_ADDR", "127.0.0.1:13425")
	host, port, err := net.SplitHostPort(internal)
	ip := net.ParseIP(host)
	n, portErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || n < 1 || n > 65535 {
		return errors.New("EVERYAPI_INTERNAL_ADDR must be a loopback IP and a port between 1 and 65535")
	}
	public, err := publicURL(addr)
	if err != nil {
		return err
	}
	root, err := apiRoot(strings.TrimSpace(os.Getenv("EVERYAPI_API_BASE")), envDefault("EVERYAPI_REGION", "global"))
	if err != nil {
		return err
	}
	upstream, err := secret("EVERYAPI_API_KEY")
	if err != nil {
		return err
	}
	gui.WebEveryAPIBase = root
	admin, err := secret("EVERYAPI_WEB_KEY")
	if err != nil {
		return err
	}
	client, err := secret("EVERYAPI_GATEWAY_KEY")
	if err != nil {
		return err
	}
	if admin != "" {
		if err := gui.CheckWebKey(admin); err != nil {
			return fmt.Errorf("EVERYAPI_WEB_KEY: %w", err)
		}
	}
	if client != "" && (len(client) < 16 || len(client) > 256 || strings.ContainsAny(client, " \t\r\n") || strings.HasPrefix(client, "sk-magpie-")) {
		return errors.New("EVERYAPI_GATEWAY_KEY needs 16–256 printable ASCII characters and cannot start with sk-magpie-")
	}
	for i := 0; i < len(client); i++ {
		if client[i] <= ' ' || client[i] > '~' {
			return errors.New("EVERYAPI_GATEWAY_KEY must contain printable ASCII characters only")
		}
	}
	if client != "" && (client == admin || client == upstream) || upstream != "" && upstream == admin {
		return errors.New("upstream, administrator and gateway keys must be different")
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		return err
	}
	// Inherited agent paths must not let the server modify the operator's real agents.
	for _, name := range agentenv.Vars {
		if err := os.Unsetenv(name); err != nil {
			return err
		}
	}
	for name, dir := range map[string]string{
		"HOME": "home", "USERPROFILE": "home", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state", "APPDATA": "home/AppData/Roaming", "LOCALAPPDATA": "home/AppData/Local",
	} {
		d := filepath.Join(data, dir)
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Setenv(name, d); err != nil {
			return err
		}
	}
	if appdir.Portable() != "" {
		return errors.New("remove the Magpie .portable marker or data folder beside this executable; everyapi-web uses --data-dir")
	}
	admin, err = privateKey(filepath.Join(data, "admin-key"), admin, "")
	if err != nil {
		return err
	}
	if err := gui.CheckWebKey(admin); err != nil {
		return err
	}
	for name, v := range map[string]string{"MAGPIE_PUBLIC_URL": public, "MAGPIE_WEB_KEY": admin, "MAGPIE_NO_ACCOUNTS": "1", "MAGPIE_NO_STATS": "1", "MAGPIE_ADDR": internal} {
		if err := os.Setenv(name, v); err != nil {
			return err
		}
	}
	s, err := settings.LoadChecked()
	if err != nil {
		return err
	}
	s.LAN, s.NoAccountDiscovery, s.NoAutoUpdate, s.NoStats, s.NoUsageStats = true, true, true, true, true
	s.AutoRestart, s.GatewayMode = false, "on"
	if err := settings.Save(s); err != nil {
		return err
	}
	// Existing stores, including empty stores after deliberate deletion, remain authoritative.
	if _, err := os.Stat(provider.Path()); errors.Is(err, os.ErrNotExist) {
		// Remote presets cannot be saved without a key. Leave the provider
		// absent so the administrator can add it through the Web UI.
		if upstream != "" {
			p, err := provider.FromPreset("everyapi")
			if err != nil {
				return err
			}
			p.Chat, p.Responses, p.Anthropic, p.Gemini = root+"/v1", root+"/v1", root, root+"/v1beta"
			p.Key = upstream
			if err := provider.Save(p); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	} else if err := provider.FileError(); err != nil {
		return err
	}
	for _, p := range provider.All() {
		if p.ID != "everyapi" || p.Preset != "everyapi" {
			return errors.New("everyapi-web supports only EveryAPI; use a separate data directory for Magpie configurations")
		}
	}
	if _, err := access.List(); err != nil {
		return err
	}
	if _, err := os.Stat(access.Path()); errors.Is(err, os.ErrNotExist) {
		client, err = privateKey(filepath.Join(data, "client-key"), client, "sk-everyapi-web-")
		if err != nil {
			return err
		}
		if client == admin || client == upstream {
			return errors.New("gateway key must differ from the administrator and upstream keys")
		}
		_, err = access.Update("add-key", access.Change{Name: "EveryAPI clients", Secret: client})
		return err
	} else {
		return err
	}
}

func healthcheck(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequest(http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/html")
	r, err := c.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized || !strings.Contains(r.Header.Get("Content-Type"), "text/html") {
		return fmt.Errorf("Web listener returned %s instead of its sign-in page", r.Status)
	}
	return nil
}
