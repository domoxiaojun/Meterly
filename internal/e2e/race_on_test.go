//go:build race

package e2e

// raceEnabled: -race 下 modernc.org/sqlite(纯 Go 转译的 SQLite)每条 SQL 都被插桩,
// 慢 20 倍以上,压测缩短模拟时长,只验证并发正确性;吞吐数字以非 race 运行为准。
const raceEnabled = true
