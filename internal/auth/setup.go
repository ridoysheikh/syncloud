package auth

import (
	"context"
	"strconv"
	"time"

	"syncloud/internal/store"
)

// SetupTokenTTL is how long the one-time setup token printed at install stays valid (§5.0).
const SetupTokenTTL = time.Hour

// EnsureSetupToken issues a fresh setup token if no account exists yet.
// It returns the plain token (to print once) or "" when setup is already done.
// A new token is issued on every controller start until setup completes, so a
// lost or expired token is recovered by restarting the controller.
func EnsureSetupToken(ctx context.Context, st *store.Store, now time.Time) (string, error) {
	n, err := st.CountUsers(ctx)
	if err != nil || n > 0 {
		return "", err
	}
	token := NewToken("syn_setup_")
	if err := st.SetSetting(ctx, store.SettingSetupTokenHash, HashToken(token)); err != nil {
		return "", err
	}
	exp := now.Add(SetupTokenTTL).Unix()
	if err := st.SetSetting(ctx, store.SettingSetupTokenExpires, strconv.FormatInt(exp, 10)); err != nil {
		return "", err
	}
	return token, nil
}

// CheckSetupToken reports whether token is the current, unexpired setup token.
func CheckSetupToken(ctx context.Context, st *store.Store, token string, now time.Time) (bool, error) {
	hash, ok, err := st.GetSetting(ctx, store.SettingSetupTokenHash)
	if err != nil || !ok {
		return false, err
	}
	expStr, ok, err := st.GetSetting(ctx, store.SettingSetupTokenExpires)
	if err != nil || !ok {
		return false, err
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || now.Unix() >= exp {
		return false, nil
	}
	return TokenMatches(token, hash), nil
}
