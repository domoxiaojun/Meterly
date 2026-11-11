// Package proto 定义 agent -> server 的推送协议(JSON)。
//
//	POST /api/v2/ingest
//	Authorization: Bearer <host token>
//	Body: Batch
//
// 幂等:seq 在同一 agent 实例(InstanceID)内严格递增并按序发送,server 按
// (host, ts, seq) 去重(实现为每 (host, instance) 的 seq 高水位),agent 可安全重发。
package proto

// MaxRecordsPerBatch 单次推送最多记录数,超过 server 返回 413。
const MaxRecordsPerBatch = 5000

const Version = 2

// 记录类型(kind)。新增采集项只需新 kind,不改库。
const (
	KindIface       = "iface"        // dim1=网卡名
	KindSingbox     = "singbox"      // dim1=来源IP 或 特殊桶,dim2=入站(type/tag);rx=upload tx=download
	KindSingboxUser = "singbox_user" // dim1=user,dim2=入站
	KindSite        = "site"         // dim1=站点,dim2=客户端IP 或 ~other;rx=请求体(若有 $request_length) tx=$body_bytes_sent
	KindSiteReq     = "site_req"     // dim1=站点,dim2=状态类(2xx/3xx/4xx/5xx/other);rx=请求数 tx=0
	KindNetbird     = "netbird"      // dim1=对等端 FQDN,dim2=NetBird IP;rx/tx 为对等端累计增量
	KindXray        = "xray"         // dim1=入站 tag,dim2 空;rx=uplink tx=downlink(无来源 IP)
	KindXrayUser    = "xray_user"    // dim1=user/email,dim2=空或入站;无来源 IP
)

// sing-box 特殊桶。
const (
	DimUnattributed = "~unattributed" // 全局计数增量 - 逐连接增量(轮询间隙内开合/最后一段)
	DimOther        = "~other"        // Top-N 之外合并
)

// Record 是一个采集样本的增量(delta),覆盖时间区间 (PrevTS, TS]。
type Record struct {
	Seq    int64        `json:"seq"`
	TS     int64        `json:"ts"`      // UTC epoch 秒
	PrevTS int64        `json:"prev_ts"` // 0 = 未知(按 TS 所在分钟归因)
	Kind   string       `json:"kind"`
	Dim1   string       `json:"dim1"`
	Dim2   string       `json:"dim2,omitempty"`
	RX     uint64       `json:"rx"`
	TX     uint64       `json:"tx"`
	Site   *SiteMetrics `json:"site,omitempty"`
}

// CollectorHealth 采集器健康度。
type CollectorHealth struct {
	Enabled bool   `json:"enabled"`
	LastOK  int64  `json:"last_ok"`
	Fails   int    `json:"fails"`              // 连续失败次数
	LastErr string `json:"last_err,omitempty"` // 最近错误(已脱敏)
	ErrAt   int64  `json:"err_at,omitempty"`
	Note    string `json:"note,omitempty"` // 例如 sing-box 版本/重启次数
}

// Meta 主机元数据,随每次推送附带。
type Meta struct {
	Hostname      string                     `json:"hostname"`
	AgentVersion  string                     `json:"agent_version"`
	InstanceID    string                     `json:"instance_id"`
	Timezone      string                     `json:"timezone"`
	BootID        string                     `json:"boot_id"`
	SentAt        int64                      `json:"sent_at"`
	Buffered      int64                      `json:"buffered"` // 本批之外仍在本地缓冲的记录数
	Dropped       int64                      `json:"dropped"`  // 缓冲超限累计丢弃记录数
	Collectors    map[string]CollectorHealth `json:"collectors"`
	Protocol      int                        `json:"protocol"`
	Inventory     *Inventory                 `json:"inventory,omitempty"`
	ConfigVersion int64                      `json:"config_version"`
	ConfigError   string                     `json:"config_error,omitempty"`
	DroppedFrom   int64                      `json:"dropped_from,omitempty"`
	DroppedTo     int64                      `json:"dropped_to,omitempty"`
}

type Batch struct {
	Meta    Meta     `json:"meta"`
	Records []Record `json:"records"`
}

type IngestResponse struct {
	Accepted   int    `json:"accepted"`
	Duplicates int    `json:"duplicates"`
	Host       string `json:"host"`
	MaxSeq     int64  `json:"max_seq"`
	Rejected   int    `json:"rejected"`
}
