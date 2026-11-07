package accounts

import (
	"context"
	"time"
)

// CheckFirstAdminAddress asks Authentik whether another account holds
// INITIAL_ADMIN_EMAIL, and warns if so (R44c). Run on every start, beside the
// API rather than before it: Authentik may not answer for minutes, and a
// failure to ask mustn't stop the API. It asks up to tries times. Its
// messages are string literals, so no address can reach the log (R73);
// scripts/stack-lib.sh looks for the first two.
func (s *Service) CheckFirstAdminAddress(ctx context.Context, every time.Duration, tries int) {
	e, ok := ParseEmail(s.InitialAdmin)
	if !ok {
		return
	}
	var err error
	for try := 0; try < tries; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(every):
			}
		}
		var holders EmailHolders
		if holders, err = s.Dir.EmailHolders(ctx, e.Address); err != nil {
			continue
		}
		if holders.Count > 1 || holders.Administrator {
			s.Log.Warn("first admin's address is also held by another account in Authentik, so sign-in and password reset reach that account, not the first admin: give Authentik's administrator (akadmin) a different address (docs/deploy.md, \"Two accounts with one address\")")
		} else {
			s.Log.Info("first admin's address isn't held by another account in Authentik")
		}
		return
	}
	if ctx.Err() == nil {
		s.Log.Warn("couldn't ask Authentik whether the first admin's address is also held by another account; asking again on the next start", "error", err.Error())
	}
}
