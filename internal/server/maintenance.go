package server

import (
	"compress/gzip"
	"context"
	"errors"
	"github.com/domoxiaojun/Meterly/internal/geo"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

type MaintenanceConfig struct {
	Backups   *bool  `json:"backups,omitempty"`
	BackupDir string `json:"backup_dir,omitempty"`
	Keep      int    `json:"keep,omitempty"`
}

func dirBytes(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() {
			if fi, e := d.Info(); e == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
func (s *Server) storageSample() map[string]int64 {
	dir := filepath.Dir(s.cfg.DBPath)
	var fs syscall.Statfs_t
	_ = syscall.Statfs(dir, &fs)
	db := int64(0)
	for _, p := range []string{s.cfg.DBPath, s.cfg.DBPath + "-wal", s.cfg.DBPath + "-shm"} {
		if fi, e := os.Stat(p); e == nil {
			db += fi.Size()
		}
	}
	return map[string]int64{"db_bytes": db, "backup_bytes": dirBytes(s.cfg.Maintenance.BackupDir), "free_bytes": int64(fs.Bavail) * int64(fs.Bsize), "total_bytes": int64(fs.Blocks) * int64(fs.Bsize)}
}
func (s *Server) backup(ctx context.Context) error {
	dir := s.cfg.Maintenance.BackupDir
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	sample := s.storageSample()
	if sample["free_bytes"] < sample["db_bytes"]*2+(512<<20) {
		return errors.New("磁盘空间不足以安全备份，未缩短数据保留期")
	}
	base := filepath.Join(dir, "meterly-"+s.Now().UTC().Format("20060102T150405.000000000"))
	tmp := base + ".db.tmp"
	defer os.Remove(tmp)
	if e := s.st.Backup(ctx, tmp); e != nil {
		return e
	}
	in, e := os.Open(tmp)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(base+".db.gz.tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer os.Remove(out.Name())
	gz := gzip.NewWriter(out)
	_, e = io.Copy(gz, in)
	if ce := gz.Close(); e == nil {
		e = ce
	}
	if e == nil {
		e = out.Sync()
	}
	if ce := out.Close(); e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	if e = os.Rename(out.Name(), base+".db.gz"); e != nil {
		return e
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "meterly-*.db.gz"))
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	for i := s.cfg.Maintenance.Keep; i < len(matches); i++ {
		if e = os.Remove(matches[i]); e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) runJob(ctx context.Context, kind string) {
	s.jobMu.Lock()
	if s.runningJobs[kind] {
		s.jobMu.Unlock()
		return
	}
	s.runningJobs[kind] = true
	s.jobMu.Unlock()
	defer func() { s.jobMu.Lock(); delete(s.runningJobs, kind); s.jobMu.Unlock() }()
	now := s.Now()
	r, e := s.st.W.ExecContext(ctx, `INSERT INTO maintenance_runs(kind,status,started_at) VALUES(?,'running',?)`, kind, now.Unix())
	if e != nil {
		return
	}
	id, _ := r.LastInsertId()
	switch kind {
	case "retention":
		_, e = s.st.Maintain(ctx, s.cfg.Retention, now)
	case "backup":
		e = s.backup(ctx)
	case "release":
		e = s.checkRelease(ctx)
	case "geo":
		u := s.geoUpdater()
		e = u.UpdateOnce(ctx)
		if e == nil {
			e = s.enrichIPs(ctx, 5000)
		}
	default:
		e = errors.New("未知维护任务")
	}
	status, msg := "succeeded", "完成"
	if e != nil {
		status = "failed"
		msg = e.Error()
		s.log.Warn("维护任务失败", "kind", kind, "err", e)
	}
	_, _ = s.st.W.ExecContext(ctx, `UPDATE maintenance_runs SET status=?,message=?,finished_at=? WHERE id=?`, status, msg, s.Now().Unix(), id)
	// Keep task history bounded, independent from traffic retention.
	_, _ = s.st.W.ExecContext(ctx, `DELETE FROM maintenance_runs WHERE id NOT IN (SELECT id FROM maintenance_runs ORDER BY id DESC LIMIT 500)`)
}
func (s *Server) jobDue(ctx context.Context, kind string, after int64) bool {
	var n int
	_ = s.st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM maintenance_runs WHERE kind=? AND started_at>=?`, kind, after).Scan(&n)
	return n == 0
}
func (s *Server) RunScheduler(ctx context.Context) {
	// An interrupted process must not leave tasks permanently running in the panel.
	_, _ = s.st.W.ExecContext(ctx, `UPDATE maintenance_runs SET status='interrupted',message='中心在任务完成前重启',finished_at=? WHERE status='running'`, s.Now().Unix())
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		now := s.Now()
		if s.jobDue(ctx, "retention", now.Add(-10*time.Minute).Unix()) {
			s.runJob(ctx, "retention")
		}
		if s.cfg.Geo.AutoUpdateOn() && s.jobDue(ctx, "geo", now.Add(-24*time.Hour).Unix()) {
			s.runJob(ctx, "geo")
		}
		if s.jobDue(ctx, "release", now.Add(-24*time.Hour).Unix()) {
			s.runJob(ctx, "release")
		}
		local := now.In(s.cfg.loc)
		due := time.Date(local.Year(), local.Month(), local.Day(), 4, 20, 0, 0, s.cfg.loc)
		if (s.cfg.Maintenance.Backups == nil || *s.cfg.Maintenance.Backups) && !now.Before(due) && s.jobDue(ctx, "backup", due.Unix()) {
			s.runJob(ctx, "backup")
		}
		_ = s.enrichIPs(ctx, 1000)
		v := s.storageSample()
		_, _ = s.st.W.ExecContext(ctx, `INSERT OR REPLACE INTO storage_samples(ts,db_bytes,backup_bytes,free_bytes,total_bytes) VALUES(?,?,?,?,?)`, now.Unix()/3600*3600, v["db_bytes"], v["backup_bytes"], v["free_bytes"], v["total_bytes"])
		_, _ = s.st.W.ExecContext(ctx, `DELETE FROM storage_samples WHERE ts<?`, now.Add(-30*24*time.Hour).Unix())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	rows, e := s.st.R.QueryContext(r.Context(), `SELECT id,kind,status,message,started_at,finished_at FROM maintenance_runs ORDER BY id DESC LIMIT 100`)
	if e != nil {
		writeErr(w, 500, "读取任务失败")
		return
	}
	defer rows.Close()
	runs := []map[string]any{}
	for rows.Next() {
		var id, started, finished int64
		var kind, status, msg string
		if rows.Scan(&id, &kind, &status, &msg, &started, &finished) != nil {
			writeErr(w, 500, "读取任务失败")
			return
		}
		runs = append(runs, map[string]any{"id": id, "kind": kind, "status": status, "message": msg, "started_at": started, "finished_at": finished})
	}
	disk := s.storageSample()
	alerts := []string{}
	if disk["total_bytes"] > 0 && (disk["free_bytes"] < 2<<30 || disk["free_bytes"] < disk["total_bytes"]/10) {
		alerts = append(alerts, "磁盘可用空间不足；请扩容或清理其他文件，明细保留期未改变")
	}
	var firstTS, firstBytes, lastTS, lastBytes int64
	_ = s.st.R.QueryRowContext(r.Context(), `SELECT ts,db_bytes FROM storage_samples ORDER BY ts LIMIT 1`).Scan(&firstTS, &firstBytes)
	_ = s.st.R.QueryRowContext(r.Context(), `SELECT ts,db_bytes FROM storage_samples ORDER BY ts DESC LIMIT 1`).Scan(&lastTS, &lastBytes)
	var daily float64
	if lastTS > firstTS {
		daily = float64(lastBytes-firstBytes) * 86400 / float64(lastTS-firstTS)
	}
	estimate := any(nil)
	if daily > 0 {
		estimate = float64(disk["free_bytes"]) / daily
	}
	writeJSON(w, 200, map[string]any{"storage": disk, "daily_growth_bytes": daily, "estimated_days_left": estimate, "maintenance": runs, "alerts": alerts, "retention": s.cfg.Retention, "backup_keep": s.cfg.Maintenance.Keep, "backup_time": "04:20", "tz": s.cfg.TZ, "geo": s.geo.Status()})
}
func (s *Server) handleRunJob(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind != "backup" && kind != "geo" && kind != "retention" && kind != "release" {
		writeErr(w, 400, "未知维护任务")
		return
	}
	s.jobWG.Add(1)
	go func() {
		defer s.jobWG.Done()
		ctx, cancel := context.WithTimeout(s.bgCtx, 15*time.Minute)
		defer cancel()
		s.runJob(ctx, kind)
	}()
	writeJSON(w, 202, map[string]any{"status": "accepted", "kind": kind})
}
func (s *Server) handleInstallInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"script_url": "https://raw.githubusercontent.com/domoxiaojun/Meterly/main/scripts/install-agent.sh", "agent_url": s.cfg.AgentURL, "protocol": 2, "instructions": []string{"生成一次性注册码，保存到权限 600 的文件", "下载并检查安装脚本，使用 --enroll-code-file 指定该文件", "自签证书同时提供 --ca-file，注册码和 token 不放入命令行", "安装后到主机页面检查网站、网卡及客户端 IP 识别状态"}})
}
func (s *Server) geoUpdater() *geo.Updater {
	return &geo.Updater{DB: s.geo, BaseURL: s.cfg.Geo.BaseURL, Log: s.log, Now: s.Now}
}
