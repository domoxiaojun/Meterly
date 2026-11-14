package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

var upgradeTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type UpgradeResult struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func ValidateUpgrade(u proto.UpgradeTask) error {
	if u.ID <= 0 || !upgradeTag.MatchString(u.Version) {
		return errors.New("升级任务版本无效")
	}
	want := "https://github.com/domoxiaojun/Meterly/releases/download/" + u.Version + "/meterly-agent-linux-" + runtime.GOARCH
	if u.URL != want {
		return errors.New("升级包地址不属于本项目对应版本和平台")
	}
	if b, e := hex.DecodeString(u.SHA256); e != nil || len(b) != 32 {
		return errors.New("升级校验和无效")
	}
	return nil
}

// Updater is a separate root systemd oneshot. The ordinary agent cannot write /usr/local/bin.
// The only delegated operation is a validated project release, never a command supplied by the server.
type Updater struct {
	Download     func(context.Context, string, string, string) error
	Exec         ExecFunc
	ReadyTimeout time.Duration
}

func (u Updater) Apply(ctx context.Context, stateDir, target string) error {
	if u.Download == nil {
		u.Download = Upgrade
	}
	if u.Exec == nil {
		u.Exec = defaultExec
	}
	if u.ReadyTimeout <= 0 {
		u.ReadyTimeout = 60 * time.Second
	}
	req := filepath.Join(stateDir, "upgrade-request.json")
	b, e := os.ReadFile(req)
	if e != nil {
		return e
	}
	var task proto.UpgradeTask
	if json.Unmarshal(b, &task) != nil {
		return errors.New("升级任务格式无效")
	}
	if e = ValidateUpgrade(task); e != nil {
		return e
	}
	result := func(status, msg string) error {
		data, _ := json.Marshal(UpgradeResult{ID: task.ID, Status: status, Message: msg})
		return writeFileAtomic(filepath.Join(stateDir, "upgrade-result.json"), data)
	}
	if e = result("running", "正在校验并安装新版本"); e != nil {
		return e
	}
	old, e := os.ReadFile(target)
	if e != nil {
		result("failed", "无法备份当前版本")
		return e
	}
	backup := target + ".previous"
	if e = writeFileAtomic(backup, old); e != nil {
		result("failed", "无法保存回滚版本")
		return e
	}
	if e = os.Chmod(backup, 0755); e != nil {
		_ = result("failed", "无法设置回滚版本权限")
		return e
	}
	if e = u.Download(ctx, task.URL, task.SHA256, target); e != nil {
		result("failed", "下载或校验失败，当前版本保持不变")
		_ = os.Remove(req)
		return e
	}
	ready := filepath.Join(stateDir, "ready.json")
	_ = os.Remove(ready)
	started := time.Now().Unix()
	_, restartErr := u.Exec(ctx, "systemctl", "restart", "meterly-agent.service")
	deadline := time.NewTimer(u.ReadyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	healthy := func() bool {
		b, e := os.ReadFile(ready)
		if e != nil {
			return false
		}
		var v struct {
			Version string `json:"version"`
			At      int64  `json:"at"`
		}
		return json.Unmarshal(b, &v) == nil && v.Version == task.Version && v.At >= started
	}
	if restartErr == nil {
		for {
			if healthy() {
				if e = result("succeeded", "新版本已启动并完成本地初始化"); e != nil {
					return e
				}
				_ = os.Remove(req)
				return nil
			}
			select {
			case <-ctx.Done():
				restartErr = ctx.Err()
			case <-deadline.C:
				restartErr = errors.New("新版本启动超时")
			case <-tick.C:
				continue
			}
			break
		}
	}
	// Restore locally even if the center is unreachable. Network health is not the readiness signal.
	if e = writeFileAtomic(target, old); e == nil {
		e = os.Chmod(target, 0755)
	}
	if e == nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, e = u.Exec(rollbackCtx, "systemctl", "restart", "meterly-agent.service")
	}
	_ = os.Remove(req)
	if e != nil {
		result("failed", "升级失败，自动回滚失败，请检查本机服务")
		return fmt.Errorf("回滚失败: %w", e)
	}
	result("rolled_back", "新版本未就绪，已恢复上一版本")
	return fmt.Errorf("升级已回滚: %w", restartErr)
}
