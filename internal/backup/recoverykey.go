package backup

import (
	"context"
	"encoding/base64"

	"github.com/ridoysheikh/syncloud/internal/secrets"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// The recovery key is kept on the instance, sealed with the master key, so
// the root user can look it up at any time (Phase 19b). It is in backups
// only inside the encrypted body, which needs the recovery key to open.

var recoveryAAD = []byte("recovery-key")

// StoreRecoveryKey keeps key sealed in the settings.
func StoreRecoveryKey(ctx context.Context, st *store.Store, box *secrets.Box, key string) error {
	sealed := box.Seal([]byte(key), recoveryAAD)
	return st.SetSetting(ctx, store.SettingRecoveryKey, base64.StdEncoding.EncodeToString(sealed))
}

// StoredRecoveryKey returns the kept recovery key; ok is false on installs
// from before keys were kept, or when the kept key no longer matches.
func StoredRecoveryKey(ctx context.Context, st *store.Store, box *secrets.Box) (key string, ok bool, err error) {
	v, found, err := st.GetSetting(ctx, store.SettingRecoveryKey)
	if err != nil || !found {
		return "", false, err
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", false, err
	}
	b, err := box.Open(raw, recoveryAAD)
	if err != nil {
		return "", false, err
	}
	if box.Verify(string(b)) != nil {
		return "", false, nil
	}
	return string(b), true, nil
}
