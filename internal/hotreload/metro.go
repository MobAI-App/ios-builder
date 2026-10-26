package hotreload

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// MetroLaunchArguments are the launch arguments that point a React Native
// debug build at the Metro bundler on host:port. React Native reads
// RCT_jsLocation from NSUserDefaults, which launch arguments set.
func MetroLaunchArguments(host string, port int) []string {
	return []string{"-RCT_jsLocation", fmt.Sprintf("%s:%d", host, port)}
}

// LocalIP is this machine's address on the local network, the one a phone on
// the same Wi-Fi reaches it at; under WSL, the Windows host's. "localhost"
// when there is none.
func LocalIP() string {
	if isWSL2() {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-Command",
			`Get-NetIPAddress -AddressFamily IPv4 | Where-Object { ($_.IPAddress -notlike "169.*") -and ($_.IPAddress -notlike "127.*") -and ($_.IPAddress -notlike "172.*") } | Select-Object -First 1 -ExpandProperty IPAddress`)
		output, err := cmd.Output()
		if err != nil {
			return "localhost"
		}
		ip := strings.TrimSpace(string(output))
		if ip == "" {
			return "localhost"
		}
		return ip
	}

	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	defer func() { _ = conn.Close() }()

	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "localhost"
	}
	return localAddr.IP.String()
}
