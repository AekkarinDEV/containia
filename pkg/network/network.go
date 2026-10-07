package network

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"containia/pkg/config"
)

const (
	BridgeName    = "containia0"
	BridgeSubnet  = "172.19.0.0/24"
	BridgeGateway = "172.19.0.1"
)

func SetupBridge() error {
	_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)

	if err := exec.Command("ip", "link", "show", BridgeName).Run(); err != nil {
		if out, err := exec.Command("ip", "link", "add", "name", BridgeName, "type", "bridge").CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create bridge %s: %s (%w)", BridgeName, string(out), err)
		}

		if out, err := exec.Command("ip", "addr", "add", BridgeGateway+"/24", "dev", BridgeName).CombinedOutput(); err != nil {
			return fmt.Errorf("failed to assign IP to bridge: %s (%w)", string(out), err)
		}

		if out, err := exec.Command("ip", "link", "set", BridgeName, "up").CombinedOutput(); err != nil {
			return fmt.Errorf("failed to bring bridge up: %s (%w)", string(out), err)
		}
	}

	if err := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-s", BridgeSubnet, "!", "-o", BridgeName, "-j", "MASQUERADE").Run(); err != nil {
		_ = exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", BridgeSubnet, "!", "-o", BridgeName, "-j", "MASQUERADE").Run()
	}
	if err := exec.Command("iptables", "-t", "nat", "-C", "POSTROUTING", "-d", BridgeSubnet, "-j", "MASQUERADE").Run(); err != nil {
		_ = exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-d", BridgeSubnet, "-j", "MASQUERADE").Run()
	}

	if err := exec.Command("iptables", "-C", "FORWARD", "-i", BridgeName, "-j", "ACCEPT").Run(); err != nil {
		_ = exec.Command("iptables", "-A", "FORWARD", "-i", BridgeName, "-j", "ACCEPT").Run()
	}
	if err := exec.Command("iptables", "-C", "FORWARD", "-o", BridgeName, "-j", "ACCEPT").Run(); err != nil {
		_ = exec.Command("iptables", "-A", "FORWARD", "-o", BridgeName, "-j", "ACCEPT").Run()
	}

	return nil
}

func SetupContainerNetwork(containerID string, pid int) (string, error) {
	if err := SetupBridge(); err != nil {
		return "", err
	}

	shortID := containerID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	hostVeth := "vh-" + shortID
	contVeth := "vc-" + shortID

	_ = exec.Command("ip", "link", "delete", hostVeth).Run()

	if out, err := exec.Command("ip", "link", "add", hostVeth, "type", "veth", "peer", "name", contVeth).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to create veth pair: %s (%w)", string(out), err)
	}

	if out, err := exec.Command("ip", "link", "set", hostVeth, "master", BridgeName).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to attach veth to bridge: %s (%w)", string(out), err)
	}
	_ = exec.Command("ip", "link", "set", hostVeth, "up").Run()

	pidStr := fmt.Sprintf("%d", pid)
	if out, err := exec.Command("ip", "link", "set", contVeth, "netns", pidStr).CombinedOutput(); err != nil {
		return "", fmt.Errorf("failed to move veth to container netns: %s (%w)", string(out), err)
	}

	ipAddr := generateContainerIP(containerID)

	commands := [][]string{
		{"nsenter", "-t", pidStr, "-n", "ip", "link", "set", contVeth, "name", "eth0"},
		{"nsenter", "-t", pidStr, "-n", "ip", "addr", "add", ipAddr + "/24", "dev", "eth0"},
		{"nsenter", "-t", pidStr, "-n", "ip", "link", "set", "lo", "up"},
		{"nsenter", "-t", pidStr, "-n", "ip", "link", "set", "eth0", "up"},
		{"nsenter", "-t", pidStr, "-n", "ip", "route", "add", "default", "via", BridgeGateway, "dev", "eth0"},
	}

	for _, cmdArgs := range commands {
		_ = exec.Command(cmdArgs[0], cmdArgs[1:]...).Run()
	}

	return ipAddr, nil
}

func CleanupContainerNetwork(containerID string) {
	shortID := containerID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	hostVeth := "vh-" + shortID
	_ = exec.Command("ip", "link", "delete", hostVeth).Run()
}

func generateContainerIP(containerID string) string {
	hash := sha256.Sum256([]byte(containerID))
	lastOctet := 2 + (int(hash[0]) % 250)
	return fmt.Sprintf("172.19.0.%d", lastOctet)
}

func SetupPortForwarding(containerIP string, portMappings []string) error {
	_ = os.WriteFile("/proc/sys/net/ipv4/conf/all/route_localnet", []byte("1"), 0644)
	_ = os.WriteFile("/proc/sys/net/ipv4/conf/containia0/route_localnet", []byte("1"), 0644)

	for _, mapping := range portMappings {
		parts := strings.Split(mapping, ":")
		var hostPort, contPort string
		if len(parts) == 2 {
			hostPort = parts[0]
			contPort = parts[1]
		} else if len(parts) == 1 {
			hostPort = parts[0]
			contPort = parts[0]
		} else {
			continue
		}

		targetDest := fmt.Sprintf("%s:%s", containerIP, contPort)

		_ = exec.Command("iptables", "-C", "FORWARD", "-p", "tcp", "-d", containerIP, "--dport", contPort, "-j", "ACCEPT").Run()
		_ = exec.Command("iptables", "-A", "FORWARD", "-p", "tcp", "-d", containerIP, "--dport", contPort, "-j", "ACCEPT").Run()

		_ = exec.Command("iptables", "-t", "nat", "-C", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
		_ = exec.Command("iptables", "-t", "nat", "-A", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()

		_ = exec.Command("iptables", "-t", "nat", "-C", "OUTPUT", "-p", "tcp", "-o", "lo", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
		_ = exec.Command("iptables", "-t", "nat", "-A", "OUTPUT", "-p", "tcp", "-o", "lo", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
	}
	return nil
}

func CleanupPortForwarding(containerIP string, portMappings []string) {
	for _, mapping := range portMappings {
		parts := strings.Split(mapping, ":")
		var hostPort, contPort string
		if len(parts) == 2 {
			hostPort = parts[0]
			contPort = parts[1]
		} else if len(parts) == 1 {
			hostPort = parts[0]
			contPort = parts[0]
		} else {
			continue
		}

		targetDest := fmt.Sprintf("%s:%s", containerIP, contPort)
		_ = exec.Command("iptables", "-D", "FORWARD", "-p", "tcp", "-d", containerIP, "--dport", contPort, "-j", "ACCEPT").Run()
		_ = exec.Command("iptables", "-t", "nat", "-D", "PREROUTING", "-p", "tcp", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
		_ = exec.Command("iptables", "-t", "nat", "-D", "OUTPUT", "-p", "tcp", "-o", "lo", "--dport", hostPort, "-j", "DNAT", "--to-destination", targetDest).Run()
	}
}

func SyncAllContainerHosts() error {
	containersDir := config.GetContainersDir()
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		return err
	}

	var activeHosts []string
	activeHosts = append(activeHosts,
		"127.0.0.1   localhost",
		"::1         localhost ip6-localhost ip6-loopback",
		fmt.Sprintf("%s containia-bridge host.containia.internal", BridgeGateway),
	)

	type ContInfo struct {
		ID   string
		Name string
		IP   string
	}
	var activeContainers []ContInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cid := entry.Name()
		statePath := config.GetContainerStatePath(cid)
		data, err := os.ReadFile(statePath)
		if err != nil {
			continue
		}
		var st config.ContainerState
		if err := json.Unmarshal(data, &st); err != nil {
			continue
		}
		if st.IPAddress != "" && (st.Status == config.StatusRunning || st.Status == config.StatusCreated) {
			activeContainers = append(activeContainers, ContInfo{
				ID:   cid,
				Name: st.Name,
				IP:   st.IPAddress,
			})
			activeHosts = append(activeHosts, fmt.Sprintf("%s %s %s", st.IPAddress, st.Name, cid[:min(len(cid), 12)]))
		}
	}

	hostsContent := strings.Join(activeHosts, "\n") + "\n"

	for _, c := range activeContainers {
		mergedDir := config.GetContainerMergedDir(c.ID)
		etcDir := filepath.Join(mergedDir, "etc")
		if _, err := os.Stat(etcDir); err == nil {
			hostsPath := filepath.Join(etcDir, "hosts")
			_ = os.WriteFile(hostsPath, []byte(hostsContent), 0644)
		}
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
