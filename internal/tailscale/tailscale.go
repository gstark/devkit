// Package tailscale publishes local ports on the tailnet with `tailscale
// serve` and removes those handlers again.
package tailscale

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// Available reports whether the tailscale CLI is on PATH.
func Available() bool {
	_, err := exec.LookPath("tailscale")
	return err == nil
}

// DNSName returns this machine's MagicDNS name without the trailing dot, or
// "" when Tailscale is missing or down.
func DNSName() string {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var status struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if json.Unmarshal(out, &status) != nil {
		return ""
	}
	return strings.TrimSuffix(status.Self.DNSName, ".")
}

// Publish serves 127.0.0.1:port at https://<name>:port, unless it already
// is, and returns that URL. Running without sudo needs
// `sudo tailscale set --operator=$USER` once.
func Publish(port int) (string, error) {
	name := DNSName()
	if name == "" {
		return "", fmt.Errorf("tailscale is not running")
	}
	status, err := serveStatus()
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%s:%d", name, port)
	if _, published := status.Web[key]; !published {
		target := fmt.Sprintf("http://127.0.0.1:%d", port)
		out, err := exec.Command("tailscale", "serve", "--bg", fmt.Sprintf("--https=%d", port), target).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("tailscale serve: %s", strings.TrimSpace(string(out)))
		}
	}
	return fmt.Sprintf("https://%s:%d", name, port), nil
}

// Off removes every serve handler that proxies to the local port, logging
// each one to out.
func Off(port int, out io.Writer) error {
	handlers, err := HandlersForPort(port)
	if err != nil {
		return err
	}
	for _, flags := range handlers {
		fmt.Fprintf(out, "Removing tailscale serve %s (port %d)...\n", strings.Join(flags, " "), port)
		args := append([]string{"serve", "--yes"}, flags...)
		args = append(args, "off")
		if output, err := exec.Command("tailscale", args...).CombinedOutput(); err != nil {
			fmt.Fprintf(out, "Could not remove it (%s). Run: tailscale %s\n", strings.TrimSpace(string(output)), strings.Join(args, " "))
		}
	}
	return nil
}

// HandlersForPort returns one `tailscale serve` flag set per handler that
// proxies to 127.0.0.1 or localhost on port, such as
// ["--https=3010", "--set-path=/"].
func HandlersForPort(port int) ([][]string, error) {
	status, err := serveStatus()
	if err != nil {
		return nil, err
	}
	local := regexp.MustCompile(fmt.Sprintf(`^(?:[a-z+]+://)?(?:127\.0\.0\.1|localhost|\[::1\]):%d(?:/|$)`, port))
	var handlers [][]string
	for hostPort, web := range status.Web {
		listen := hostPort[strings.LastIndex(hostPort, ":")+1:]
		scheme := "https"
		if tcp, ok := status.TCP[listen]; ok && tcp.HTTP {
			scheme = "http"
		}
		for path, handler := range web.Handlers {
			if local.MatchString(handler.Proxy) {
				handlers = append(handlers, []string{fmt.Sprintf("--%s=%s", scheme, listen), "--set-path=" + path})
			}
		}
	}
	for listen, tcp := range status.TCP {
		if !local.MatchString(tcp.TCPForward) {
			continue
		}
		flag := "--tcp=" + listen
		if tcp.TerminateTLS != "" {
			flag = "--tls-terminated-tcp=" + listen
		}
		handlers = append(handlers, []string{flag})
	}
	return handlers, nil
}

type serveConfig struct {
	TCP map[string]struct {
		HTTP         bool   `json:"HTTP"`
		TCPForward   string `json:"TCPForward"`
		TerminateTLS string `json:"TerminateTLS"`
	} `json:"TCP"`
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string `json:"Proxy"`
		} `json:"Handlers"`
	} `json:"Web"`
}

func serveStatus() (serveConfig, error) {
	var config serveConfig
	out, err := exec.Command("tailscale", "serve", "status", "--json").Output()
	if err != nil {
		// No serve config yet reads as an error or empty output; treat both as empty.
		return config, nil
	}
	if strings.TrimSpace(string(out)) == "" {
		return config, nil
	}
	if err := json.Unmarshal(out, &config); err != nil {
		return config, fmt.Errorf("tailscale serve status: %w", err)
	}
	return config, nil
}
