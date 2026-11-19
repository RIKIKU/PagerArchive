package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/authentik"
	"github.com/RIKIKU/PagerArchive/internal/store"
)

const serviceAccountUsage = "usage: service-account create <name> --owner <admin ID or email> | service-account list | service-account revoke <name> | service-account rotate <name>"

// serviceAccountCmd manages service accounts (R62a, ADR 0048) from the
// server's shell, as clientCmd manages receiving clients (R59): a
// credential that reads the whole archive needs a shell on the server. It
// needs DATABASE_URL, AUTHENTIK_URL, AUTHENTIK_API_TOKEN and, in the
// compose stack, AUTHENTIK_INTERNAL_URL: the API container has them all.
func serviceAccountCmd(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(serviceAccountUsage)
	}
	acc, closeStore, err := serviceAccounts()
	if err != nil {
		return err
	}
	defer closeStore()
	ctx := context.Background()
	switch {
	case args[0] == "create" && len(args) == 4 && args[2] == "--owner":
		return printCredential(stdout, "created", func() (accounts.ServiceAccountCredential, error) {
			_, c, err := acc.CreateServiceAccount(ctx, args[1], args[3])
			return c, err
		})
	case args[0] == "create" && len(args) == 4 && args[1] == "--owner":
		return printCredential(stdout, "created", func() (accounts.ServiceAccountCredential, error) {
			_, c, err := acc.CreateServiceAccount(ctx, args[3], args[2])
			return c, err
		})
	case args[0] == "rotate" && len(args) == 2:
		return printCredential(stdout, "rotated: its old credential has ended", func() (accounts.ServiceAccountCredential, error) {
			return acc.RotateServiceAccount(ctx, args[1])
		})
	case args[0] == "revoke" && len(args) == 2:
		if err := acc.RevokeServiceAccount(ctx, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "service account %s revoked\n", args[1])
		return nil
	case args[0] == "list" && len(args) == 1:
		rows, err := acc.ListServiceAccounts(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSTATUS\tOWNER\tCREATED\tEXPIRES")
		now := time.Now()
		for _, r := range rows {
			status := r.Status
			switch {
			case r.Status == "deleted":
				status = "revoked"
			case r.Status == "active" && r.CredentialExpiresAt != nil && !now.Before(*r.CredentialExpiresAt):
				status = "expired"
			}
			owner := fmt.Sprintf("%d", r.OwnerID)
			if r.OwnerEmail != nil {
				owner += " " + *r.OwnerEmail
			} else {
				owner += " (deleted user)"
			}
			expires := "-"
			if r.CredentialExpiresAt != nil && r.Status != "deleted" {
				expires = r.CredentialExpiresAt.UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", deref(r.Name), status, owner, r.CreatedAt.UTC().Format(time.RFC3339), expires)
		}
		return tw.Flush()
	}
	return errors.New(serviceAccountUsage)
}

// printCredential prints a new credential, the only time it's ever shown.
func printCredential(stdout io.Writer, what string, fn func() (accounts.ServiceAccountCredential, error)) error {
	c, err := fn()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "service account %s %s. It expires %s.\n", c.Name, what, c.ExpiresAt.UTC().Format(time.RFC3339))
	fmt.Fprintln(stdout, "Its credential is shown only once. Give it to the tool, e.g. in PowerShell:")
	fmt.Fprintf(stdout, "  Connect-PagerArchive -Uri <the app's URL> -ServiceAccount (Get-Credential -UserName %s)\n", c.Name)
	fmt.Fprintln(stdout, "and paste the credential as the password. The credential:")
	fmt.Fprintln(stdout, c.Secret)
	return nil
}

// serviceAccounts is the accounts service with only what service accounts
// need: the database and Authentik's API.
func serviceAccounts() (*accounts.Service, func(), error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, nil, errors.New("DATABASE_URL is not set")
	}
	parse := func(name string) (*url.URL, error) {
		v := strings.TrimRight(os.Getenv(name), "/")
		if v == "" {
			return nil, nil
		}
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%s must be an http(s) URL", name)
		}
		return u, nil
	}
	public, err := parse("AUTHENTIK_URL")
	if err != nil {
		return nil, nil, err
	}
	if public == nil {
		return nil, nil, errors.New("AUTHENTIK_URL is not set")
	}
	internal, err := parse("AUTHENTIK_INTERNAL_URL")
	if err != nil {
		return nil, nil, err
	}
	token := os.Getenv("AUTHENTIK_API_TOKEN")
	if token == "" {
		return nil, nil, errors.New("AUTHENTIK_API_TOKEN is not set")
	}
	st, err := store.Open(context.Background(), dbURL)
	if err != nil {
		return nil, nil, err
	}
	httpc := &http.Client{Transport: authentik.Transport(public, internal, nil), Timeout: 30 * time.Second}
	dir := &authentik.Client{URL: public, Token: token, HTTP: httpc}
	// Its log lines go to stderr, as JSON, like the API's; never the credential.
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	return &accounts.Service{Store: st, Dir: dir, Now: time.Now, Log: log}, st.Close, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
