package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"golang.org/x/term"

	"gitlab.com/dunn.dev/bairn/api/famly"
	"gitlab.com/dunn.dev/bairn/internal/config"
)

// runLogin authenticates a Famly account with email and password,
// fetches /me to confirm the new token works for actual API calls,
// and prints success. The minted token is not persisted to disk;
// the operator's next bairn run reads creds from env or config and
// re-authenticates as needed.
//
// This is the recommended onboarding path. The advanced
// FAMLY_ACCESS_TOKEN flow (paste a session token from devtools)
// remains supported for accounts protected by MFA, where the
// Authenticate mutation returns "challenged".
func runLogin(ctx context.Context, cfg *config.Config, logger *slog.Logger, args []string) int {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	emailFlag := fs.String("email", "", "Famly email (or set FAMLY_EMAIL)")
	passwordFlag := fs.String("password", "", "Famly password (insecure; prefer FAMLY_PASSWORD or interactive prompt)")
	deviceID := fs.String("device-id", cfg.FamlyDeviceID, "stable device identifier sent to Famly")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	email := firstNonEmpty(*emailFlag, cfg.FamlyEmail)
	password := firstNonEmpty(*passwordFlag, cfg.FamlyPassword)

	if email == "" {
		var err error
		email, err = promptLine(os.Stdin, os.Stderr, "Email: ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "login: read email:", err)
			return 1
		}
	}
	if email == "" {
		fmt.Fprintln(os.Stderr, "login: email is required")
		return 2
	}

	if password == "" {
		var err error
		password, err = promptPassword(os.Stderr, "Password: ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "login: read password:", err)
			return 1
		}
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "login: password is required")
		return 2
	}

	token, err := famly.Login(ctx, cfg.FamlyBaseURL, email, password, *deviceID)
	if err != nil {
		// Distinguish MFA challenges from credential failures so
		// the operator gets the right next step.
		if strings.Contains(err.Error(), "challenged") {
			fmt.Fprintln(os.Stderr, "login: MFA required for this account.")
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "  Bairn does not yet support MFA flows. Workaround:")
			fmt.Fprintln(os.Stderr, "  1. Log into https://app.famly.co in a browser.")
			fmt.Fprintln(os.Stderr, "  2. Open devtools → Application → Local Storage.")
			fmt.Fprintln(os.Stderr, "  3. Copy the access token (a UUID-shaped string).")
			fmt.Fprintln(os.Stderr, "  4. Set FAMLY_ACCESS_TOKEN=<token> for future bairn runs.")
			return 1
		}
		fmt.Fprintln(os.Stderr, "login:", err)
		return 1
	}

	// Sanity-check the new token by hitting /me. Fails closed: a
	// successful Authenticate that produces an unusable token
	// is worse than a clear "wrong place to log in" error.
	c := famly.New(famly.NewStaticToken(token), famlyOpts(cfg)...)
	me, err := c.Me(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "login: token minted but /me check failed:", err)
		fmt.Fprintln(os.Stderr, "  this is unusual; report it.")
		return 1
	}

	// Defensive: me.Email is normally the login identifier and
	// always populated, but fall back to the email used to log in
	// when the API returns an empty string (Trap E).
	displayEmail := me.Email
	if displayEmail == "" {
		displayEmail = email
	}
	fmt.Fprintf(os.Stderr, "login: ok (logged in as %s, %d children visible)\n", displayEmail, len(me.Roles2))
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Next steps:")
	fmt.Fprintln(os.Stderr, "  Set FAMLY_EMAIL and FAMLY_PASSWORD in your shell or cron")
	fmt.Fprintln(os.Stderr, "  environment so bairn can re-authenticate when the session")
	fmt.Fprintln(os.Stderr, "  token rotates. For example:")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "    export FAMLY_EMAIL="+email)
	fmt.Fprintln(os.Stderr, "    export FAMLY_PASSWORD=<your-password>")
	fmt.Fprintln(os.Stderr, "    bairn fetch")
	return 0
}

// promptLine reads a single line from stdin with a prompt to stderr.
func promptLine(in *os.File, out *os.File, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// promptPassword reads a password from the controlling terminal
// without echoing. Falls back to a clear error when stdin is not
// a terminal (e.g. a pipe in cron); cron operators should use the
// FAMLY_PASSWORD env var.
func promptPassword(out *os.File, prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal; set FAMLY_PASSWORD instead")
	}
	fmt.Fprint(out, prompt)
	pw, err := term.ReadPassword(fd)
	fmt.Fprintln(out)
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
