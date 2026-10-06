package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"log/slog"
	"os"
	"path/filepath"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/upgrade"
	"syncloud/internal/version"
)

// selfUpgrade receives a new agent binary in chunks and replaces this agent
// with it (§5.0.1). Chunks arrive in order on the stream's receive loop.
type selfUpgrade struct {
	dataDir string
	log     *slog.Logger
	report  func(*agentv1.ConnectRequest)

	f       *os.File
	h       hash.Hash
	want    *agentv1.UpgradeAgent
	written int64
}

func (u *selfUpgrade) fail(v string, err error) {
	u.log.Error("agent upgrade failed", "version", v, "err", err)
	if u.f != nil {
		u.f.Close()
		u.f = nil
		_ = os.Remove(upgrade.AgentStagingFile(u.dataDir))
	}
	u.report(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_UpgradeResult{UpgradeResult: &agentv1.UpgradeResult{Version: v, Error: err.Error()}}})
}

func (u *selfUpgrade) chunk(m *agentv1.UpgradeAgent) {
	if m.GetOffset() == 0 {
		if u.f != nil {
			u.f.Close()
		}
		path := upgrade.AgentStagingFile(u.dataDir)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			u.fail(m.GetVersion(), err)
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if err != nil {
			u.fail(m.GetVersion(), err)
			return
		}
		u.f, u.h, u.want, u.written = f, sha256.New(), m, 0
		u.log.Info("receiving agent upgrade", "version", m.GetVersion(), "size", m.GetSize())
	}
	if u.f == nil || u.want.GetVersion() != m.GetVersion() || m.GetOffset() != u.written {
		if u.f != nil {
			u.fail(m.GetVersion(), fmt.Errorf("chunk at %d out of order (have %d bytes)", m.GetOffset(), u.written))
		}
		return
	}
	if _, err := u.f.Write(m.GetData()); err != nil {
		u.fail(m.GetVersion(), err)
		return
	}
	u.h.Write(m.GetData())
	u.written += int64(len(m.GetData()))
	if !m.GetLast() {
		return
	}
	err := u.f.Close()
	u.f = nil
	if err == nil && u.written != u.want.GetSize() {
		err = fmt.Errorf("received %d bytes, expected %d", u.written, u.want.GetSize())
	}
	if err == nil && hex.EncodeToString(u.h.Sum(nil)) != u.want.GetSha256() {
		err = fmt.Errorf("checksum mismatch")
	}
	if err != nil {
		u.fail(m.GetVersion(), err)
		return
	}
	ri, err := upgrade.BeginAgent(u.dataDir, upgrade.AgentStagingFile(u.dataDir), u.want.GetVersion(), version.Version)
	if err != nil {
		u.fail(m.GetVersion(), err)
		return
	}
	u.log.Info("restarting as the new agent", "version", u.want.GetVersion())
	if err := upgrade.Reexec(ri); err != nil {
		u.fail(m.GetVersion(), fmt.Errorf("re-exec: %w", err))
	}
}
