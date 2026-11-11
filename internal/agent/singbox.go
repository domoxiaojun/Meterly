package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

// ---- clash_api 访问 ----

// ClashClient 访问 sing-box clash_api /connections。secret 只保存在内存。
type ClashClient struct {
	cfg    SingboxConfig
	url    string
	secret string // 永不打印
	http   *http.Client
}

func NewClashClient(cfg SingboxConfig) *ClashClient {
	return &ClashClient{cfg: cfg, http: &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: nil, MaxIdleConns: 1, IdleConnTimeout: 30 * time.Second},
	}}
}

// loadEndpoint 从 sing-box 配置读取 clash_api 地址与 secret;不修改任何 sing-box 配置。
func (c *ClashClient) loadEndpoint() error {
	c.url, c.secret = "", ""
	if c.cfg.URL != "" {
		c.url = strings.TrimRight(c.cfg.URL, "/") + "/connections"
	}
	if c.cfg.SecretFile != "" {
		if err := checkPerm(c.cfg.SecretFile); err != nil {
			return err
		}
		b, err := os.ReadFile(c.cfg.SecretFile)
		if err != nil {
			return errors.New("读取 singbox.secret_file 失败")
		}
		c.secret = strings.TrimSpace(string(b))
	}
	if c.cfg.ConfigPath == "" {
		if c.url == "" {
			return errors.New("未配置 singbox.url 或 config_path")
		}
		return nil
	}
	b, err := os.ReadFile(c.cfg.ConfigPath)
	if err != nil {
		return fmt.Errorf("读取 sing-box 配置失败: %s", errOp(err))
	}
	var sc struct {
		Experimental struct {
			ClashAPI struct {
				ExternalController string `json:"external_controller"`
				Secret             string `json:"secret"`
			} `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(b, &sc); err != nil {
		return errors.New("解析 sing-box 配置失败(需为标准 JSON)")
	}
	ca := sc.Experimental.ClashAPI
	if c.secret == "" {
		c.secret = ca.Secret
	}
	if c.url == "" {
		if ca.ExternalController == "" {
			return errors.New("sing-box 未启用 experimental.clash_api.external_controller")
		}
		c.url = "http://" + localController(ca.ExternalController) + "/connections"
	}
	return nil
}

// localController 把 0.0.0.0 / :: / 空主机 换成回环地址访问(只是访问方式,不改绑定)。
func localController(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func errOp(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return "I/O 错误"
}

// clashSnapshot /connections 的响应。
type clashSnapshot struct {
	UploadTotal   uint64      `json:"uploadTotal"`
	DownloadTotal uint64      `json:"downloadTotal"`
	Connections   []clashConn `json:"connections"`
}

type clashConn struct {
	ID       string         `json:"id"`
	Metadata map[string]any `json:"metadata"`
	Upload   uint64         `json:"upload"`
	Download uint64         `json:"download"`
	Chains   []string       `json:"chains"`
}

// Fetch 拉取一次快照;401 时重读配置(secret 可能已轮换)再试一次。
func (c *ClashClient) Fetch(ctx context.Context) (*clashSnapshot, error) {
	if c.url == "" {
		if err := c.loadEndpoint(); err != nil {
			return nil, err
		}
	}
	snap, status, err := c.get(ctx)
	if status == http.StatusUnauthorized {
		if lerr := c.loadEndpoint(); lerr != nil {
			return nil, lerr
		}
		snap, status, err = c.get(ctx)
	}
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("clash_api HTTP %d", status)
	}
	return snap, nil
}

func (c *ClashClient) get(ctx context.Context) (*clashSnapshot, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, 0, errors.New("clash_api 地址无效")
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("连接 clash_api 失败: %s", netErrKind(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, resp.StatusCode, nil
	}
	var s clashSnapshot
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&s); err != nil {
		return nil, resp.StatusCode, errors.New("clash_api 响应解析失败")
	}
	return &s, resp.StatusCode, nil
}

// netErrKind 只给出错误类别,避免把 URL 等细节写进日志。
func netErrKind(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "超时"
	case strings.Contains(err.Error(), "connection refused"):
		return "连接被拒绝"
	default:
		return "网络错误"
	}
}

// ---- 增量累计 ----

// SBKey sing-box 统计维度。
type SBKey struct {
	IP      string `json:"ip"`
	Inbound string `json:"in"`
	User    string `json:"u,omitempty"`
}

// ConnState 单连接上次看到的累计值。
type ConnState struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

// SBState 需持久化的 sing-box 采集状态(每分钟随 State 原子落盘)。
type SBState struct {
	Baselined bool                 `json:"baselined"`
	Up        uint64               `json:"up"`   // 上次全局 uploadTotal
	Down      uint64               `json:"down"` // 上次全局 downloadTotal
	Conns     map[string]ConnState `json:"conns"`
	Restarts  int                  `json:"restarts"`
}

// SingboxAcc 每 5~10 秒轮询一次,内存中按连接 id 累计增量,每分钟 Drain 一次。
type SingboxAcc struct {
	St      SBState
	acc     map[SBKey]*[2]uint64
	unattr  [2]uint64
	polls   int
	Restart func() // 检测到 sing-box 重启时回调(日志用)
}

func NewSingboxAcc(st SBState) *SingboxAcc {
	if st.Conns == nil {
		st.Conns = map[string]ConnState{}
	}
	return &SingboxAcc{St: st, acc: map[SBKey]*[2]uint64{}}
}

// Apply 处理一次快照。
//   - 首次(无基线):只记录基线,不计入(避免把进程生命周期累计量塞进一分钟);
//   - 全局计数下降:判定 sing-box 重启,清空连接状态,重启后的计数全部计入;
//   - 已知连接:计入差值;新连接:计入其全部计数;连接 id 去重;
//   - 全局增量 - 逐连接增量 = 轮询间隙内开合的短连接 + 已关闭连接最后一段 → 未归因桶。
func (s *SingboxAcc) Apply(snap *clashSnapshot) {
	s.polls++
	cur := make(map[string]ConnState, len(snap.Connections))
	if !s.St.Baselined {
		for _, c := range snap.Connections {
			cur[connID(c)] = ConnState{c.Upload, c.Download}
		}
		s.St = SBState{Baselined: true, Up: snap.UploadTotal, Down: snap.DownloadTotal, Conns: cur, Restarts: s.St.Restarts}
		return
	}
	if snap.UploadTotal < s.St.Up || snap.DownloadTotal < s.St.Down {
		s.St.Restarts++
		s.St.Up, s.St.Down = 0, 0
		s.St.Conns = map[string]ConnState{}
		if s.Restart != nil {
			s.Restart()
		}
	}
	gUp, gDown := snap.UploadTotal-s.St.Up, snap.DownloadTotal-s.St.Down
	var aUp, aDown uint64
	for _, c := range snap.Connections {
		id := connID(c)
		if _, dup := cur[id]; dup {
			continue
		}
		cur[id] = ConnState{c.Upload, c.Download}
		du, dd := c.Upload, c.Download
		if p, ok := s.St.Conns[id]; ok && c.Upload >= p.Up && c.Download >= p.Down {
			du, dd = c.Upload-p.Up, c.Download-p.Down
		}
		if du == 0 && dd == 0 {
			continue
		}
		k := connKey(c)
		v := s.acc[k]
		if v == nil {
			v = &[2]uint64{}
			s.acc[k] = v
		}
		v[0] += du
		v[1] += dd
		aUp += du
		aDown += dd
	}
	if gUp > aUp {
		s.unattr[0] += gUp - aUp
	}
	if gDown > aDown {
		s.unattr[1] += gDown - aDown
	}
	s.St.Up, s.St.Down = snap.UploadTotal, snap.DownloadTotal
	s.St.Conns = cur
}

// Drain 取出自上次 Drain 以来的累计增量(rx=upload, tx=download)。
func (s *SingboxAcc) Drain() []Counter {
	out := make([]Counter, 0, len(s.acc)+1)
	byIP := map[[2]string]*[2]uint64{}
	byUser := map[[2]string]*[2]uint64{}
	add := func(m map[[2]string]*[2]uint64, k [2]string, v *[2]uint64) {
		t := m[k]
		if t == nil {
			t = &[2]uint64{}
			m[k] = t
		}
		t[0] += v[0]
		t[1] += v[1]
	}
	for k, v := range s.acc {
		add(byIP, [2]string{k.IP, k.Inbound}, v)
		if k.User != "" {
			add(byUser, [2]string{k.User, k.Inbound}, v)
		}
	}
	for k, v := range byIP {
		out = append(out, Counter{Kind: proto.KindSingbox, Key: k[0], Key2: k[1], RX: v[0], TX: v[1]})
	}
	for k, v := range byUser {
		out = append(out, Counter{Kind: proto.KindSingboxUser, Key: k[0], Key2: k[1], RX: v[0], TX: v[1]})
	}
	if s.unattr[0] > 0 || s.unattr[1] > 0 {
		out = append(out, Counter{Kind: proto.KindSingbox, Key: proto.DimUnattributed, RX: s.unattr[0], TX: s.unattr[1]})
	}
	s.acc = map[SBKey]*[2]uint64{}
	s.unattr = [2]uint64{}
	return out
}

func connID(c clashConn) string {
	if c.ID != "" {
		return c.ID
	}
	// 兜底:无 id 时用 来源地址+入站+开始时间 组合
	return fmt.Sprintf("%v|%v|%v|%v", c.Metadata["sourceIP"], c.Metadata["sourcePort"], c.Metadata["type"], c.Metadata["start"])
}

func connKey(c clashConn) SBKey {
	return SBKey{IP: NormIP(metaStr(c.Metadata, "sourceIP", "source_ip")),
		Inbound: clean(metaStr(c.Metadata, "type", "inbound", "inboundTag")),
		User:    clean(metaStr(c.Metadata, "inboundUser", "user", "username"))}
}

func metaStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// NormIP 规范化来源 IP:IPv4-mapped IPv6 归一为 IPv4,IPv6 取规范写法,去掉 zone。
func NormIP(s string) string {
	a, err := netip.ParseAddr(strings.Trim(s, "[]"))
	if err != nil {
		if s == "" {
			return "?"
		}
		return clean(s)
	}
	return a.Unmap().WithZone("").String()
}

// clean 限制长度并去掉控制字符,防止异常数据撑爆存储。
func clean(s string) string {
	if len(s) > 96 {
		s = s[:96]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
