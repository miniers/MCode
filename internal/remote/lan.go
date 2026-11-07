package remote

import (
	"net"
	"sort"
	"strconv"
	"strings"
)

// virtualInterfacePrefixes 是需要排在物理网卡之后的虚拟网卡名前缀，
// 覆盖 macOS/Linux 上常见的 VPN、容器、虚拟机与私有中继网卡。
// 这些网卡的地址手机往往无法直接访问（utun 的 VPN 隧道、Tailscale 的 CGNAT 段、
// Docker 的桥接网段等），因此不能作为扫码配对的首选地址。
var virtualInterfacePrefixes = []string{
	"utun", "ipsec", "tun", "tap", "ppp", "gif", "stf",
	"tailscale", "wg", "zt", "nebula", "ham",
	"bridge", "docker", "br-", "veth", "virbr", "vboxnet", "vmnet",
	"cni", "flannel", "kube", "podman", "lxc", "vnet",
	"awdl", "llw", "anpi",
}

// isVirtualInterface 判断网卡名是否属于虚拟/VPN/容器网卡。
// 未知或空名称按虚拟网卡处理，避免抢占物理网卡的优先级。
func isVirtualInterface(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return true
	}
	for _, prefix := range virtualInterfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// LANIPv4s 遍历活跃非回环网卡，返回过滤并排序后的可用局域网 IPv4 地址列表。
// 真实物理网卡（en0、en1、eth0 等）的地址排在前面，虚拟网卡地址仅作为兜底追加在后，
// 从而保证 PairingAuth 取首个地址时拿到的是手机可直接内网访问的 LAN 地址。
func LANIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	type nicAddrs struct {
		name     string
		physical bool
		ips      []string
	}

	groups := make([]nicAddrs, 0, len(ifaces))
	for _, iface := range ifaces {
		// 跳过未 up 和 loopback 的网卡
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		ips := filterLANIPv4(addrs)
		if len(ips) == 0 {
			continue
		}
		groups = append(groups, nicAddrs{
			name:     iface.Name,
			physical: !isVirtualInterface(iface.Name),
			ips:      ips,
		})
	}

	// 物理网卡优先，同类内按网卡名排序，保证多网卡环境下顺序稳定（en0 先于 en1）。
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].physical != groups[j].physical {
			return groups[i].physical
		}
		return groups[i].name < groups[j].name
	})

	seen := make(map[string]bool)
	var ips []string
	for _, g := range groups {
		for _, ip := range g.ips {
			if seen[ip] {
				continue
			}
			seen[ip] = true
			ips = append(ips, ip)
		}
	}
	return ips
}

// filterLANIPv4 从地址列表中提取有效局域网 IPv4 地址，排除回环、链路本地与未指定地址，去重并排序后返回。无结果时返回 nil。
func filterLANIPv4(addrs []net.Addr) []string {
	var ips []string
	seen := make(map[string]bool)

	for _, addr := range addrs {
		var ip net.IP
		switch a := addr.(type) {
		case *net.IPNet:
			ip = a.IP
		case *net.IPAddr:
			ip = a.IP
		default:
			continue
		}

		ip4 := ip.To4()
		if ip4 == nil {
			continue
		}

		if ip4.IsLoopback() || ip4.IsLinkLocalUnicast() || ip4.IsUnspecified() {
			continue
		}

		ipStr := ip4.String()
		if !seen[ipStr] {
			seen[ipStr] = true
			ips = append(ips, ipStr)
		}
	}

	if len(ips) == 0 {
		return nil
	}

	sort.Strings(ips)
	return ips
}

// LANBaseURLs 对每个可用局域网 IPv4 地址拼接对应端口的 HTTP 基础 URL。
// 返回顺序与 LANIPv4s 一致，因此首个 URL 是物理网卡地址（若存在）。
func LANBaseURLs(port int) []string {
	ips := LANIPv4s()
	if len(ips) == 0 {
		return nil
	}
	urls := make([]string, 0, len(ips))
	for _, ip := range ips {
		urls = append(urls, "http://"+net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	return urls
}
