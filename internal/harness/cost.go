package harness

// CumulativeCostUSD 汇总按轮次升序排列的累计上报花费：CLI 进程重开（轮数、时长、空闲回收、
// 重启、Transient 轮次后 --resume）后累计值会从 0 重新开始，所以数值回落就视为新一段。
func CumulativeCostUSD(costs []float64) float64 {
	total, prev := 0.0, 0.0
	for _, c := range costs {
		if c <= 0 {
			continue
		}
		if c >= prev {
			total += c - prev
		} else {
			total += c
		}
		prev = c
	}
	return total
}
