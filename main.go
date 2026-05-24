package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: %s <get|store|erase>", os.Args[0])
	}

	action := os.Args[1]
	stdinRaw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatalf("read stdin: %v", err)
	}
	logDebug(os.Args, stdinRaw)

	cred, err := readCredential(bytes.NewReader(stdinRaw))
	if err != nil {
		fatalf("read credential input: %v", err)
	}

	entry := passEntryName(cred)
	if entry == "" {
		// No usable lookup key, quietly no-op per Git credential helper expectations.
		return
	}

	switch action {
	case "get":
		out, found, err := passShow(entry)
		if err != nil {
			fatalf("pass show failed: %v", err)
		}
		if !found {
			return
		}

		stored := parseSecret(out)
		writeGetResult(cred, stored, os.Stdout)
	case "store":
		if err := passStore(entry, cred); err != nil {
			fatalf("pass store failed: %v", err)
		}
	case "erase":
		if err := passErase(entry); err != nil {
			fatalf("pass erase failed: %v", err)
		}
	default:
		fatalf("unsupported action %q", action)
	}
}

func readCredential(r io.Reader) (map[string]string, error) {
	scanner := bufio.NewScanner(r)
	m := make(map[string]string)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		m[k] = v
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

func writeGetResult(request, stored map[string]string, w io.Writer) {
	username := firstNonEmpty(stored["username"], request["username"])
	password := firstNonEmpty(stored["password"], request["password"])

	if username != "" {
		_, _ = fmt.Fprintf(w, "username=%s\n", username)
	}
	if password != "" {
		_, _ = fmt.Fprintf(w, "password=%s\n", password)
	}
}

func passEntryName(cred map[string]string) string {
	protocol := credentialComponent(cred["protocol"])
	host := credentialComponent(cred["host"])
	username := credentialComponent(credentialUsername(cred))
	if protocol == "" || host == "" {
		rawURL := strings.TrimSpace(cred["url"])
		if rawURL != "" {
			if u, err := url.Parse(rawURL); err == nil {
				if protocol == "" {
					protocol = credentialComponent(u.Scheme)
				}
				if host == "" {
					host = credentialComponent(u.Host)
				}
				if username == "" && u.User != nil {
					username = credentialComponent(u.User.Username())
				}
			}
		}
	}

	if host == "" || username == "" {
		return ""
	}

	parts := []string{"git-credential-pass"}
	if protocol != "" {
		parts = append(parts, protocol)
	}
	parts = append(parts, host, username)
	return path.Join(parts...)
}

func credentialUsername(cred map[string]string) string {
	if v := strings.TrimSpace(cred["username"]); v != "" {
		return v
	}
	if rawURL := strings.TrimSpace(cred["url"]); rawURL != "" {
		if u, err := url.Parse(rawURL); err == nil && u.User != nil {
			return u.User.Username()
		}
	}
	return ""
}

func credentialComponent(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	return escapePathSegment(v)
}

func escapePathSegment(v string) string {
	return url.PathEscape(v)
}

func passShow(entry string) (string, bool, error) {
	cmd := newPassCmd("show", entry)
	tty := attachTTYInput(cmd)
	if tty != nil {
		defer func() { _ = tty.Close() }()
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), true, nil
	}

	if isPassNotFoundError(err, out) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
}

func passStore(entry string, cred map[string]string) error {
	existingRaw, found, err := passShow(entry)
	if err != nil {
		return err
	}
	if found {
		existing := parseSecret(existingRaw)
		if credentialsEqual(existing, cred) {
			logDebug(os.Args, []byte("store skipped: existing entry unchanged\n"))
			return nil
		}
		logDebug(os.Args, []byte("store update: existing entry differs, rewriting\n"))
	}

	secret := formatSecret(cred)
	cmd := newPassCmd("insert", "-m", "-f", entry)
	cmd.Stdin = strings.NewReader(secret)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func credentialsEqual(existing, incoming map[string]string) bool {
	// Compare only secret payload fields so helper metadata variance
	// (host/protocol/url/path formatting) doesn't trigger redundant writes.
	if existing["password"] != incoming["password"] {
		return false
	}

	incomingUsername := strings.TrimSpace(incoming["username"])
	existingUsername := strings.TrimSpace(existing["username"])
	if incomingUsername != "" && existingUsername != "" && incomingUsername != existingUsername {
		return false
	}
	return true
}

func passErase(entry string) error {
	cmd := newPassCmd("rm", "-f", entry)
	tty := attachTTYInput(cmd)
	if tty != nil {
		defer func() { _ = tty.Close() }()
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if isPassNotFoundError(err, out) {
		return nil
	}
	return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
}

func formatSecret(cred map[string]string) string {
	password := cred["password"]
	var buf bytes.Buffer
	buf.WriteString(password)
	buf.WriteByte('\n')

	fields := map[string]string{}
	for _, k := range []string{"username", "protocol", "host", "path", "url"} {
		if v := strings.TrimSpace(cred[k]); v != "" {
			fields[k] = v
		}
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(&buf, "%s=%s\n", k, fields[k])
	}
	return buf.String()
}

func parseSecret(secret string) map[string]string {
	lines := strings.Split(strings.ReplaceAll(secret, "\r\n", "\n"), "\n")
	result := map[string]string{}

	// `pass` convention: first line is usually the password.
	if len(lines) > 0 && lines[0] != "" {
		result["password"] = lines[0]
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		result[k] = v
	}
	return result
}

func isPassNotFoundError(err error, output []byte) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	s := strings.ToLower(string(output))
	return strings.Contains(s, "not in the password store")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func newPassCmd(args ...string) *exec.Cmd {
	env := passEnv()
	updateGPGStartupTTY(env)

	cmd := exec.Command("pass", args...)
	cmd.Env = env
	return cmd
}

func passEnv() []string {
	env := os.Environ()
	if strings.TrimSpace(os.Getenv("GPG_TTY")) != "" {
		return env
	}

	if tty := detectTTY(); tty != "" {
		return append(env, "GPG_TTY="+tty)
	}
	return env
}

func detectTTY() string {
	if _, err := os.Stat("/dev/tty"); err == nil {
		return "/dev/tty"
	}

	cmd := exec.Command("tty")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if strings.HasPrefix(s, "/dev/") {
		return s
	}
	return ""
}

func updateGPGStartupTTY(env []string) {
	cmd := exec.Command("gpg-connect-agent", "updatestartuptty", "/bye")
	cmd.Env = env
	tty := attachTTYInput(cmd)
	if tty != nil {
		defer func() { _ = tty.Close() }()
	}
	_, _ = cmd.CombinedOutput()
}

func attachTTYInput(cmd *exec.Cmd) *os.File {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return nil
	}
	cmd.Stdin = tty
	return tty
}

func logDebug(args []string, stdinRaw []byte) {
	if !strings.EqualFold(strings.TrimSpace(os.Getenv("GIT_CREDENTIAL_PASS_DEBUG")), "on") {
		return
	}

	_, _ = fmt.Fprintf(os.Stderr, "git-credential-pass debug: args=%q\n", args)
	_, _ = fmt.Fprintf(os.Stderr, "git-credential-pass debug: stdin:\n%s\n", string(stdinRaw))
}
